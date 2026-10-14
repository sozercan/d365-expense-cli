package expense

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

// AttachReportReceiptRequest attaches one receipt already uploaded to a Draft
// to one of its expense lines. Line is the number shown by ShowDraftReport;
// Receipt is the receipt's file name as Dynamics lists it.
type AttachReportReceiptRequest struct {
	ReportNumber string
	Line         int
	Receipt      string
}

// AttachReportReceiptResult contains only non-secret outcome data.
type AttachReportReceiptResult struct {
	Receipt  string
	Date     string
	Category string
	Amount   string
}

// AttachReportReceipt attaches an uploaded report receipt to one expense line
// through the receipt's Attach to expense popup, then saves and closes the
// Draft.
func (client *Client) AttachReportReceipt(ctx context.Context, request AttachReportReceiptRequest) (result AttachReportReceiptResult, err error) {
	if err := client.validateExistingReport(ctx, request.ReportNumber); err != nil {
		return AttachReportReceiptResult{}, err
	}
	if request.Line < 1 || strings.TrimSpace(request.Receipt) == "" {
		return AttachReportReceiptResult{}, errors.New("expense: a line number and receipt name are required")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-existingReportHeadroom-8 {
		return AttachReportReceiptResult{}, errors.New("expense: client sequence lacks headroom for attaching a receipt")
	}
	draft, _, err := client.openDraftReport(ctx, request.ReportNumber)
	if err != nil {
		return AttachReportReceiptResult{}, err
	}
	lines, err := reportLines(draft.responseBody, draft.detailsRootID)
	if err != nil {
		return AttachReportReceiptResult{}, err
	}
	if request.Line > len(lines) {
		return AttachReportReceiptResult{}, fmt.Errorf("expense: report %s has %d lines", request.ReportNumber, len(lines))
	}
	line := lines[request.Line-1]
	root := draft.detailsRootID
	send := func(message dynamics.Message, targets dynamics.ReceiptMatchCommandTargets) ([]byte, error) {
		return client.sendValidated(ctx, []dynamics.Message{message}, func(envelope dynamics.Envelope) error {
			return dynamics.ValidateReceiptMatchCommands(envelope, targets)
		})
	}

	openModel, err := dynamics.DiscoverResponseModel(draft.responseBody)
	if err != nil {
		return AttachReportReceiptResult{}, err
	}
	tab, ok := openModel.FindControlInRoot(dynamics.ControlReceiptsTabPage, root)
	if !ok || tab.ID == "" {
		return AttachReportReceiptResult{}, errors.New("expense: Receipts tab not found in the report")
	}
	activate, err := buildActivateReceiptsTabMessage(client.nextClientSequence, root, tab.ID)
	if err != nil {
		return AttachReportReceiptResult{}, err
	}
	tabBody, err := client.sendValidated(ctx, []dynamics.Message{activate}, func(envelope dynamics.Envelope) error {
		return validateActivateReceiptsTabEnvelope(envelope, root, tab.ID)
	})
	if err != nil {
		return AttachReportReceiptResult{}, fmt.Errorf("expense: activate Receipts tab: %w", err)
	}
	tabModel, err := dynamics.DiscoverResponseModel(tabBody)
	if err != nil {
		return AttachReportReceiptResult{}, err
	}
	grid, gridOK := tabModel.FindControlInRoot(dynamics.ControlReceiptsGrid, root)
	match, matchOK := tabModel.FindControlInRoot(dynamics.ControlMatchExpensesButton, root)
	if !gridOK || !matchOK || grid.ID == "" || match.ID == "" {
		return AttachReportReceiptResult{}, errors.New("expense: receipts grid or Attach to expense control not found")
	}
	// The open response lists the receipts; the tab response adds names.
	receipts := make(map[string]map[string]string)
	for _, body := range [][]byte{draft.responseBody, tabBody} {
		rows, err := recordsByTracking(body, dynamics.DataSourceReceipts, root)
		if err != nil {
			return AttachReportReceiptResult{}, err
		}
		for tracking, fields := range rows {
			if receipts[tracking] == nil {
				receipts[tracking] = make(map[string]string)
			}
			for name, value := range fields {
				receipts[tracking][name] = value
			}
		}
	}
	receiptTracking := ""
	for tracking, fields := range receipts {
		if fields["FileName_field"] != request.Receipt {
			continue
		}
		if receiptTracking != "" {
			return AttachReportReceiptResult{}, fmt.Errorf("expense: more than one receipt is named %q", request.Receipt)
		}
		receiptTracking = tracking
	}
	if receiptTracking == "" {
		return AttachReportReceiptResult{}, fmt.Errorf("expense: the report has no receipt named %q", request.Receipt)
	}
	activeReceipt, err := activeTracking(dynamics.DataSourceReceipts, root, tabBody, draft.responseBody)
	if err != nil {
		return AttachReportReceiptResult{}, err
	}

	defer func() {
		if err != nil {
			err = markOperationUncertain(err)
		}
	}()
	if activeReceipt != receiptTracking {
		selectReceipt, err := dynamics.BuildChangeSelectedIndexMessage(client.nextClientSequence, root, grid.ID, receiptTracking, dynamics.MarkModeActivate, activeReceipt)
		if err != nil {
			return AttachReportReceiptResult{}, fmt.Errorf("expense: %w", err)
		}
		body, err := send(selectReceipt, dynamics.ReceiptMatchCommandTargets{DetailsRootID: root, ReceiptsGridID: grid.ID})
		if err != nil {
			return AttachReportReceiptResult{}, fmt.Errorf("expense: select receipt: %w", err)
		}
		if now, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceReceipts, root); err != nil || now != receiptTracking {
			return AttachReportReceiptResult{}, fmt.Errorf("expense: receipt %q did not become the selected receipt", request.Receipt)
		}
	}

	body, err := send(dynamics.BuildOpenReceiptMatchMessage(client.nextClientSequence, root, match.ID), dynamics.ReceiptMatchCommandTargets{
		DetailsRootID: root,
		MatchButtonID: match.ID,
	})
	if err != nil {
		return AttachReportReceiptResult{}, fmt.Errorf("expense: open Attach to expense: %w", err)
	}
	popupModel, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		return AttachReportReceiptResult{}, err
	}
	popup, ok := popupModel.FindUniqueForm(dynamics.FormReceiptMatch)
	if !ok || popup.ID == "" {
		return AttachReportReceiptResult{}, errors.New("expense: Attach to expense did not open its popup")
	}
	popupGrid, gridOK := popupModel.FindControlInRoot(dynamics.ControlLinesGrid, popup.ID)
	attach, attachOK := popupModel.FindControlInRoot(dynamics.ControlAttachButton, popup.ID)
	if !gridOK || !attachOK || popupGrid.ID == "" || attach.ID == "" {
		return AttachReportReceiptResult{}, errors.New("expense: Attach to expense popup controls not found")
	}
	offered, ok, err := dynamics.DiscoverFullList(body, dynamics.DataSourceExpenseLines, popup.ID)
	if err != nil || !ok {
		return AttachReportReceiptResult{}, errors.New("expense: Attach to expense popup did not list its lines")
	}
	rows, err := recordsByTracking(body, dynamics.DataSourceExpenseLines, popup.ID)
	if err != nil {
		return AttachReportReceiptResult{}, err
	}
	lineTracking := ""
	for _, tracking := range offered {
		if !popupRowMatches(rows[tracking], line) {
			continue
		}
		if lineTracking != "" {
			return AttachReportReceiptResult{}, fmt.Errorf("expense: more than one offered line matches line %d", request.Line)
		}
		lineTracking = tracking
	}
	if lineTracking == "" {
		return AttachReportReceiptResult{}, fmt.Errorf("expense: line %d is not offered for this receipt; it may already have one", request.Line)
	}
	popupTargets := dynamics.ReceiptMatchCommandTargets{PopupRootID: popup.ID, PopupGridID: popupGrid.ID, AttachButtonID: attach.ID}
	activeLine, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceExpenseLines, popup.ID)
	if err != nil {
		return AttachReportReceiptResult{}, err
	}
	if activeLine != lineTracking {
		if activeLine == "" {
			activeLine = offered[0]
		}
		selectLine, err := dynamics.BuildChangeSelectedIndexMessage(client.nextClientSequence, popup.ID, popupGrid.ID, lineTracking, dynamics.MarkModeActivate, activeLine)
		if err != nil {
			return AttachReportReceiptResult{}, fmt.Errorf("expense: %w", err)
		}
		lineBody, err := send(selectLine, dynamics.ReceiptMatchCommandTargets{PopupRootID: popup.ID, PopupGridID: popupGrid.ID})
		if err != nil {
			return AttachReportReceiptResult{}, fmt.Errorf("expense: select line %d in the popup: %w", request.Line, err)
		}
		if now, err := dynamics.DiscoverActiveTrackingID(lineBody, dynamics.DataSourceExpenseLines, popup.ID); err != nil || now != lineTracking {
			return AttachReportReceiptResult{}, fmt.Errorf("expense: line %d did not become the popup's active line", request.Line)
		}
	}

	body, err = send(dynamics.BuildAttachReceiptMessage(client.nextClientSequence, popup.ID, attach.ID), popupTargets)
	if err != nil {
		return AttachReportReceiptResult{}, fmt.Errorf("expense: attach receipt: %w", err)
	}
	matches, err := dynamics.DiscoverDataRecords(body, dynamics.DataSourceReceiptMatches)
	if err != nil || len(matches) == 0 {
		return AttachReportReceiptResult{}, errors.New("expense: Dynamics did not record the receipt match")
	}
	remaining, ok, err := dynamics.DiscoverFullList(body, dynamics.DataSourceExpenseLines, popup.ID)
	if err != nil || !ok || len(remaining) != len(offered)-1 {
		return AttachReportReceiptResult{}, errors.New("expense: the attached line was not removed from the offered lines")
	}
	remainingRows, err := recordsByTracking(body, dynamics.DataSourceExpenseLines, popup.ID)
	if err != nil {
		return AttachReportReceiptResult{}, err
	}
	for _, tracking := range remaining {
		if popupRowMatches(remainingRows[tracking], line) {
			return AttachReportReceiptResult{}, fmt.Errorf("expense: line %d is still offered after attaching", request.Line)
		}
	}

	body, err = send(dynamics.BuildClosePopupMessage(client.nextClientSequence, popup.ID), dynamics.ReceiptMatchCommandTargets{PopupRootID: popup.ID})
	if err != nil {
		return AttachReportReceiptResult{}, fmt.Errorf("expense: close Attach to expense: %w", err)
	}
	if closed, err := dynamics.ResponseDeletesViewModel(body, popup.ID); err != nil || !closed {
		return AttachReportReceiptResult{}, errors.New("expense: Attach to expense popup did not close")
	}
	if err := client.saveAndCloseOpenDraft(ctx, draft); err != nil {
		return AttachReportReceiptResult{}, err
	}
	return AttachReportReceiptResult{Receipt: request.Receipt, Date: line.Date, Category: line.Category, Amount: line.Amount}, nil
}

// popupRowMatches compares an offered line with a report line by date, amount,
// and merchant when both list one.
func popupRowMatches(row map[string]string, line reportLine) bool {
	if row == nil || row["TransDate_field"] != line.Date {
		return false
	}
	amount, _, _ := strings.Cut(strings.TrimSpace(line.Amount), " ")
	if !decimalEqual(strings.ReplaceAll(amount, ",", ""), row["AmountCurr_field"]) {
		return false
	}
	merchant := strings.TrimSpace(row["MerchantId_field"])
	return merchant == "" || line.merchantID == "" || dynamics.TrimmedEqualFold(merchant, line.merchantID)
}

// activeTracking returns the first active row reported for a data source by
// the given responses, newest first.
func activeTracking(dataSource, rootID string, bodies ...[]byte) (string, error) {
	for _, body := range bodies {
		active, err := dynamics.DiscoverActiveTrackingID(body, dataSource, rootID)
		if err != nil {
			return "", err
		}
		if active != "" {
			return active, nil
		}
	}
	return "", fmt.Errorf("expense: no active %s row", dataSource)
}
