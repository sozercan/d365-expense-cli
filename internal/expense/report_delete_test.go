package expense_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/dynamics"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

const deleteWorkspace = "captured-workspace-root"

func deleteClient(t *testing.T, baseURL string, withGrid bool, options ...expense.Option) *expense.Client {
	t.Helper()
	profile := &capture.BootstrapProfile{
		Session:   validProfile(baseURL).Session,
		NewReport: capture.CommandTarget{CommandName: "Click", RootID: deleteWorkspace, TargetID: "captured-new-button", ControlName: dynamics.SelectedControlNewExpenseReportReportsTab},
	}
	if withGrid {
		profile.ReportsGrid = capture.ReportsGridTarget(deleteWorkspace, "grid")
	}
	client, err := expense.NewFromBootstrap(profile, options...)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func reportRow(tracking, number, status string) map[string]any {
	return map[string]any{
		"dataSourceName_internal": dynamics.DataSourceReports, "MasterTrackingId": tracking, "_RESERVED_IsMarked": "false",
		"ExpNumber_field": number, "ApprovalStatus_field": status, "Txt2_field": "Purpose " + number,
	}
}

func activeRow(tracking string) json.RawMessage {
	return mustRaw(map[string]any{"$type": "UpdateModelCollectionInteraction", "RootId": deleteWorkspace, "Descriptor": map[string]any{
		"Name": dynamics.DataSourceReports, "ActiveMasterTrackingId": tracking,
	}})
}

func refreshStep(rows ...map[string]any) scriptedStep {
	return func(dynamics.Envelope) []json.RawMessage {
		return []json.RawMessage{
			controlUpdate(deleteWorkspace, control("delete", dynamics.ControlDeleteReportButton)),
			recordInsert(deleteWorkspace, dynamics.DataSourceReports, rows...),
			activeRow("13"),
		}
	}
}

func deleteSteps(markOthers bool) []scriptedStep {
	rows := []map[string]any{reportRow("13", "ER-0001", "1"), reportRow("15", "ER-0002", "1"), reportRow("17", "ER-0003", "3")}
	return []scriptedStep{
		refreshStep(rows...),
		func(dynamics.Envelope) []json.RawMessage {
			interactions := []json.RawMessage{
				recordUpdate(deleteWorkspace, map[string]any{"dataSourceName_internal": dynamics.DataSourceReports, "MasterTrackingId": "15", "_RESERVED_IsMarked": "true"}),
				activeRow("15"),
			}
			if markOthers {
				interactions = append(interactions, recordUpdate(deleteWorkspace, map[string]any{"dataSourceName_internal": dynamics.DataSourceReports, "MasterTrackingId": "13", "_RESERVED_IsMarked": "true"}))
			}
			return interactions
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{rootedViewModel("confirm", map[string]any{
				"Id": "confirm", "Name": dynamics.FormConfirmation, "TypeName": "LightBox",
				"ChildViewModels": []any{control("yes", dynamics.ControlConfirmYes), control("no", dynamics.ControlConfirmNo)},
			})}
		},
		func(dynamics.Envelope) []json.RawMessage { return []json.RawMessage{deleteViewModel("confirm")} },
		refreshStep(rows[0], rows[2]),
	}
}

func TestDeleteDraftReportMarksOnlyTheTargetAndVerifiesItIsGone(t *testing.T) {
	t.Parallel()
	server, emitted := scriptedServer(t, deleteSteps(false))
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	result, err := client.DeleteDraftReport(context.Background(), expense.DeleteReportRequest{ReportNumber: "ER-0002"})
	if err != nil {
		t.Fatalf("DeleteDraftReport() error = %v", err)
	}
	if want := (expense.DeleteReportResult{ReportNumber: "ER-0002", Purpose: "Purpose ER-0002", Deleted: true}); result != want {
		t.Fatalf("DeleteDraftReport() = %#v, want %#v", result, want)
	}
	if got, want := *emitted, []string{
		"ExecuteShortcuts:" + deleteWorkspace,
		"ChangeSelectedIndex:grid",
		"UpdateLastSelectedControl:" + deleteWorkspace, "Click:delete",
		"Click:yes",
		"ExecuteShortcuts:" + deleteWorkspace,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted commands = %v, want %v", got, want)
	}
}

func TestDeleteDraftReportStopsBeforeDeleteWhenOtherRowsAreMarked(t *testing.T) {
	t.Parallel()
	server, emitted := scriptedServer(t, deleteSteps(true)[:2])
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	_, err := client.DeleteDraftReport(context.Background(), expense.DeleteReportRequest{ReportNumber: "ER-0002"})
	if err == nil || !strings.Contains(err.Error(), "did not mark exactly that report") || errors.Is(err, expense.ErrOperationUncertain) {
		t.Fatalf("DeleteDraftReport() error = %v, want certain marking failure", err)
	}
	if len(*emitted) != 2 {
		t.Fatalf("emitted commands = %v, want only refresh and selection", *emitted)
	}
}

func TestDeleteDraftReportRefusesNonDraftAndMissingReports(t *testing.T) {
	t.Parallel()
	for report, want := range map[string]string{"ER-0003": "is not a Draft", "ER-0009": "is not among the 3 most recent reports"} {
		t.Run(report, func(t *testing.T) {
			server, emitted := scriptedServer(t, deleteSteps(false)[:1])
			client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))
			_, err := client.DeleteDraftReport(context.Background(), expense.DeleteReportRequest{ReportNumber: report})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("DeleteDraftReport() error = %v, want %q", err, want)
			}
			if len(*emitted) != 1 {
				t.Fatalf("emitted commands = %v, want only the refresh", *emitted)
			}
		})
	}
}

func TestPlanDeleteDraftReportRequiresReportsGrid(t *testing.T) {
	t.Parallel()
	client := deleteClient(t, "https://example.test", false)
	if _, err := client.PlanDeleteDraftReport(expense.DeleteReportRequest{ReportNumber: "ER-0042"}); !errors.Is(err, expense.ErrReportsGridUnavailable) {
		t.Fatalf("PlanDeleteDraftReport() error = %v, want ErrReportsGridUnavailable", err)
	}
}
