package expense_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func draftLine(tracking, category, date, amount, merchant string) map[string]any {
	row := lineRow(tracking, category, date)
	row["amountWithCurrencyCode_dataMethod"] = amount + " USD"
	row["MerchantId_field"] = merchant
	return row
}

// openSteps refresh the reports grid, activate ER-0002, and open it with the
// given lines; the first line is active.
func openSteps(lines ...map[string]any) []scriptedStep {
	items := make([]any, len(lines))
	for index, line := range lines {
		items[index] = map[string]any{"Item": map[string]any{"Properties": line}}
	}
	return []scriptedStep{
		refreshStep(reportRow("13", "ER-0001", "1"), reportRow("15", "ER-0002", "1"), reportRow("17", "ER-0003", "3")),
		func(dynamics.Envelope) []json.RawMessage { return []json.RawMessage{activeRow("15")} },
		func(dynamics.Envelope) []json.RawMessage {
			submit := submitButtonDescriptor("submit")
			return []json.RawMessage{rootedViewModel("details", map[string]any{
				"Id": "details", "Name": dynamics.FormExpenseReportDetails, "TypeName": "Form",
				"ChildModelCollections": map[string]any{
					"TrvExpTable_ds": map[string]any{"Items": []any{map[string]any{"Item": map[string]any{"Properties": map[string]any{
						"dataSourceName_internal": "TrvExpTable_ds", "ExpNumber_field": "ER-0002", "expenseReportStatus_dataMethod": "Draft",
					}}}}},
					dynamics.DataSourceExpenseLines: map[string]any{
						"Name": dynamics.DataSourceExpenseLines, "IsLastPage": true, "ActiveMasterTrackingId": lines[0]["MasterTrackingId"], "Items": items,
					},
				},
				"ChildViewModels": []any{
					control("save-and-close", dynamics.ControlSaveAndClose), submit,
					control("lines-grid", dynamics.ControlLinesGrid), control("card-menu", dynamics.ControlExpenseCardMenu),
				},
			})}
		},
	}
}

func workspaceStep(dynamics.Envelope) []json.RawMessage {
	return []json.RawMessage{rootedViewModel(deleteWorkspace, map[string]any{
		"Id": deleteWorkspace, "Name": dynamics.FormExpenseWorkspace, "TypeName": "Form",
		"ChildViewModels": []any{control("new-report", dynamics.SelectedControlNewExpenseReportReportsTab)},
	})}
}

func TestShowDraftReportActivatesOpensListsAndCloses(t *testing.T) {
	t.Parallel()
	steps := append(openSteps(
		draftLine("160", "Parking", "2026-09-29", "3.00", "EXAMPLE TRANSIT"),
		draftLine("162", "Hotel", "2026-10-03", "120.00", "EXAMPLE HOTEL"),
	), workspaceStep)
	server, emitted := scriptedServer(t, steps)
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	details, err := client.ShowDraftReport(context.Background(), "ER-0002")
	if err != nil {
		t.Fatalf("ShowDraftReport() error = %v", err)
	}
	want := expense.ReportDetails{ReportNumber: "ER-0002", Purpose: "Purpose ER-0002", Status: "Draft", Lines: []expense.ReportLine{
		{Number: 1, Date: "2026-09-29", Category: "Parking", Amount: "3.00 USD", Merchant: "EXAMPLE TRANSIT"},
		{Number: 2, Date: "2026-10-03", Category: "Hotel", Amount: "120.00 USD", Merchant: "EXAMPLE HOTEL"},
	}}
	if !reflect.DeepEqual(details, want) {
		t.Fatalf("ShowDraftReport() = %#v, want %#v", details, want)
	}
	if got, want := *emitted, []string{
		"ExecuteShortcuts:" + deleteWorkspace, "ChangeSelectedIndex:grid", "ExecuteHyperlink:link", "Click:save-and-close",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted commands = %v, want %v", got, want)
	}
}

func TestItemizeDraftReportLineSelectsTheLineAndVerifiesNewLines(t *testing.T) {
	t.Parallel()
	parking := draftLine("160", "Parking", "2026-09-29", "3.00", "EXAMPLE TRANSIT")
	hotel := draftLine("162", "Hotel", "2026-10-03", "120.00", "EXAMPLE HOTEL")
	steps := append(openSteps(parking, hotel),
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{mustRaw(map[string]any{"$type": "UpdateModelCollectionInteraction", "RootId": "details", "Descriptor": map[string]any{
				"Name": dynamics.DataSourceExpenseLines, "ActiveMasterTrackingId": "162",
			}})}
		},
		func(dynamics.Envelope) []json.RawMessage {
			button := control("itemize", dynamics.ControlItemizeExpenseButton)
			button["ValueProperties"] = map[string]any{"MenuItemName": "ExpenseItemizeExpense", "PrimaryModelName": dynamics.DataSourceExpenseLines}
			button["SerializedValueProperties"] = map[string]any{"Enabled": "true", "Visible": "true"}
			return []json.RawMessage{controlUpdate("details", button)}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{rootedViewModel("itemize-form", map[string]any{
				"Id": "itemize-form", "Name": dynamics.FormExpenseItemizeExpense, "TypeName": "Dialog",
				"ChildModelCollections": map[string]any{dynamics.DataSourceItemizationRows: map[string]any{"Items": []any{map[string]any{"Item": map[string]any{
					"Properties": itemizationRow("20", "0", "2026-10-03", "0", "0"),
				}}}}},
				"ChildViewModels": []any{
					control("new-row", dynamics.ControlItemizationNewButton), control("subcategory", dynamics.ControlItemizationSubcategory),
					control("subcategory-name", dynamics.ControlItemizationSubcatName), control("start-date", dynamics.ControlItemizationStartDate),
					control("daily-rate", dynamics.ControlItemizationDailyRate), control("quantity", dynamics.ControlItemizationQuantity),
					control("close", dynamics.ControlItemizationCloseButton),
					amountControl("total", dynamics.ControlItemizationTotal, "120.0000000000000000"),
				},
			})}
		},
		subcategoryStep("Incidentals"),
		rowUpdateStep("20", map[string]any{"DailyRate_field": "120.0000000000000000"}),
		rowUpdateStep("20", map[string]any{"Quantity_field": "1"}, amountControl("remaining", dynamics.ControlItemizationRemaining, "0.0000000000000000")),
		// Reloaded rows reuse RecIds by position, so verification compares content.
		func(dynamics.Envelope) []json.RawMessage {
			return append([]json.RawMessage{deleteViewModel("itemize-form")},
				fullLineList(parking, hotel, draftLine("164", "Incidentals", "2026-10-03", "120.00", ""))...)
		},
		workspaceStep,
	)
	server, emitted := scriptedServer(t, steps)
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	result, err := client.ItemizeDraftReportLine(context.Background(), expense.ItemizeReportLineRequest{
		ReportNumber: "ER-0002", Line: 2,
		Itemization: []expense.ItemizationInput{{Subcategory: "Incidentals", DailyRate: "120.00", Days: 1}},
	})
	if err != nil {
		t.Fatalf("ItemizeDraftReportLine() error = %v", err)
	}
	if want := (expense.ExpenseLineResult{Category: "Hotel", Date: "2026-10-03", Amount: "120.00 USD", ItemizedLines: 1}); result != want {
		t.Fatalf("ItemizeDraftReportLine() = %#v, want %#v", result, want)
	}
	if got := strings.Join(*emitted, " "); !strings.Contains(got, "ChangeSelectedIndex:lines-grid UpdateLastSelectedControl:details Click:card-menu") {
		t.Fatalf("emitted commands did not select the line before opening its menu: %s", got)
	}
}

func TestItemizeDraftReportLineRejectsRowsThatDoNotMatchTheLine(t *testing.T) {
	t.Parallel()
	parking := draftLine("160", "Parking", "2026-09-29", "3.00", "EXAMPLE TRANSIT")
	steps := openSteps(parking)
	steps = append(steps, func(dynamics.Envelope) []json.RawMessage {
		button := control("itemize", dynamics.ControlItemizeExpenseButton)
		button["ValueProperties"] = map[string]any{"MenuItemName": "ExpenseItemizeExpense", "PrimaryModelName": dynamics.DataSourceExpenseLines}
		button["SerializedValueProperties"] = map[string]any{"Enabled": "true", "Visible": "true"}
		return []json.RawMessage{controlUpdate("details", button)}
	}, func(dynamics.Envelope) []json.RawMessage {
		return []json.RawMessage{rootedViewModel("itemize-form", map[string]any{
			"Id": "itemize-form", "Name": dynamics.FormExpenseItemizeExpense, "TypeName": "Dialog",
			"ChildViewModels": []any{
				control("new-row", dynamics.ControlItemizationNewButton), control("subcategory", dynamics.ControlItemizationSubcategory),
				control("subcategory-name", dynamics.ControlItemizationSubcatName), control("start-date", dynamics.ControlItemizationStartDate),
				control("daily-rate", dynamics.ControlItemizationDailyRate), control("quantity", dynamics.ControlItemizationQuantity),
				control("close", dynamics.ControlItemizationCloseButton),
				amountControl("total", dynamics.ControlItemizationTotal, "3.0000000000000000"),
			},
		})}
	})
	server, emitted := scriptedServer(t, steps)
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	_, err := client.ItemizeDraftReportLine(context.Background(), expense.ItemizeReportLineRequest{
		ReportNumber: "ER-0002", Line: 1,
		Itemization: []expense.ItemizationInput{{Subcategory: "Incidentals", DailyRate: "120.00", Days: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "total does not match") {
		t.Fatalf("ItemizeDraftReportLine() error = %v, want total mismatch", err)
	}
	for _, command := range *emitted {
		if strings.HasPrefix(command, "SetValue") {
			t.Fatalf("itemization rows were set despite the total mismatch: %v", *emitted)
		}
	}
}

func TestSubmitDraftReportVerifiesStatusThroughRefresh(t *testing.T) {
	t.Parallel()
	for name, status := range map[string]string{"submitted": "2", "still draft": "1"} {
		t.Run(name, func(t *testing.T) {
			steps := append(openSteps(draftLine("160", "Hotel", "2026-10-03", "120.00", "EXAMPLE HOTEL")),
				workspaceStep,
				refreshStep(reportRow("13", "ER-0001", "1"), reportRow("15", "ER-0002", status)),
			)
			server, emitted := scriptedServer(t, steps)
			client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

			result, err := client.SubmitDraftReport(context.Background(), "ER-0002")
			if status == "1" {
				if err == nil || !errors.Is(err, expense.ErrOperationUncertain) || !strings.Contains(err.Error(), "still a Draft") {
					t.Fatalf("SubmitDraftReport() error = %v, want uncertain still-Draft failure", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SubmitDraftReport() error = %v", err)
			}
			if want := (expense.ReportResult{Purpose: "Purpose ER-0002", ReportNumber: "ER-0002", Status: "2", Submitted: true}); !reflect.DeepEqual(result, want) {
				t.Fatalf("SubmitDraftReport() = %#v, want %#v", result, want)
			}
			if got := (*emitted)[3]; got != "Click:submit" {
				t.Fatalf("fourth command = %s, want Click:submit (all: %v)", got, *emitted)
			}
		})
	}
}
