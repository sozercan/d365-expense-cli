package expense

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

const (
	// openReportRequestCount covers Refresh, row activation, and open.
	openReportRequestCount = 3
	existingReportHeadroom = int64(openReportRequestCount + 4)
)

// ErrReportLinkUnavailable means the session predates opening existing
// reports and must be re-imported.
var ErrReportLinkUnavailable = errors.New("expense: session does not identify the workspace report link; re-import the session")

// ReportLine is one expense line of an existing report, numbered in report
// order starting at 1.
type ReportLine struct {
	Number   int
	Date     string
	Category string
	Amount   string
	Merchant string
	Notes    string
	Receipts bool
}

// ReportReceipt is one receipt on an existing report's Receipts tab, listed
// by its file name. Attached means it is attached to an expense line.
type ReportReceipt struct {
	Name     string
	Attached bool
}

// ReportDetails contains only non-secret data from an existing Draft.
type ReportDetails struct {
	ReportNumber string
	Purpose      string
	Status       string
	Lines        []ReportLine
	Receipts     []ReportReceipt
}

// ItemizeReportLineRequest itemizes one existing line of a Draft. Line is the
// 1-based number shown by ShowDraftReport.
type ItemizeReportLineRequest struct {
	ReportNumber string
	Line         int
	Itemization  []ItemizationInput
}

// ShowDraftReport opens an existing Draft, lists its lines and receipts, and
// saves and closes it unchanged.
func (client *Client) ShowDraftReport(ctx context.Context, reportNumber string) (details ReportDetails, err error) {
	if err := client.validateExistingReport(ctx, reportNumber); err != nil {
		return ReportDetails{}, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-existingReportHeadroom {
		return ReportDetails{}, errors.New("expense: client sequence lacks headroom for opening a report")
	}
	draft, purpose, err := client.openDraftReport(ctx, reportNumber)
	if err != nil {
		return ReportDetails{}, err
	}
	lines, err := reportLines(draft.responseBody, draft.detailsRootID)
	if err != nil {
		return ReportDetails{}, err
	}
	tab, err := client.readReceiptsTab(ctx, draft)
	if err != nil {
		return ReportDetails{}, err
	}
	if err := client.saveAndCloseOpenDraft(ctx, draft); err != nil {
		return ReportDetails{}, err
	}
	details = ReportDetails{ReportNumber: reportNumber, Purpose: purpose, Status: draft.status}
	for _, line := range lines {
		details.Lines = append(details.Lines, line.ReportLine)
	}
	for _, id := range tab.list.order {
		row := tab.list.rows[id]
		if row.name == "" {
			return ReportDetails{}, errors.New("expense: Dynamics did not name every receipt")
		}
		details.Receipts = append(details.Receipts, ReportReceipt{Name: row.name, Attached: row.attached})
	}
	return details, nil
}

// ItemizeDraftReportLine itemizes one existing line of a Draft and saves and
// closes the report. The rows must add up exactly to the line amount.
func (client *Client) ItemizeDraftReportLine(ctx context.Context, request ItemizeReportLineRequest) (result ExpenseLineResult, err error) {
	if err := client.validateExistingReport(ctx, request.ReportNumber); err != nil {
		return ExpenseLineResult{}, err
	}
	if request.Line < 1 {
		return ExpenseLineResult{}, errors.New("expense: line number must be at least 1")
	}
	if len(request.Itemization) == 0 {
		return ExpenseLineResult{}, errors.New("expense: at least one itemization row is required")
	}
	var itemized int64
	for _, row := range request.Itemization {
		rate, err := parseExpenseCents("daily rate", row.DailyRate)
		if err != nil {
			return ExpenseLineResult{}, fmt.Errorf("expense: %w", err)
		}
		itemized += rate * int64(row.Days)
	}
	// The real line date and category are checked after the report opens;
	// placeholders let the shared validator check the rows offline.
	probe := ExpenseLineInput{Category: "line", Date: "2000-01-01", Amount: formatCents(itemized), Itemization: request.Itemization}
	if err := validateExpenseLine(probe); err != nil {
		return ExpenseLineResult{}, fmt.Errorf("expense: %w", err)
	}
	headroom := existingReportHeadroom + expenseLinesSequenceHeadroom([]ExpenseLineInput{probe})

	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-headroom {
		return ExpenseLineResult{}, errors.New("expense: client sequence lacks headroom for itemizing a line")
	}
	draft, _, err := client.openDraftReport(ctx, request.ReportNumber)
	if err != nil {
		return ExpenseLineResult{}, err
	}
	lines, err := reportLines(draft.responseBody, draft.detailsRootID)
	if err != nil {
		return ExpenseLineResult{}, err
	}
	if request.Line > len(lines) {
		return ExpenseLineResult{}, fmt.Errorf("expense: report %s has %d lines", request.ReportNumber, len(lines))
	}
	target := lines[request.Line-1]
	// Itemize verifies the dialog total against this amount, so rows that
	// do not add up to the selected line's amount stop before any change.
	line := ExpenseLineInput{Category: target.Category, Date: target.Date, Amount: formatCents(itemized), Itemization: request.Itemization}

	model, err := dynamics.DiscoverResponseModel(draft.responseBody)
	if err != nil {
		return ExpenseLineResult{}, err
	}
	grid, ok := model.FindControlInRoot(dynamics.ControlLinesGrid, draft.detailsRootID)
	if !ok || grid.ID == "" {
		return ExpenseLineResult{}, errors.New("expense: expense lines grid not found in the report")
	}
	session := &expenseLineSession{client: client, detailsRootID: draft.detailsRootID}
	if node, ok := model.FindControlInRoot(dynamics.ControlExpenseCardMenu, draft.detailsRootID); ok {
		session.cardMenu = node
	}
	if err := session.observeLines(draft.responseBody); err != nil {
		return ExpenseLineResult{}, err
	}

	defer func() {
		if err != nil {
			err = markOperationUncertain(err)
		}
	}()
	active, err := dynamics.DiscoverActiveTrackingID(draft.responseBody, dynamics.DataSourceExpenseLines, draft.detailsRootID)
	if err != nil {
		return ExpenseLineResult{}, err
	}
	if active == "" {
		return ExpenseLineResult{}, errors.New("expense: the report has no active expense line")
	}
	if active != target.trackingID {
		selectLine, err := dynamics.BuildChangeSelectedIndexMessage(client.nextClientSequence, draft.detailsRootID, grid.ID, target.trackingID, dynamics.MarkModeActivate, active)
		if err != nil {
			return ExpenseLineResult{}, fmt.Errorf("expense: %w", err)
		}
		body, err := session.send(ctx, []dynamics.Message{selectLine}, dynamics.ExpenseLineCommandTargets{
			DetailsRootID: draft.detailsRootID,
			LinesGridID:   grid.ID,
		})
		if err != nil {
			return ExpenseLineResult{}, fmt.Errorf("expense: select line %d: %w", request.Line, err)
		}
		if now, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceExpenseLines, draft.detailsRootID); err != nil || now != target.trackingID {
			return ExpenseLineResult{}, fmt.Errorf("expense: line %d did not become the active line", request.Line)
		}
	}
	result = ExpenseLineResult{Category: target.Category, Date: target.Date, Amount: target.Amount}
	if result.ItemizedLines, err = session.itemize(ctx, line); err != nil {
		return ExpenseLineResult{}, fmt.Errorf("expense: itemize line %d: %w", request.Line, err)
	}
	if err := client.saveAndCloseOpenDraft(ctx, draft); err != nil {
		return ExpenseLineResult{}, err
	}
	return result, nil
}

// SubmitDraftReport opens an existing Draft, clicks only its validated
// SubmitButton, and verifies through a Refresh that it left Draft status.
func (client *Client) SubmitDraftReport(ctx context.Context, reportNumber string) (result ReportResult, err error) {
	if err := client.validateExistingReport(ctx, reportNumber); err != nil {
		return ReportResult{}, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-existingReportHeadroom {
		return ReportResult{}, errors.New("expense: client sequence lacks headroom for submitting a report")
	}
	draft, purpose, err := client.openDraftReport(ctx, reportNumber)
	if err != nil {
		return ReportResult{}, err
	}
	if err := dynamics.ValidateSubmitButton(draft.submitButton, draft.detailsRootID); err != nil {
		return ReportResult{}, fmt.Errorf("expense: submit control is unavailable or unsupported: %w", err)
	}
	defer func() {
		if err != nil {
			err = markOperationUncertain(err)
		}
	}()
	submit := dynamics.BuildSubmitClickMessage(client.nextClientSequence, draft.detailsRootID, draft.submitButton.ID)
	body, err := client.sendValidated(ctx, []dynamics.Message{submit}, func(envelope dynamics.Envelope) error {
		return dynamics.ValidateSubmitCommands(envelope, dynamics.SubmitCommandTargets{
			DetailsRootID:  draft.detailsRootID,
			SubmitButtonID: draft.submitButton.ID,
		})
	})
	if err != nil {
		return ReportResult{}, fmt.Errorf("expense: submit report: %w", err)
	}
	if err := client.restoreWorkspace(body, "submit response"); err != nil {
		return ReportResult{}, err
	}
	rows, _, err := client.refreshReportRows(ctx, dynamics.MarkModeActivate)
	if err != nil {
		return ReportResult{}, err
	}
	for _, row := range rows {
		if row["ExpNumber_field"] != reportNumber {
			continue
		}
		status := row["ApprovalStatus_field"]
		if status == "" || status == draftApprovalStatus {
			return ReportResult{}, fmt.Errorf("expense: report %s is still a Draft after Submit", reportNumber)
		}
		return ReportResult{Purpose: purpose, ReportNumber: reportNumber, Status: status, Submitted: true}, nil
	}
	return ReportResult{}, fmt.Errorf("expense: report %s is no longer listed after Submit", reportNumber)
}

// CheckExistingReport validates offline that reportNumber is well formed and
// the session can open existing reports.
func (client *Client) CheckExistingReport(reportNumber string) error {
	if err := client.validateDeleteRequest(DeleteReportRequest{ReportNumber: reportNumber}); err != nil {
		return err
	}
	if client.reportLinkID == "" {
		return ErrReportLinkUnavailable
	}
	return nil
}

func (client *Client) validateExistingReport(ctx context.Context, reportNumber string) error {
	if ctx == nil {
		return errors.New("expense: context is nil")
	}
	return client.CheckExistingReport(reportNumber)
}

// refreshReportRows refreshes the workspace reports grid and returns its rows
// and the refresh response. The caller must hold client.mu.
func (client *Client) refreshReportRows(ctx context.Context, markMode int) (map[string]map[string]string, []byte, error) {
	targets := dynamics.ReportCommandTargets{WorkspaceRootID: client.workspaceRootID, ReportsGridID: client.reportsGridID, MarkMode: markMode}
	body, err := client.sendValidated(ctx, []dynamics.Message{dynamics.BuildRefreshMessage(client.nextClientSequence, client.workspaceRootID)}, func(envelope dynamics.Envelope) error {
		return dynamics.ValidateReportCommands(envelope, targets)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("expense: refresh reports: %w", err)
	}
	rows, err := client.reportRows(body)
	if err != nil {
		return nil, nil, err
	}
	return rows, body, nil
}

// openDraftReport activates and opens one listed Draft and returns its open
// details form and purpose. The caller must hold client.mu.
func (client *Client) openDraftReport(ctx context.Context, reportNumber string) (openDraftDetails, string, error) {
	targets := dynamics.ReportCommandTargets{
		WorkspaceRootID: client.workspaceRootID,
		ReportsGridID:   client.reportsGridID,
		MarkMode:        dynamics.MarkModeActivate,
		ReportLinkID:    client.reportLinkID,
	}
	send := func(message dynamics.Message) ([]byte, error) {
		return client.sendValidated(ctx, []dynamics.Message{message}, func(envelope dynamics.Envelope) error {
			return dynamics.ValidateReportCommands(envelope, targets)
		})
	}
	rows, body, err := client.refreshReportRows(ctx, dynamics.MarkModeActivate)
	if err != nil {
		return openDraftDetails{}, "", err
	}
	trackingID, row, err := findDraftRow(rows, reportNumber)
	if err != nil {
		return openDraftDetails{}, "", err
	}
	active, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceReports, client.workspaceRootID)
	if err != nil {
		return openDraftDetails{}, "", err
	}
	if active == "" {
		return openDraftDetails{}, "", errors.New("expense: refreshed reports grid has no active row")
	}
	if active != trackingID {
		selectRow, err := dynamics.BuildChangeSelectedIndexMessage(client.nextClientSequence, client.workspaceRootID, client.reportsGridID, trackingID, dynamics.MarkModeActivate, active)
		if err != nil {
			return openDraftDetails{}, "", fmt.Errorf("expense: %w", err)
		}
		body, err := send(selectRow)
		if err != nil {
			return openDraftDetails{}, "", fmt.Errorf("expense: select report: %w", err)
		}
		if now, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceReports, client.workspaceRootID); err != nil || now != trackingID {
			return openDraftDetails{}, "", errors.New("expense: the report did not become the active row")
		}
	}

	body, err = send(dynamics.BuildOpenReportMessage(client.nextClientSequence, client.workspaceRootID, client.reportLinkID))
	if err != nil {
		return openDraftDetails{}, "", fmt.Errorf("expense: open report: %w", err)
	}
	model, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		return openDraftDetails{}, "", err
	}
	details, ok := model.FindUniqueForm(dynamics.FormExpenseReportDetails)
	if !ok || details.ID == "" {
		return openDraftDetails{}, "", errors.New("expense: opening the report did not show its details form")
	}
	if model.ReportNumber != reportNumber {
		return openDraftDetails{}, "", fmt.Errorf("expense: opened report %q, not %s", model.ReportNumber, reportNumber)
	}
	if model.Status != "Draft" {
		return openDraftDetails{}, "", fmt.Errorf("expense: opened report status is %q, not Draft", model.Status)
	}
	saveAndClose, ok := model.FindControlInRoot(dynamics.ControlSaveAndClose, details.ID)
	if !ok || saveAndClose.ID == "" {
		return openDraftDetails{}, "", errors.New("expense: SaveAndClose control not found in the opened report")
	}
	submitButton, _ := model.FindControlInRoot(dynamics.ControlSubmitButton, details.ID)
	return openDraftDetails{
		responseBody:   body,
		reportNumber:   reportNumber,
		status:         model.Status,
		detailsRootID:  details.ID,
		saveAndCloseID: saveAndClose.ID,
		submitButton:   submitButton,
		existing:       true,
	}, row["Txt2_field"], nil
}

func (client *Client) saveAndCloseOpenDraft(ctx context.Context, draft openDraftDetails) error {
	save := dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, draft.detailsRootID, draft.saveAndCloseID)
	body, err := client.send(ctx, []dynamics.Message{save}, dynamics.DraftCommandTargets{
		DetailsRootID:  draft.detailsRootID,
		SaveAndCloseID: draft.saveAndCloseID,
	})
	if err != nil {
		return markOperationUncertain(fmt.Errorf("expense: save and close report: %w", err))
	}
	if err := client.restoreWorkspace(body, "save and close response"); err != nil {
		return markOperationUncertain(err)
	}
	return nil
}

type reportLine struct {
	ReportLine
	trackingID string
	merchantID string
}

// reportLines lists the opened report's expense lines in display order.
func reportLines(body []byte, detailsRootID string) ([]reportLine, error) {
	ids, ok, err := dynamics.DiscoverFullList(body, dynamics.DataSourceExpenseLines, detailsRootID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("expense: the opened report did not list all of its lines")
	}
	rows, err := recordsByTracking(body, dynamics.DataSourceExpenseLines, detailsRootID)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, errors.New("expense: the opened report has no expense lines")
	}
	lines := make([]reportLine, 0, len(ids))
	for index, id := range ids {
		fields := rows[id]
		if fields["categoryOrSubCategoryName_dataMethod"] == "" || fields["TransDate_field"] == "" {
			return nil, fmt.Errorf("expense: line %d has no category or date", index+1)
		}
		merchant := fields["MerchantId_field"]
		if merchant == "" {
			merchant = fields["cardMerchantId_dataMethod"]
		}
		lines = append(lines, reportLine{
			ReportLine: ReportLine{
				Number:   index + 1,
				Date:     fields["TransDate_field"],
				Category: fields["categoryOrSubCategoryName_dataMethod"],
				Amount:   fields["amountWithCurrencyCode_dataMethod"],
				Merchant: merchant,
				Notes:    fields["AdditionalInformation_field"],
				Receipts: fields["ReceiptsAttached_field"] != "" && fields["ReceiptsAttached_field"] != "0",
			},
			trackingID: id,
			merchantID: fields["MerchantId_field"],
		})
	}
	return lines, nil
}
