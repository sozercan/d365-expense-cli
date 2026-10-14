package expense

import (
	"context"
	"errors"
	"fmt"
	"math"
)

// AddReportReceiptsRequest uploads PNG receipts to an existing Draft's
// Receipts tab, in slice order.
type AddReportReceiptsRequest struct {
	ReportNumber   string
	Receipts       []CreateReportReceiptInput
	UploadContract ReceiptUploadContract
}

// AddReportReceiptsResult contains only non-secret outcome data.
type AddReportReceiptsResult struct {
	ReceiptCountBefore int
	ReceiptCountAfter  int
	Receipts           []CreateReportReceiptResult
}

// AddReportReceipts opens an existing Draft, uploads every receipt through the
// same verified flow used when creating a report, and saves and closes it.
func (client *Client) AddReportReceipts(ctx context.Context, request AddReportReceiptsRequest) (result AddReportReceiptsResult, err error) {
	if err := client.validateExistingReport(ctx, request.ReportNumber); err != nil {
		return AddReportReceiptsResult{}, err
	}
	if err := request.UploadContract.validate(); err != nil {
		return AddReportReceiptsResult{}, err
	}
	for index, receipt := range request.Receipts {
		if _, err := validateReceiptInput(receipt.Notes, receipt.Receipt, request.UploadContract.maxSupportedSingleFileSize); err != nil {
			return AddReportReceiptsResult{}, fmt.Errorf("expense: validate receipt %d: %w", index+1, err)
		}
	}
	headroom, err := createReportWithReceiptsSequenceHeadroom(len(request.Receipts))
	if err != nil {
		return AddReportReceiptsResult{}, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-existingReportHeadroom-headroom {
		return AddReportReceiptsResult{}, errors.New("expense: client sequence lacks headroom for adding receipts")
	}
	draft, _, err := client.openDraftReport(ctx, request.ReportNumber)
	if err != nil {
		return AddReportReceiptsResult{}, err
	}
	defer func() {
		if err != nil {
			err = markOperationUncertain(err)
		}
	}()
	receiptClient, before, attached, err := client.uploadReceiptsToOpenDraft(ctx, draft, request.Receipts, request.UploadContract, false)
	if err != nil {
		return AddReportReceiptsResult{}, err
	}
	body, err := receiptClient.saveAndClose(ctx)
	client.syncReceiptSession(receiptClient)
	if err != nil {
		return AddReportReceiptsResult{}, err
	}
	if err := client.restoreWorkspace(body, "save and close response"); err != nil {
		return AddReportReceiptsResult{}, err
	}
	return AddReportReceiptsResult{
		ReceiptCountBefore: before,
		ReceiptCountAfter:  before + len(attached),
		Receipts:           attached,
	}, nil
}
