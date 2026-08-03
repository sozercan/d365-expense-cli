package expense

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

const (
	maxReceiptUploadResponseBody      = 1 << 20
	maxReceiptNotesBytes              = 64 << 10
	maxReceiptFileSize                = int64(1024000)
	receiptRequestCount               = 9
	receiptAttachmentSequenceHeadroom = int64(8)
	receiptSaveSequenceHeadroom       = int64(1)
	receiptSequenceHeadroomWithSave   = receiptAttachmentSequenceHeadroom + receiptSaveSequenceHeadroom
)

var (
	errReceiptReaderShort = errors.New("expense: receipt reader ended before its declared size")
	errReceiptReaderLong  = errors.New("expense: receipt reader exceeded its declared size")
	errReceiptReader      = errors.New("expense: receipt reader failed")
)

// ReceiptClient is a stateful client for attaching one bounded receipt to the
// report represented by a receipt-capable capture profile.
type ReceiptClient struct {
	mu sync.Mutex

	httpClient     *http.Client
	endpoint       *url.URL
	uploadEndpoint *url.URL
	origin         string
	headers        http.Header
	cookies        []*http.Cookie

	company   string
	language  string
	channelID int

	lastServerSequence int64
	nextClientSequence int64

	reportNumber       string
	detailsRootID      string
	addReceiptButtonID string
	saveAndCloseID     string
	submitButton       dynamics.ModelNode

	maxChunkSize               int64
	maxSupportedSingleFileSize int64
	documentType               string
	multipartFieldOrder        []string

	lastReceiptCount int
}

// NewReceiptClient validates and snapshots a captured receipt session.
func NewReceiptClient(profile *capture.ReceiptProfile, options ...Option) (*ReceiptClient, error) {
	if profile == nil {
		return nil, errors.New("expense: receipt capture profile is nil")
	}
	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("expense: validate receipt capture profile: %w", err)
	}

	endpoint, origin, err := validateEndpoint(profile.Session)
	if err != nil {
		return nil, err
	}
	headers, err := safeHeaders(profile.Session.RequestHeaders, origin)
	if err != nil {
		return nil, err
	}

	uploadEndpoint, err := url.Parse(origin + profile.Upload.EndpointPath)
	if err != nil || uploadEndpoint.Scheme == "" || uploadEndpoint.Host == "" || uploadEndpoint.Path != profile.Upload.EndpointPath || uploadEndpoint.RawQuery != "" || uploadEndpoint.Fragment != "" {
		return nil, errors.New("expense: invalid receipt upload endpoint")
	}
	if strings.ToLower(uploadEndpoint.Scheme)+"://"+uploadEndpoint.Host != origin {
		return nil, errors.New("expense: receipt upload endpoint origin mismatch")
	}
	if profile.Upload.MaxChunkSize != maxReceiptFileSize || profile.Upload.MaxSupportedSingleFileSize != maxReceiptFileSize {
		return nil, errors.New("expense: receipt upload size contract is unsupported")
	}

	configuration := clientOptions{httpClient: http.DefaultClient}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("expense: nil option")
		}
		if err := option.apply(&configuration); err != nil {
			return nil, err
		}
	}

	httpClient := *configuration.httpClient
	httpClient.Jar = nil
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errRedirect
	}

	cookies := make([]*http.Cookie, 0, len(profile.Session.Cookies))
	for _, cookie := range profile.Session.Cookies {
		if cookie == nil {
			continue
		}
		clone := *cookie
		cookies = append(cookies, &clone)
	}

	return &ReceiptClient{
		httpClient:                 &httpClient,
		endpoint:                   endpoint,
		uploadEndpoint:             uploadEndpoint,
		origin:                     origin,
		headers:                    headers,
		cookies:                    cookies,
		company:                    profile.Session.Company,
		language:                   profile.Session.Language,
		channelID:                  int(profile.Session.ChannelID),
		lastServerSequence:         profile.Session.LastServerSequence,
		nextClientSequence:         profile.Session.NextClientSequence,
		reportNumber:               profile.ReportNumber,
		detailsRootID:              profile.DetailsFormRootID,
		addReceiptButtonID:         profile.AddReceipts.TargetID,
		saveAndCloseID:             profile.SaveAndClose.TargetID,
		maxChunkSize:               profile.Upload.MaxChunkSize,
		maxSupportedSingleFileSize: profile.Upload.MaxSupportedSingleFileSize,
		documentType:               profile.Upload.DocumentType,
		multipartFieldOrder:        append([]string(nil), profile.Upload.MultipartFieldOrder...),
		lastReceiptCount:           profile.ReceiptCount,
	}, nil
}

// PlanAttachReceipt returns an offline, credential-free description of the
// receipt operation. It does not open the receipt reader.
func (client *ReceiptClient) PlanAttachReceipt(request AttachReceiptRequest) (AttachReceiptPlan, error) {
	if client == nil {
		return AttachReceiptPlan{}, errors.New("expense: receipt client is nil")
	}
	if _, err := client.validateRequest(request); err != nil {
		return AttachReceiptPlan{}, err
	}
	return AttachReceiptPlan{
		ReportNumber: request.ReportNumber,
		Receipt: ReceiptSummary{
			Filename:  request.Receipt.Filename,
			MediaType: request.Receipt.MediaType,
			Size:      request.Receipt.Size,
		},
		RequestCount: receiptRequestCount,
		Actions: []string{
			"open Add receipts for a Draft-status preflight",
			"close and reopen Add receipts with fresh upload metadata",
			"validate receipt name and file",
			"upload one PNG",
			"close the upload dialog",
			"confirm the receipt",
			"verify Draft report and receipt count",
			"save and close the report",
		},
	}, nil
}

// AttachReceipt attaches one PNG without retries or compensating actions, then
// saves and closes the report. The combined multi-receipt workflow uses the
// same attachment implementation but defers SaveAndClose until every receipt
// has been confirmed.
func (client *ReceiptClient) AttachReceipt(ctx context.Context, request AttachReceiptRequest) (AttachReceiptResult, error) {
	if client == nil {
		return AttachReceiptResult{}, errors.New("expense: receipt client is nil")
	}
	if ctx == nil {
		return AttachReceiptResult{}, errors.New("expense: context is nil")
	}
	documentName, err := client.validateRequest(request)
	if err != nil {
		return AttachReceiptResult{}, err
	}

	client.mu.Lock()
	defer client.mu.Unlock()

	if client.nextClientSequence > math.MaxInt64-receiptSequenceHeadroomWithSave {
		return AttachReceiptResult{}, errors.New("expense: client sequence lacks headroom for receipt attachment")
	}

	attached, err := client.attachReceiptLocked(ctx, request, documentName)
	if err != nil {
		return AttachReceiptResult{}, err
	}
	if err := client.saveAndCloseLocked(ctx); err != nil {
		return AttachReceiptResult{}, err
	}
	attached.SavedAndClosed = true
	return attached, nil
}

// attachReceiptWithoutSave attaches and verifies one receipt while keeping the
// report details form open. It is intentionally unexported so callers cannot
// accidentally bypass the public single-receipt SaveAndClose behavior.
func (client *ReceiptClient) attachReceiptWithoutSave(ctx context.Context, request AttachReceiptRequest) (AttachReceiptResult, error) {
	if client == nil {
		return AttachReceiptResult{}, errors.New("expense: receipt client is nil")
	}
	if ctx == nil {
		return AttachReceiptResult{}, errors.New("expense: context is nil")
	}
	documentName, err := client.validateRequest(request)
	if err != nil {
		return AttachReceiptResult{}, err
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-receiptAttachmentSequenceHeadroom {
		return AttachReceiptResult{}, errors.New("expense: client sequence lacks headroom for receipt attachment")
	}
	return client.attachReceiptLocked(ctx, request, documentName)
}

// saveAndClose saves the current Draft after one or more successful receipt
// attachments. Callers must not invoke it until every requested attachment has
// passed its Draft-status and cumulative-count checks.
func (client *ReceiptClient) saveAndClose(ctx context.Context) error {
	if client == nil {
		return errors.New("expense: receipt client is nil")
	}
	if ctx == nil {
		return errors.New("expense: context is nil")
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-receiptSaveSequenceHeadroom {
		return errors.New("expense: client sequence lacks headroom to save and close receipt report")
	}
	return client.saveAndCloseLocked(ctx)
}

func (client *ReceiptClient) attachReceiptLocked(ctx context.Context, request AttachReceiptRequest, documentName string) (AttachReceiptResult, error) {
	beforeReceiptCount := client.lastReceiptCount
	if beforeReceiptCount < 0 || beforeReceiptCount == maxInt() {
		return AttachReceiptResult{}, errors.New("expense: receipt count cannot safely increase")
	}

	open := dynamics.BuildOpenNewReceiptMessage(client.nextClientSequence, client.detailsRootID, client.addReceiptButtonID)
	openBody, err := client.sendReceipt(ctx, []dynamics.Message{open}, dynamics.ReceiptCommandTargets{
		DetailsRootID:      client.detailsRootID,
		NewReceiptButtonID: client.addReceiptButtonID,
	})
	if err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: open Add receipts preflight: %w", err)
	}
	preflightModel, err := dynamics.DiscoverReceiptModel(openBody)
	if err != nil {
		return AttachReceiptResult{}, err
	}
	if err := client.validateDiscoveredState(preflightModel, true, false); err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: validate Add receipts preflight response: %w", err)
	}
	if preflightModel.ReceiptCountPresent && preflightModel.ReceiptCount != beforeReceiptCount {
		return AttachReceiptResult{}, errors.New("expense: receipt count is stale before attachment")
	}
	if preflightModel.AddNewReceiptForm.ID == "" || preflightModel.CloseButton.ID == "" ||
		preflightModel.CloseButton.RootID != preflightModel.AddNewReceiptForm.ID {
		return AttachReceiptResult{}, errors.New("expense: receipt Draft preflight controls are incomplete")
	}

	preflightTargets := dynamics.ReceiptCommandTargets{
		DialogRootID:  preflightModel.AddNewReceiptForm.ID,
		CloseButtonID: preflightModel.CloseButton.ID,
	}
	closePreflight := dynamics.BuildReceiptCloseButtonMessage(
		client.nextClientSequence, preflightTargets.DialogRootID, preflightTargets.CloseButtonID,
	)
	closeBody, err := client.sendReceipt(ctx, []dynamics.Message{closePreflight}, preflightTargets)
	if err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: close receipt Draft preflight: %w", err)
	}
	closeModel, err := dynamics.DiscoverReceiptModel(closeBody)
	if err != nil {
		return AttachReceiptResult{}, err
	}
	if err := client.validateDiscoveredState(closeModel, false, true); err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: validate closed preflight response: %w", err)
	}
	if closeModel.ReceiptCountPresent && closeModel.ReceiptCount != beforeReceiptCount {
		return AttachReceiptResult{}, errors.New("expense: receipt count changed during Draft preflight")
	}

	open = dynamics.BuildOpenNewReceiptMessage(client.nextClientSequence, client.detailsRootID, client.addReceiptButtonID)
	openBody, err = client.sendReceipt(ctx, []dynamics.Message{open}, dynamics.ReceiptCommandTargets{
		DetailsRootID:      client.detailsRootID,
		NewReceiptButtonID: client.addReceiptButtonID,
	})
	if err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: reopen Add receipts: %w", err)
	}
	openModel, err := dynamics.DiscoverReceiptModel(openBody)
	if err != nil {
		return AttachReceiptResult{}, err
	}
	if err := client.validateDiscoveredState(openModel, true, false); err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: validate reopened Add receipts response: %w", err)
	}
	if openModel.ReceiptCountPresent && openModel.ReceiptCount != beforeReceiptCount {
		return AttachReceiptResult{}, errors.New("expense: receipt count is stale before upload")
	}
	if openModel.AddNewReceiptForm.ID == "" || openModel.UploadControl.ID == "" || openModel.OKButton.ID == "" ||
		openModel.UploadControl.RootID != openModel.AddNewReceiptForm.ID || openModel.OKButton.RootID != openModel.AddNewReceiptForm.ID {
		return AttachReceiptResult{}, errors.New("expense: fresh receipt dialog controls are incomplete")
	}
	if invalidTransient(openModel.AccessToken) || strings.TrimSpace(openModel.CurrentRecID) == "" ||
		strings.TrimSpace(openModel.CurrentDocuRefRecID) == "" || strings.TrimSpace(openModel.CurrentTableID) == "" ||
		openModel.SelectedDocumentType != client.documentType {
		return AttachReceiptResult{}, errors.New("expense: fresh receipt upload metadata is incomplete")
	}

	targets := dynamics.ReceiptCommandTargets{
		DialogRootID:    openModel.AddNewReceiptForm.ID,
		UploadControlID: openModel.UploadControl.ID,
		OKButtonID:      openModel.OKButton.ID,
	}

	documents := dynamics.BuildReceiptDocuNameCheckFileMessages(
		client.nextClientSequence,
		targets.DialogRootID,
		targets.UploadControlID,
		documentName,
		request.Receipt.Filename,
	)
	documentBody, err := client.sendReceipt(ctx, documents, targets)
	if err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: validate receipt file: %w", err)
	}
	if err := client.validateOptionalReceiptState(documentBody); err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: validate receipt name response: %w", err)
	}

	check := dynamics.BuildReceiptCheckFileMessage(
		client.nextClientSequence,
		targets.DialogRootID,
		targets.UploadControlID,
		documentName,
		request.Receipt.Filename,
	)
	checkBody, err := client.sendReceipt(ctx, []dynamics.Message{check}, targets)
	if err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: revalidate receipt file: %w", err)
	}
	if err := client.validateOptionalReceiptState(checkBody); err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: validate pre-upload receipt response: %w", err)
	}

	fileID, err := client.uploadReceipt(ctx, request, documentName, openModel)
	if err != nil {
		return AttachReceiptResult{}, err
	}

	complete := dynamics.BuildReceiptUploadedFileCloseDialogMessage(
		client.nextClientSequence,
		targets.DialogRootID,
		targets.UploadControlID,
		fileID,
	)
	completeBody, err := client.sendReceipt(ctx, []dynamics.Message{complete}, targets)
	if err != nil {
		return AttachReceiptResult{}, errors.New("expense: finalize receipt upload request failed")
	}
	if err := client.validateOptionalReceiptState(completeBody); err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: validate upload completion response: %w", err)
	}

	ok := dynamics.BuildReceiptOKClickMessage(client.nextClientSequence, targets.DialogRootID, targets.OKButtonID)
	okBody, err := client.sendReceipt(ctx, []dynamics.Message{ok}, targets)
	if err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: confirm receipt: %w", err)
	}
	okModel, err := dynamics.DiscoverReceiptModel(okBody)
	if err != nil {
		return AttachReceiptResult{}, err
	}
	if err := client.validateDiscoveredState(okModel, false, true); err != nil {
		return AttachReceiptResult{}, fmt.Errorf("expense: validate receipt confirmation response: %w", err)
	}
	if !okModel.ReceiptCountPresent || okModel.ReceiptCount != beforeReceiptCount+1 {
		return AttachReceiptResult{}, errors.New("expense: receipt count did not increase by exactly one")
	}

	if okModel.AddReceiptButton.ID != "" {
		if okModel.AddReceiptButton.Name != dynamics.ControlNewReceiptButton || okModel.AddReceiptButton.RootID != client.detailsRootID {
			return AttachReceiptResult{}, errors.New("expense: dynamic NewReceiptButton is outside the captured report")
		}
		client.addReceiptButtonID = okModel.AddReceiptButton.ID
	}
	if okModel.SaveAndClose.ID != "" {
		if okModel.SaveAndClose.RootID != client.detailsRootID {
			return AttachReceiptResult{}, errors.New("expense: dynamic SaveAndClose control is outside the captured report")
		}
		client.saveAndCloseID = okModel.SaveAndClose.ID
	}
	if submitButton, ok := okModel.FindSubmitButtonInRoot(client.detailsRootID); ok {
		// Receipt attachment is shared by draft-only and submit workflows. Keep
		// the freshest candidate from this report, but defer trust validation
		// until the caller actually chooses Submit; draft-only SaveAndClose must
		// not depend on irrelevant or tenant-specific SubmitButton metadata.
		client.submitButton = submitButton
	}
	if client.saveAndCloseID == "" {
		return AttachReceiptResult{}, errors.New("expense: SaveAndClose control is missing")
	}

	client.lastReceiptCount = okModel.ReceiptCount
	return AttachReceiptResult{
		ReportNumber: request.ReportNumber,
		Status:       "Draft",
		ReceiptCount: okModel.ReceiptCount,
		Attached: AttachedReceipt{
			Filename: request.Receipt.Filename,
			Size:     request.Receipt.Size,
		},
	}, nil
}

func (client *ReceiptClient) saveAndCloseLocked(ctx context.Context) error {
	if client.detailsRootID == "" || client.saveAndCloseID == "" {
		return errors.New("expense: SaveAndClose control is missing")
	}
	save := dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, client.detailsRootID, client.saveAndCloseID)
	saveBody, err := client.sendReceipt(ctx, []dynamics.Message{save}, dynamics.ReceiptCommandTargets{
		DetailsRootID:  client.detailsRootID,
		SaveAndCloseID: client.saveAndCloseID,
	})
	if err != nil {
		return fmt.Errorf("expense: save and close receipt report: %w", err)
	}
	if err := client.validateOptionalReceiptState(saveBody); err != nil {
		return fmt.Errorf("expense: validate SaveAndClose response: %w", err)
	}
	return nil
}

func (client *ReceiptClient) validateRequest(request AttachReceiptRequest) (string, error) {
	if !utf8.ValidString(request.ReportNumber) || strings.TrimSpace(request.ReportNumber) == "" {
		return "", errors.New("expense: report number is empty or invalid")
	}
	if request.ReportNumber != client.reportNumber {
		return "", errors.New("expense: receipt request does not target the captured report")
	}
	return validateReceiptInput(request.Notes, request.Receipt, client.maxSupportedSingleFileSize)
}

func validateReceiptInput(notes string, receipt ReceiptInput, maxSupportedSingleFileSize int64) (string, error) {
	if !utf8.ValidString(notes) || len(notes) > maxReceiptNotesBytes {
		return "", errors.New("expense: receipt notes are invalid or too large")
	}

	filename := receipt.Filename
	if !utf8.ValidString(filename) || strings.TrimSpace(filename) == "" || filename == "." || filename == ".." ||
		filepath.Base(filename) != filename || strings.ContainsAny(filename, "/\\\r\n\x00\"") {
		return "", errors.New("expense: receipt filename must be a non-empty basename")
	}
	if receipt.MediaType != "image/png" {
		return "", errors.New("expense: receipt media type must be image/png")
	}
	if receipt.Size <= 0 || receipt.Size > maxReceiptFileSize || receipt.Size > maxSupportedSingleFileSize {
		return "", errors.New("expense: receipt size is outside the supported single-file limit")
	}
	if receipt.Open == nil {
		return "", errors.New("expense: receipt reader factory is nil")
	}

	documentName := strings.TrimSuffix(filename, filepath.Ext(filename))
	if strings.TrimSpace(documentName) == "" {
		return "", errors.New("expense: receipt document name is empty")
	}
	return documentName, nil
}

func (client *ReceiptClient) validateDiscoveredState(model dynamics.ReceiptModel, requireReport, requireStatus bool) error {
	if requireReport && strings.TrimSpace(model.ReportNumber) == "" {
		return errors.New("expense: receipt response report number is missing")
	}
	if model.ReportNumber != "" && model.ReportNumber != client.reportNumber {
		return fmt.Errorf("expense: receipt response is for a different report: got %s want %s", model.ReportNumber, client.reportNumber)
	}
	if requireStatus && model.Status == "" {
		return errors.New("expense: receipt response lacks fresh Draft status evidence")
	}
	if model.Status != "" && model.Status != "Draft" {
		return errors.New("expense: receipt response report status is not Draft")
	}
	return nil
}

func (client *ReceiptClient) validateOptionalReceiptState(body []byte) error {
	model, err := dynamics.DiscoverReceiptModel(body)
	if err != nil {
		return err
	}
	return client.validateDiscoveredState(model, false, false)
}

func (client *ReceiptClient) sendReceipt(ctx context.Context, messages []dynamics.Message, targets dynamics.ReceiptCommandTargets) ([]byte, error) {
	envelope := dynamics.Envelope{
		ChannelID:                      client.channelID,
		CompanyID:                      client.company,
		Language:                       client.language,
		LastAcknowledgedSequenceNumber: client.lastServerSequence,
		Messages:                       messages,
	}
	if err := dynamics.ValidateReceiptCommands(envelope, targets); err != nil {
		return nil, err
	}
	body, err := dynamics.MarshalEnvelope(envelope)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("expense: build Dynamics receipt request")
	}
	client.applyHeadersAndCookies(request, "application/json; charset=UTF-8")

	response, err := client.httpClient.Do(request)
	if response != nil {
		mergeSameOriginResponseState(client.origin, client.headers, &client.cookies, response)
	}
	if err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if errors.Is(err, errRedirect) {
			return nil, errRedirect
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("expense: send Dynamics receipt request")
	}
	if response == nil || response.Body == nil {
		return nil, errors.New("expense: Dynamics receipt response is missing")
	}
	defer response.Body.Close()

	responseBody, err := readBoundedBody(response.Body, maxResponseBody, "Dynamics receipt")
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, ErrAuthenticationExpired
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("expense: Dynamics receipt endpoint returned HTTP %d", response.StatusCode)
	}

	responseEnvelope, err := dynamics.ParseEnvelope(responseBody)
	if err != nil {
		return nil, err
	}
	if responseEnvelope.ChannelID != client.channelID {
		return nil, errors.New("expense: Dynamics response channel does not match captured session")
	}
	lastSent := messages[len(messages)-1].SequenceNumber
	if responseEnvelope.LastAcknowledgedSequenceNumber != lastSent {
		return nil, errors.New("expense: stale Dynamics receipt response acknowledgement")
	}

	maximumServerSequence := client.lastServerSequence
	for _, message := range responseEnvelope.Messages {
		if message.SequenceNumber <= client.lastServerSequence {
			return nil, errors.New("expense: stale Dynamics receipt server sequence")
		}
		if message.SequenceNumber > maximumServerSequence {
			maximumServerSequence = message.SequenceNumber
		}
	}
	if responseEnvelope.LastAcknowledgedSequenceNumber == math.MaxInt64 {
		return nil, errors.New("expense: Dynamics acknowledgement exhausted client sequence space")
	}
	client.lastServerSequence = maximumServerSequence
	client.nextClientSequence = responseEnvelope.LastAcknowledgedSequenceNumber + 1

	if issue := dynamicsIssue(responseBody); issue != "" {
		return nil, errors.New(issue)
	}
	return responseBody, nil
}

func (client *ReceiptClient) uploadReceipt(ctx context.Context, request AttachReceiptRequest, documentName string, model dynamics.ReceiptModel) (string, error) {
	reader, err := request.Receipt.Open()
	if err != nil {
		return "", errors.New("expense: open receipt reader")
	}
	if reader == nil {
		return "", errors.New("expense: receipt reader factory returned nil")
	}
	defer reader.Close()

	clientID, err := newReceiptClientID()
	if err != nil {
		return "", errors.New("expense: generate receipt upload client identifier")
	}

	values := map[string]string{
		"clientId":     clientID,
		"maxChunkSize": strconv.FormatInt(client.maxChunkSize, 10),
		"tableid":      model.CurrentTableID,
		"recid":        model.CurrentRecID,
		"companyid":    client.company,
		"accesstoken":  model.AccessToken,
		"notes":        request.Notes,
		"docuname":     documentName,
		"docutypeid":   client.documentType,
		"ischunked":    "false",
		"docuRefRecId": model.CurrentDocuRefRecID,
	}

	var metadata bytes.Buffer
	writer := multipart.NewWriter(&metadata)
	for _, field := range client.multipartFieldOrder[:len(client.multipartFieldOrder)-1] {
		if err := writer.WriteField(field, values[field]); err != nil {
			return "", errors.New("expense: build receipt multipart metadata")
		}
	}
	fileHeader := make(textproto.MIMEHeader)
	fileHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[]"; filename="%s"`, escapeMultipartValue(request.Receipt.Filename)))
	fileHeader.Set("Content-Type", request.Receipt.MediaType)
	if _, err := writer.CreatePart(fileHeader); err != nil {
		return "", errors.New("expense: build receipt multipart file part")
	}
	prefixLength := metadata.Len()
	if err := writer.Close(); err != nil {
		return "", errors.New("expense: close receipt multipart metadata")
	}
	allMetadata := metadata.Bytes()
	prefix := allMetadata[:prefixLength]
	suffix := allMetadata[prefixLength:]

	exact := &exactSizeReader{source: reader, remaining: request.Receipt.Size}
	body := io.MultiReader(bytes.NewReader(prefix), exact, bytes.NewReader(suffix))
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.uploadEndpoint.String(), io.NopCloser(body))
	if err != nil {
		return "", errors.New("expense: build receipt upload request")
	}
	httpRequest.ContentLength = int64(len(prefix)) + request.Receipt.Size + int64(len(suffix))
	httpRequest.GetBody = nil
	client.applyHeadersAndCookies(httpRequest, writer.FormDataContentType())

	response, doErr := client.httpClient.Do(httpRequest)
	if response != nil {
		mergeSameOriginResponseState(client.origin, client.headers, &client.cookies, response)
	}
	if exact.err != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return "", exact.err
	}
	if doErr != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		if errors.Is(doErr, errRedirect) {
			return "", errRedirect
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("expense: receipt upload request failed")
	}
	if response == nil || response.Body == nil {
		return "", errors.New("expense: receipt upload response is missing")
	}
	defer response.Body.Close()
	if !exact.validated {
		return "", errors.New("expense: receipt reader was not consumed exactly")
	}

	responseBody, err := readBoundedBody(response.Body, maxReceiptUploadResponseBody, "receipt upload")
	if err != nil {
		return "", err
	}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return "", ErrAuthenticationExpired
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("expense: receipt upload endpoint returned HTTP %d", response.StatusCode)
	}

	var uploaded []struct {
		FileID string `json:"fileId"`
	}
	if err := json.Unmarshal(responseBody, &uploaded); err != nil || len(uploaded) != 1 || strings.TrimSpace(uploaded[0].FileID) == "" {
		return "", errors.New("expense: receipt upload response lacks one file identifier")
	}
	return uploaded[0].FileID, nil
}

func (client *ReceiptClient) applyHeadersAndCookies(request *http.Request, contentType string) {
	request.Header = client.headers.Clone()
	request.Header.Set("Origin", client.origin)
	request.Header.Set("Content-Type", contentType)
	for _, cookie := range client.cookies {
		request.AddCookie(cookie)
	}
}

func readBoundedBody(reader io.Reader, limit int64, label string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("expense: read %s response", label)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("expense: %s response body exceeds limit", label)
	}
	return body, nil
}

func invalidTransient(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized == "" || strings.Contains(normalized, "redacted")
}

func newReceiptClientID() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	data := make([]byte, 9)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	for index := range data {
		data[index] = alphabet[int(data[index])%len(alphabet)]
	}
	return string(data), nil
}

func escapeMultipartValue(value string) string {
	return strings.NewReplacer(`\\`, `\\\\`, `"`, `\"`).Replace(value)
}

type exactSizeReader struct {
	source     io.Reader
	remaining  int64
	validated  bool
	err        error
	noProgress int
	probe      [1]byte
}

func (reader *exactSizeReader) Read(buffer []byte) (int, error) {
	if reader.err != nil {
		return 0, reader.err
	}
	if reader.validated {
		return 0, io.EOF
	}
	if len(buffer) == 0 {
		return 0, nil
	}

	if reader.remaining > 0 {
		if int64(len(buffer)) > reader.remaining {
			buffer = buffer[:reader.remaining]
		}
		n, err := reader.source.Read(buffer)
		if n > 0 {
			reader.noProgress = 0
			reader.remaining -= int64(n)
			if reader.remaining < 0 {
				reader.err = errReceiptReaderLong
				return n, reader.err
			}
			if err != nil {
				if err == io.EOF && reader.remaining == 0 {
					reader.validated = true
					return n, io.EOF
				}
				if err == io.EOF {
					reader.err = errReceiptReaderShort
				} else {
					reader.err = errReceiptReader
				}
				return n, reader.err
			}
			return n, nil
		}
		if err == io.EOF {
			reader.err = errReceiptReaderShort
			return 0, reader.err
		}
		if err != nil {
			reader.err = errReceiptReader
			return 0, reader.err
		}
		reader.noProgress++
		if reader.noProgress >= 100 {
			reader.err = errReceiptReader
			return 0, reader.err
		}
		return 0, nil
	}

	n, err := reader.source.Read(reader.probe[:])
	if n > 0 {
		reader.err = errReceiptReaderLong
		return 0, reader.err
	}
	if err == io.EOF {
		reader.validated = true
		return 0, io.EOF
	}
	if err != nil {
		reader.err = errReceiptReader
		return 0, reader.err
	}
	reader.noProgress++
	if reader.noProgress >= 100 {
		reader.err = errReceiptReader
		return 0, reader.err
	}
	return 0, nil
}
