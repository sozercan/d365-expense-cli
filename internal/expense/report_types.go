package expense

import "errors"

var errInvalidReportFinalAction = errors.New("expense: report final action is invalid")

// ReportFinalAction selects the final action after report creation and any
// receipt attachments complete.
type ReportFinalAction string

const (
	ReportFinalActionSubmit    ReportFinalAction = "submit"
	ReportFinalActionSaveDraft ReportFinalAction = "save_draft"
)

// CreateReportRequest describes a report creation operation with an explicit
// final action.
type CreateReportRequest struct {
	Purpose     string
	FinalAction ReportFinalAction
}

// CreateReportPlan is a credential-free description of a report creation
// operation.
type CreateReportPlan struct {
	Purpose      string
	RequestCount int
	Actions      []string
}

// ReportResult contains only non-secret outcome data from creating an expense
// report.
type ReportResult struct {
	Purpose        string
	ReportNumber   string
	Status         string
	SavedAndClosed bool
	Submitted      bool
}

func validateReportFinalAction(action ReportFinalAction) error {
	if action != ReportFinalActionSubmit && action != ReportFinalActionSaveDraft {
		return errInvalidReportFinalAction
	}
	return nil
}
