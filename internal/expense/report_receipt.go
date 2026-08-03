package expense

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

const (
	createReportWithReceiptsBaseRequestCount       = 4
	createReportWithReceiptsBaseSequenceHeadroom   = int64(5)
	createReportWithReceiptsPerReceiptRequestCount = receiptRequestCount - 1
)

// PlanCreateReportWithReceipts returns an offline, credential-free description
// of the report creation, ordered receipt attachments, and explicit final
// action. It does not open any receipt readers.
func (client *Client) PlanCreateReportWithReceipts(request CreateReportWithReceiptsRequest) (CreateReportWithReceiptsPlan, error) {
	if client == nil {
		return CreateReportWithReceiptsPlan{}, errors.New("expense: client is nil")
	}
	if err := validateCreateReportWithReceiptsRequest(request); err != nil {
		return CreateReportWithReceiptsPlan{}, err
	}
	requestCount, err := createReportWithReceiptsRequestCount(len(request.Receipts))
	if err != nil {
		return CreateReportWithReceiptsPlan{}, err
	}

	receipts := make([]CreateReportReceiptSummary, len(request.Receipts))
	for index, receipt := range request.Receipts {
		receipts[index] = CreateReportReceiptSummary{
			Receipt: ReceiptSummary{
				Filename:  receipt.Receipt.Filename,
				MediaType: receipt.Receipt.MediaType,
				Size:      receipt.Receipt.Size,
			},
		}
	}
	finalAction := "after all receipts succeed, click only SaveAndClose as the final report action"
	if request.FinalAction == ReportFinalActionSubmit {
		finalAction = "after all receipts succeed, submit using only the exact discovered SubmitButton"
	}
	return CreateReportWithReceiptsPlan{
		Purpose:      request.Purpose,
		Receipts:     receipts,
		RequestCount: requestCount,
		Actions: []string{
			"open new expense report",
			"set purpose and create Draft while leaving its details form open",
			"activate the dynamically discovered Receipts tab once",
			"discover the tab response's New receipt control",
			"attach every receipt sequentially in request order",
			"for each receipt, perform a Draft-status preflight and use fresh upload metadata",
			"for each receipt, validate and upload one PNG",
			"after each receipt, verify Draft status and the cumulative receipt count",
			finalAction,
		},
	}, nil
}

// CreateReportWithReceipts creates a report, attaches every receipt, and
// performs the request's explicit final action. It does not approve, post,
// recall, or run generic workflow commands.
func (client *Client) CreateReportWithReceipts(ctx context.Context, request CreateReportWithReceiptsRequest) (CreateReportWithReceiptsResult, error) {
	if client == nil {
		return CreateReportWithReceiptsResult{}, errors.New("expense: client is nil")
	}
	if ctx == nil {
		return CreateReportWithReceiptsResult{}, errors.New("expense: context is nil")
	}
	if err := validateCreateReportWithReceiptsRequest(request); err != nil {
		return CreateReportWithReceiptsResult{}, err
	}
	sequenceHeadroom, err := createReportWithReceiptsSequenceHeadroom(len(request.Receipts))
	if err != nil {
		return CreateReportWithReceiptsResult{}, err
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-sequenceHeadroom {
		return CreateReportWithReceiptsResult{}, errors.New("expense: client sequence lacks headroom for report creation with receipts")
	}

	draft, err := client.createDraftDetails(ctx, request.Purpose)
	if err != nil {
		return CreateReportWithReceiptsResult{}, err
	}
	if request.FinalAction == ReportFinalActionSubmit {
		if err := dynamics.ValidateSubmitButton(draft.submitButton, draft.detailsRootID); err != nil {
			return CreateReportWithReceiptsResult{}, fmt.Errorf("expense: submit control is unavailable or unsupported: %w", err)
		}
	}
	createdReceiptModel, err := dynamics.DiscoverReceiptModel(draft.responseBody)
	if err != nil {
		return CreateReportWithReceiptsResult{}, err
	}
	if createdReceiptModel.ReceiptsTabPage.ID == "" || createdReceiptModel.ReceiptsTabPage.RootID != draft.detailsRootID {
		return CreateReportWithReceiptsResult{}, errors.New("expense: ReceiptsTabPage control not found in new report details form")
	}
	beforeReceiptCount := -1
	if createdReceiptModel.ReceiptCountPresent {
		beforeReceiptCount = createdReceiptModel.ReceiptCount
	}

	activate, err := buildActivateReceiptsTabMessage(
		client.nextClientSequence,
		draft.detailsRootID,
		createdReceiptModel.ReceiptsTabPage.ID,
	)
	if err != nil {
		return CreateReportWithReceiptsResult{}, err
	}
	activateBody, err := client.sendValidated(ctx, []dynamics.Message{activate}, func(envelope dynamics.Envelope) error {
		return validateActivateReceiptsTabEnvelope(
			envelope,
			draft.detailsRootID,
			createdReceiptModel.ReceiptsTabPage.ID,
		)
	})
	if err != nil {
		return CreateReportWithReceiptsResult{}, fmt.Errorf("expense: activate Receipts tab: %w", err)
	}
	activatedModel, err := dynamics.DiscoverReceiptModel(activateBody)
	if err != nil {
		return CreateReportWithReceiptsResult{}, err
	}
	if err := validateReceiptModelForOpenDraft(activatedModel, draft.reportNumber); err != nil {
		return CreateReportWithReceiptsResult{}, err
	}
	if activatedModel.AddReceiptButton.ID == "" || activatedModel.AddReceiptButton.Name != dynamics.ControlNewReceiptButton ||
		activatedModel.AddReceiptButton.RootID != draft.detailsRootID {
		return CreateReportWithReceiptsResult{}, errors.New("expense: activated Receipts tab response lacks NewReceiptButton in the new report")
	}
	if activatedModel.ReceiptCountPresent {
		if beforeReceiptCount >= 0 && activatedModel.ReceiptCount != beforeReceiptCount {
			return CreateReportWithReceiptsResult{}, errors.New("expense: receipt count changed while activating the Receipts tab")
		}
		beforeReceiptCount = activatedModel.ReceiptCount
	}
	if beforeReceiptCount < 0 {
		return CreateReportWithReceiptsResult{}, errors.New("expense: new draft receipt count is missing after activating the Receipts tab")
	}
	if len(request.Receipts) > maxInt()-beforeReceiptCount {
		return CreateReportWithReceiptsResult{}, errors.New("expense: cumulative receipt count would overflow")
	}

	saveAndCloseID := draft.saveAndCloseID
	if activatedModel.SaveAndClose.ID != "" {
		if activatedModel.SaveAndClose.RootID != draft.detailsRootID {
			return CreateReportWithReceiptsResult{}, errors.New("expense: activated Receipts tab returned SaveAndClose outside the new report")
		}
		saveAndCloseID = activatedModel.SaveAndClose.ID
	}
	submitButton := draft.submitButton
	if activatedSubmitButton, ok := activatedModel.FindSubmitButtonInRoot(draft.detailsRootID); ok {
		mergedSubmitButton := mergeSubmitButtonCandidate(submitButton, activatedSubmitButton)
		if request.FinalAction == ReportFinalActionSubmit {
			if err := dynamics.ValidateSubmitButton(mergedSubmitButton, draft.detailsRootID); err != nil {
				return CreateReportWithReceiptsResult{}, fmt.Errorf("expense: activated Receipts tab returned an unsupported SubmitButton: %w", err)
			}
		}
		submitButton = mergedSubmitButton
	}

	uploadEndpoint, err := receiptUploadEndpoint(client.origin, request.UploadContract)
	if err != nil {
		return CreateReportWithReceiptsResult{}, err
	}
	receiptClient := &ReceiptClient{
		httpClient:                 client.httpClient,
		endpoint:                   client.endpoint,
		uploadEndpoint:             uploadEndpoint,
		origin:                     client.origin,
		headers:                    client.headers.Clone(),
		cookies:                    cloneCookies(client.cookies),
		company:                    client.company,
		language:                   client.language,
		channelID:                  client.channelID,
		lastServerSequence:         client.lastServerSequence,
		nextClientSequence:         client.nextClientSequence,
		reportNumber:               draft.reportNumber,
		detailsRootID:              draft.detailsRootID,
		addReceiptButtonID:         activatedModel.AddReceiptButton.ID,
		saveAndCloseID:             saveAndCloseID,
		submitButton:               submitButton,
		maxChunkSize:               request.UploadContract.maxChunkSize,
		maxSupportedSingleFileSize: request.UploadContract.maxSupportedSingleFileSize,
		documentType:               request.UploadContract.documentType,
		multipartFieldOrder:        append([]string(nil), request.UploadContract.multipartFieldOrder...),
		lastReceiptCount:           beforeReceiptCount,
	}

	attachedReceipts := make([]CreateReportReceiptResult, 0, len(request.Receipts))
	for index, receipt := range request.Receipts {
		attached, attachErr := receiptClient.attachReceiptWithoutSave(ctx, AttachReceiptRequest{
			ReportNumber: draft.reportNumber,
			Notes:        receipt.Notes,
			Receipt:      receipt.Receipt,
		})
		client.syncReceiptSession(receiptClient)
		if attachErr != nil {
			return CreateReportWithReceiptsResult{}, fmt.Errorf(
				"expense: attach receipt %d (%q) to new draft: %w",
				index+1,
				receipt.Receipt.Filename,
				attachErr,
			)
		}
		expectedCount := beforeReceiptCount + index + 1
		if attached.Status != "Draft" || attached.ReceiptCount != expectedCount {
			return CreateReportWithReceiptsResult{}, fmt.Errorf(
				"expense: receipt %d confirmation did not preserve Draft status and cumulative count %d",
				index+1,
				expectedCount,
			)
		}
		attachedReceipts = append(attachedReceipts, CreateReportReceiptResult{
			Attached:          attached.Attached,
			ReceiptCountAfter: attached.ReceiptCount,
		})
	}

	status := "Draft"
	savedAndClosed := false
	submitted := false
	if request.FinalAction == ReportFinalActionSubmit {
		client.syncReceiptSession(receiptClient)
		status, err = client.submitOpenDraft(ctx, draft.reportNumber, draft.detailsRootID, receiptClient.submitButton)
		if err != nil {
			return CreateReportWithReceiptsResult{}, err
		}
		submitted = true
	} else {
		if err := receiptClient.saveAndClose(ctx); err != nil {
			client.syncReceiptSession(receiptClient)
			return CreateReportWithReceiptsResult{}, err
		}
		client.syncReceiptSession(receiptClient)
		savedAndClosed = true
	}

	return CreateReportWithReceiptsResult{
		Purpose:            request.Purpose,
		ReportNumber:       draft.reportNumber,
		Status:             status,
		ReceiptCountBefore: beforeReceiptCount,
		ReceiptCountAfter:  beforeReceiptCount + len(request.Receipts),
		Receipts:           attachedReceipts,
		SavedAndClosed:     savedAndClosed,
		Submitted:          submitted,
	}, nil
}

func validateCreateReportWithReceiptsRequest(request CreateReportWithReceiptsRequest) error {
	if err := validatePurpose(request.Purpose); err != nil {
		return err
	}
	if err := request.UploadContract.validate(); err != nil {
		return err
	}
	if len(request.Receipts) == 0 {
		return errors.New("expense: at least one receipt is required")
	}
	if err := validateReportFinalAction(request.FinalAction); err != nil {
		return err
	}
	for index, receipt := range request.Receipts {
		if _, err := validateReceiptInput(
			receipt.Notes,
			receipt.Receipt,
			request.UploadContract.maxSupportedSingleFileSize,
		); err != nil {
			return fmt.Errorf("expense: validate receipt %d: %w", index+1, err)
		}
	}
	return nil
}

func createReportWithReceiptsRequestCount(receiptCount int) (int, error) {
	if receiptCount < 1 {
		return 0, errors.New("expense: at least one receipt is required")
	}
	if receiptCount > (maxInt()-createReportWithReceiptsBaseRequestCount)/createReportWithReceiptsPerReceiptRequestCount {
		return 0, errors.New("expense: receipt count is too large")
	}
	return createReportWithReceiptsBaseRequestCount + receiptCount*createReportWithReceiptsPerReceiptRequestCount, nil
}

func createReportWithReceiptsSequenceHeadroom(receiptCount int) (int64, error) {
	if receiptCount < 1 {
		return 0, errors.New("expense: at least one receipt is required")
	}
	count := int64(receiptCount)
	if count > (math.MaxInt64-createReportWithReceiptsBaseSequenceHeadroom)/receiptAttachmentSequenceHeadroom {
		return 0, errors.New("expense: receipt count is too large")
	}
	return createReportWithReceiptsBaseSequenceHeadroom + count*receiptAttachmentSequenceHeadroom, nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func (client *Client) syncReceiptSession(receiptClient *ReceiptClient) {
	client.headers = receiptClient.headers.Clone()
	client.cookies = cloneCookies(receiptClient.cookies)
	client.lastServerSequence = receiptClient.lastServerSequence
	client.nextClientSequence = receiptClient.nextClientSequence
}

func validateReceiptModelForOpenDraft(model dynamics.ReceiptModel, reportNumber string) error {
	if model.ReportNumber != "" && model.ReportNumber != reportNumber {
		return fmt.Errorf("expense: activated Receipts tab response is for a different report: got %s want %s", model.ReportNumber, reportNumber)
	}
	if model.Status != "" && model.Status != "Draft" {
		return errors.New("expense: activated Receipts tab response report status is not Draft")
	}
	return nil
}

func receiptUploadEndpoint(origin string, contract ReceiptUploadContract) (*url.URL, error) {
	if err := contract.validate(); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(origin + contract.endpointPath)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || endpoint.Path != contract.endpointPath || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("expense: invalid receipt upload endpoint")
	}
	if strings.ToLower(endpoint.Scheme)+"://"+endpoint.Host != origin {
		return nil, errors.New("expense: receipt upload endpoint origin mismatch")
	}
	return endpoint, nil
}

func cloneCookies(source []*http.Cookie) []*http.Cookie {
	clones := make([]*http.Cookie, 0, len(source))
	for _, cookie := range source {
		if cookie == nil {
			continue
		}
		clone := *cookie
		clones = append(clones, &clone)
	}
	return clones
}

func buildActivateReceiptsTabMessage(sequence int64, detailsRootID, receiptsTabPageID string) (dynamics.Message, error) {
	if sequence <= 0 || strings.TrimSpace(detailsRootID) == "" || strings.TrimSpace(receiptsTabPageID) == "" {
		return dynamics.Message{}, errors.New("expense: ActivateTab command targets are incomplete")
	}
	// Dynamics serializes ResetThrottleTime and ThrottleFirst as string booleans
	// for this command. Preserve that observed wire shape rather than routing it
	// through CommandInteraction's boolean ResetThrottleTime field.
	interaction, err := json.Marshal(map[string]any{
		"$type":                dynamics.InteractionTypeCommand,
		"CallbackId":           "",
		"CommandName":          dynamics.CommandActivateTab,
		"FailureCallbackId":    "",
		"NamedParameters":      map[string]any{},
		"NoAsyncIncrement":     false,
		"PositionalParameters": []any{},
		"PriorityPosition":     false,
		"ResetThrottleTime":    "true",
		"RootId":               detailsRootID,
		"TargetId":             receiptsTabPageID,
		"Throttle":             true,
		"ThrottleFirst":        "true",
		"ThrottleId":           detailsRootID + "_TopTabs",
		"ThrottleTimestamp":    int64(0),
		"ThrottleValue":        int64(300),
		"Telemetry":            true,
	})
	if err != nil {
		return dynamics.Message{}, errors.New("expense: build ActivateTab command")
	}
	return dynamics.Message{
		SequenceNumber: sequence,
		Interactions:   []json.RawMessage{interaction},
	}, nil
}

func validateActivateReceiptsTabEnvelope(envelope dynamics.Envelope, detailsRootID, receiptsTabPageID string) error {
	if len(envelope.ExtraFields) != 0 || len(envelope.Messages) != 1 || len(envelope.Messages[0].ExtraFields) != 0 || len(envelope.Messages[0].Interactions) != 1 {
		return fmt.Errorf("%w: ActivateTab envelope shape is not allowlisted", dynamics.ErrUnsafeCommand)
	}
	expected, err := buildActivateReceiptsTabMessage(envelope.Messages[0].SequenceNumber, detailsRootID, receiptsTabPageID)
	if err != nil {
		return fmt.Errorf("%w: %v", dynamics.ErrUnsafeCommand, err)
	}
	if !bytes.Equal(bytes.TrimSpace(envelope.Messages[0].Interactions[0]), bytes.TrimSpace(expected.Interactions[0])) {
		return fmt.Errorf("%w: ActivateTab command shape or target is not allowlisted", dynamics.ErrUnsafeCommand)
	}
	return nil
}
