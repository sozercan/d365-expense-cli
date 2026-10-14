package expense

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

const (
	deleteReportRequestCount = 5
	// draftApprovalStatus is the TrvExpTable ApprovalStatus code for Draft.
	draftApprovalStatus = "1"
)

var reportNumberPattern = regexp.MustCompile(`^[A-Za-z0-9_/-]{5,64}$`)

// ErrReportsGridUnavailable means the session predates report selection and
// must be re-imported.
var ErrReportsGridUnavailable = errors.New("expense: session does not identify the workspace reports grid; re-import the session")

// DeleteReportRequest identifies one existing Draft report to delete.
type DeleteReportRequest struct {
	ReportNumber string
}

// DeleteReportPlan is a credential-free description of a report deletion.
type DeleteReportPlan struct {
	ReportNumber string
	RequestCount int
	Actions      []string
}

// DeleteReportResult contains only non-secret outcome data.
type DeleteReportResult struct {
	ReportNumber string
	Purpose      string
	Deleted      bool
}

// PlanDeleteDraftReport returns an offline, credential-free deletion plan.
func (client *Client) PlanDeleteDraftReport(request DeleteReportRequest) (DeleteReportPlan, error) {
	if err := client.validateDeleteRequest(request); err != nil {
		return DeleteReportPlan{}, err
	}
	return DeleteReportPlan{
		ReportNumber: request.ReportNumber,
		RequestCount: deleteReportRequestCount,
		Actions: []string{
			"refresh the workspace reports grid",
			"require exactly one listed report with this number, in Draft status, and no marked rows",
			"mark only that report and verify it is the only marked row",
			"click Delete and confirm Yes in the Dynamics confirmation dialog",
			"refresh the reports grid and verify the report is gone",
		},
	}, nil
}

// DeleteDraftReport deletes one Draft report listed in the workspace reports
// grid. It refuses any report that is not in Draft status.
func (client *Client) DeleteDraftReport(ctx context.Context, request DeleteReportRequest) (result DeleteReportResult, err error) {
	if ctx == nil {
		return DeleteReportResult{}, errors.New("expense: context is nil")
	}
	if err := client.validateDeleteRequest(request); err != nil {
		return DeleteReportResult{}, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-deleteReportRequestCount {
		return DeleteReportResult{}, errors.New("expense: client sequence lacks headroom for report deletion")
	}
	targets := dynamics.ReportCommandTargets{WorkspaceRootID: client.workspaceRootID, ReportsGridID: client.reportsGridID, MarkMode: dynamics.MarkModeMark}
	send := func(message dynamics.Message, targets dynamics.ReportCommandTargets) ([]byte, error) {
		return client.sendValidated(ctx, []dynamics.Message{message}, func(envelope dynamics.Envelope) error {
			return dynamics.ValidateReportCommands(envelope, targets)
		})
	}

	body, err := send(dynamics.BuildRefreshMessage(client.nextClientSequence, client.workspaceRootID), targets)
	if err != nil {
		return DeleteReportResult{}, fmt.Errorf("expense: refresh reports: %w", err)
	}
	rows, err := client.reportRows(body)
	if err != nil {
		return DeleteReportResult{}, err
	}
	trackingID, target, err := findDraftRow(rows, request.ReportNumber)
	if err != nil {
		return DeleteReportResult{}, err
	}
	pivot, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceReports, client.workspaceRootID)
	if err != nil {
		return DeleteReportResult{}, err
	}
	if pivot == "" {
		return DeleteReportResult{}, errors.New("expense: refreshed reports grid has no active row")
	}
	model, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		return DeleteReportResult{}, err
	}
	deleteButton, ok := model.FindControlInRoot(dynamics.ControlDeleteReportButton, client.workspaceRootID)
	if !ok || deleteButton.ID == "" {
		return DeleteReportResult{}, errors.New("expense: reports Delete button not found in the workspace")
	}

	selectRow, err := dynamics.BuildChangeSelectedIndexMessage(client.nextClientSequence, client.workspaceRootID, client.reportsGridID, trackingID, dynamics.MarkModeMark, pivot)
	if err != nil {
		return DeleteReportResult{}, fmt.Errorf("expense: %w", err)
	}
	body, err = send(selectRow, targets)
	if err != nil {
		return DeleteReportResult{}, fmt.Errorf("expense: select report: %w", err)
	}
	if err := mergeReportRows(rows, body, client.workspaceRootID); err != nil {
		return DeleteReportResult{}, err
	}
	active, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceReports, client.workspaceRootID)
	if err != nil {
		return DeleteReportResult{}, err
	}
	if active == "" {
		// Dynamics omits the active row when selection does not change it.
		active = pivot
	}
	for tracking, row := range rows {
		if (row["_RESERVED_IsMarked"] == "true") != (tracking == trackingID) {
			return DeleteReportResult{}, errors.New("expense: selecting the report did not mark exactly that report")
		}
	}
	if active != trackingID {
		return DeleteReportResult{}, errors.New("expense: selecting the report did not make it the active row")
	}

	targets.DeleteButtonID = deleteButton.ID
	body, err = send(dynamics.BuildDeleteReportClickMessage(client.nextClientSequence, client.workspaceRootID, deleteButton.ID), targets)
	if err != nil {
		return DeleteReportResult{}, fmt.Errorf("expense: click Delete: %w", err)
	}
	model, err = dynamics.DiscoverResponseModel(body)
	if err != nil {
		return DeleteReportResult{}, err
	}
	confirm, ok := model.FindUniqueForm(dynamics.FormConfirmation)
	if !ok || confirm.ID == "" {
		return DeleteReportResult{}, errors.New("expense: Delete did not open the confirmation dialog")
	}
	yes, yesOK := model.FindControlInRoot(dynamics.ControlConfirmYes, confirm.ID)
	no, noOK := model.FindControlInRoot(dynamics.ControlConfirmNo, confirm.ID)
	if !yesOK || !noOK || yes.ID == "" || no.ID == "" {
		return DeleteReportResult{}, errors.New("expense: confirmation dialog is not the captured Yes/No dialog")
	}

	defer func() {
		if err != nil {
			err = markOperationUncertain(err)
		}
	}()
	body, err = send(dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, confirm.ID, yes.ID), dynamics.ReportCommandTargets{
		ConfirmRootID: confirm.ID,
		ConfirmYesID:  yes.ID,
	})
	if err != nil {
		return DeleteReportResult{}, fmt.Errorf("expense: confirm Delete: %w", err)
	}
	if closed, err := dynamics.ResponseDeletesViewModel(body, confirm.ID); err != nil || !closed {
		return DeleteReportResult{}, errors.New("expense: confirmation dialog did not close")
	}

	body, err = send(dynamics.BuildRefreshMessage(client.nextClientSequence, client.workspaceRootID), targets)
	if err != nil {
		return DeleteReportResult{}, fmt.Errorf("expense: refresh reports after Delete: %w", err)
	}
	after, err := client.reportRows(body)
	if err != nil {
		return DeleteReportResult{}, err
	}
	for _, row := range after {
		if row["ExpNumber_field"] == request.ReportNumber {
			return DeleteReportResult{}, fmt.Errorf("expense: report %s is still listed after Delete", request.ReportNumber)
		}
	}
	return DeleteReportResult{ReportNumber: request.ReportNumber, Purpose: target["Txt2_field"], Deleted: true}, nil
}

func (client *Client) validateDeleteRequest(request DeleteReportRequest) error {
	if client == nil {
		return errors.New("expense: client is nil")
	}
	if !reportNumberPattern.MatchString(request.ReportNumber) {
		return errors.New("expense: report number is invalid")
	}
	if client.reportsGridID == "" {
		return ErrReportsGridUnavailable
	}
	return nil
}

// reportRows returns the refreshed reports grid rows keyed by MasterTrackingId.
func (client *Client) reportRows(body []byte) (map[string]map[string]string, error) {
	rows := make(map[string]map[string]string)
	if err := mergeReportRows(rows, body, client.workspaceRootID); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("expense: refresh did not return any reports")
	}
	return rows, nil
}

func mergeReportRows(rows map[string]map[string]string, body []byte, rootID string) error {
	records, err := dynamics.DiscoverDataRecords(body, dynamics.DataSourceReports)
	if err != nil {
		return err
	}
	for _, record := range records {
		tracking := record.Fields["MasterTrackingId"]
		if record.RootID != rootID || tracking == "" {
			continue
		}
		if rows[tracking] == nil {
			rows[tracking] = make(map[string]string)
		}
		for name, value := range record.Fields {
			rows[tracking][name] = value
		}
	}
	return nil
}

func findDraftRow(rows map[string]map[string]string, reportNumber string) (string, map[string]string, error) {
	var trackingID string
	var target map[string]string
	for tracking, row := range rows {
		if row["_RESERVED_IsMarked"] == "true" {
			return "", nil, errors.New("expense: the reports grid already has marked rows; clear the selection in Dynamics and re-import")
		}
		if row["ExpNumber_field"] != reportNumber {
			continue
		}
		if target != nil {
			return "", nil, fmt.Errorf("expense: report %s is listed more than once", reportNumber)
		}
		trackingID, target = tracking, row
	}
	if target == nil {
		return "", nil, fmt.Errorf("expense: report %s is not among the %d most recent reports", reportNumber, len(rows))
	}
	if target["ApprovalStatus_field"] != draftApprovalStatus {
		return "", nil, fmt.Errorf("expense: report %s is not a Draft", reportNumber)
	}
	return trackingID, target, nil
}
