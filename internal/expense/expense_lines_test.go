package expense_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func TestCreateReportAddsAndItemizesExpenseLines(t *testing.T) {
	t.Parallel()

	steps := expenseLineSteps(nil)
	server, emitted := scriptedServer(t, steps)
	client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	request := expense.CreateReportRequest{
		Purpose:     "Conference travel",
		Expenses:    testExpenseLines(),
		FinalAction: expense.ReportFinalActionSaveDraft,
	}
	plan, err := client.PlanCreateReport(request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Expenses != 2 || plan.RequestCount != len(steps) {
		t.Fatalf("plan = %#v, want 2 expenses and %d requests", plan, len(steps))
	}

	report, err := client.CreateReport(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateReport() error = %v", err)
	}
	want := expense.ReportResult{
		Purpose: "Conference travel", ReportNumber: "ER-0042", Status: "Draft", SavedAndClosed: true,
		Expenses: []expense.ExpenseLineResult{
			{Category: "Hotel", Date: "2026-10-10", Amount: "300.00", ItemizedLines: 4},
			{Category: "Airline Fees", Date: "2026-10-11", Amount: "35"},
		},
	}
	if !reflect.DeepEqual(report, want) {
		t.Fatalf("CreateReport() = %#v, want %#v", report, want)
	}
	if got, want := *emitted, []string{
		"UpdateLastSelectedControl:captured-workspace-root", "Click:captured-new-button",
		"SetValue:purpose", "ExecuteShortcuts:dialog",
		"UpdateLastSelectedControl:details", "Click:new-expense",
		"SetValue:category", "SetValue:date", "SetValue:amount", "SetValue:merchant",
		"Click:save",
		"UpdateLastSelectedControl:details", "Click:card-menu",
		"UpdateLastSelectedControl:details", "Click:itemize",
		"SetValue:subcategory-name", "ResolveChanges:subcategory",
		"SetValue:start-date", "SetValue:daily-rate", "SetValue:quantity",
		"UpdateLastSelectedControl:itemize-form", "Click:new-row",
		"SetValue:subcategory-name", "ResolveChanges:subcategory",
		"SetValue:daily-rate", "SetValue:quantity",
		"Click:close",
		"UpdateLastSelectedControl:details", "Click:new-expense",
		"SetValue:category", "SetValue:date", "SetValue:amount",
		"Click:save",
		"Click:save-and-close",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted commands =\n%v\nwant\n%v", got, want)
	}
}

func TestCreateReportFailsClosedWhenDynamicsChangesAnExpenseField(t *testing.T) {
	t.Parallel()

	// The merchant response reports a different category, as merchant-based
	// category mapping would.
	steps := expenseLineSteps(map[int]func([]json.RawMessage) []json.RawMessage{
		6: func([]json.RawMessage) []json.RawMessage {
			return []json.RawMessage{recordUpdate("expense-dialog", expenseRecord(map[string]any{"CostType_field": "Airfare", "MerchantId_field": "Test Hotel"}))}
		},
	})
	server, emitted := scriptedServer(t, steps[:7])
	client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateReport(context.Background(), expense.CreateReportRequest{
		Purpose: "Conference travel", Expenses: testExpenseLines(), FinalAction: expense.ReportFinalActionSaveDraft,
	})
	if err == nil || !errors.Is(err, expense.ErrOperationUncertain) || !strings.Contains(err.Error(), `CategoryInput as "Airfare"`) {
		t.Fatalf("CreateReport() error = %v, want uncertain category mismatch", err)
	}
	for _, command := range *emitted {
		if command == "Click:save" {
			t.Fatal("expense line was saved after a verification failure")
		}
	}
}

func TestExpenseLineValidationRunsBeforeNetwork(t *testing.T) {
	t.Parallel()

	client, err := expense.New(validProfile("https://example.test"))
	if err != nil {
		t.Fatal(err)
	}
	valid := testExpenseLines()[0]
	tests := map[string]struct {
		mutate func(*expense.ExpenseLineInput)
		action expense.ReportFinalAction
		want   string
	}{
		"submit":           {func(*expense.ExpenseLineInput) {}, expense.ReportFinalActionSubmit, "only when saving a Draft"},
		"date":             {func(line *expense.ExpenseLineInput) { line.Date = "10/10/2026" }, "", "YYYY-MM-DD"},
		"amount precision": {func(line *expense.ExpenseLineInput) { line.Amount = "300.001" }, "", "two fractional digits"},
		"itemized total":   {func(line *expense.ExpenseLineInput) { line.Itemization[1].DailyRate = "20" }, "", "itemization totals 290.00"},
		"days":             {func(line *expense.ExpenseLineInput) { line.Itemization[0].Days = 0 }, "", "days must be"},
		"currency":         {func(line *expense.ExpenseLineInput) { line.Currency = "US" }, "", "three-letter"},
		"category":         {func(line *expense.ExpenseLineInput) { line.Category = " " }, "", "category is empty"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			line := valid
			line.Itemization = append([]expense.ItemizationInput(nil), valid.Itemization...)
			test.mutate(&line)
			action := test.action
			if action == "" {
				action = expense.ReportFinalActionSaveDraft
			}
			_, err := client.PlanCreateReport(expense.CreateReportRequest{Purpose: "Trip", Expenses: []expense.ExpenseLineInput{line}, FinalAction: action})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("PlanCreateReport() error = %v, want %q", err, test.want)
			}
		})
	}
}

func testExpenseLines() []expense.ExpenseLineInput {
	return []expense.ExpenseLineInput{
		{
			Category: "Hotel", Date: "2026-10-10", Amount: "300.00", Merchant: "Test Hotel",
			Itemization: []expense.ItemizationInput{
				{Subcategory: "Daily Room Rate", StartDate: "2026-10-10", DailyRate: "125.00", Days: 2},
				{Subcategory: "Hotel Tax", DailyRate: "25", Days: 2},
			},
		},
		{Category: "Airline Fees", Date: "2026-10-11", Amount: "35"},
	}
}

type scriptedStep func(request dynamics.Envelope) []json.RawMessage

// expenseLineSteps returns one response builder per expected request.
// Overrides replace a step's interactions by index.
func expenseLineSteps(overrides map[int]func([]json.RawMessage) []json.RawMessage) []scriptedStep {
	details := "details"
	steps := []scriptedStep{
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{viewModelInteraction(map[string]any{
				"Id": "dialog", "Name": dynamics.FormExpenseNewExpenseReport, "TypeName": "Dialog",
				"ChildViewModels": []any{control("purpose", dynamics.ControlNamePurpose)},
			})}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{viewModelInteraction(map[string]any{
				"Id": details, "Name": dynamics.FormExpenseReportDetails, "TypeName": "Form",
				"ChildModelCollections": map[string]any{"TrvExpTable_ds": map[string]any{"Items": []any{map[string]any{"Item": map[string]any{
					"Id": "report", "Properties": map[string]any{
						"dataSourceName_internal": "TrvExpTable_ds", "ExpNumber_field": "ER-0042", "expenseReportStatus_dataMethod": "Draft",
					},
				}}}}},
				"ChildViewModels": []any{
					control("save-and-close", dynamics.ControlSaveAndClose),
					control("new-expense", dynamics.ControlNewExpenseButton),
				},
			})}
		},
	}
	steps = append(steps, expenseDialogSteps("expense-dialog", "Hotel", "2026-10-10", "300.0000000000000000", "Test Hotel", "5001",
		controlUpdate(details, control("card-menu", dynamics.ControlExpenseCardMenu)))...)
	steps = append(steps,
		func(dynamics.Envelope) []json.RawMessage {
			button := control("itemize", dynamics.ControlItemizeExpenseButton)
			button["ValueProperties"] = map[string]any{"MenuItemName": "ExpenseItemizeExpense", "PrimaryModelName": dynamics.DataSourceExpenseLines}
			button["SerializedValueProperties"] = map[string]any{"Enabled": "true", "Visible": "true"}
			return []json.RawMessage{controlUpdate(details, button)}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{rootedViewModel("itemize-form", map[string]any{
				"Id": "itemize-form", "Name": dynamics.FormExpenseItemizeExpense, "TypeName": "Dialog",
				"ChildModelCollections": map[string]any{dynamics.DataSourceItemizationRows: map[string]any{"Items": []any{map[string]any{"Item": map[string]any{
					"Id": "row-1", "Properties": itemizationRow("20", "0", "2026-10-10", "0", "0"),
				}}}}},
				"ChildViewModels": []any{
					control("new-row", dynamics.ControlItemizationNewButton),
					control("subcategory", dynamics.ControlItemizationSubcategory),
					control("subcategory-name", dynamics.ControlItemizationSubcatName),
					control("start-date", dynamics.ControlItemizationStartDate),
					control("daily-rate", dynamics.ControlItemizationDailyRate),
					control("quantity", dynamics.ControlItemizationQuantity),
					control("close", dynamics.ControlItemizationCloseButton),
					amountControl("total", dynamics.ControlItemizationTotal, "300.0000000000000000"),
					amountControl("remaining", dynamics.ControlItemizationRemaining, "300.0000000000000000"),
				},
			})}
		},
		subcategoryStep("Daily Room Rate"),
		rowUpdateStep("20", map[string]any{"StartDate_field": "2026-10-10"}),
		rowUpdateStep("20", map[string]any{"DailyRate_field": "125.0000000000000000"}),
		rowUpdateStep("20", map[string]any{"Quantity_field": "2"}, amountControl("remaining", dynamics.ControlItemizationRemaining, "50.0000000000000000")),
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				recordUpdate("itemize-form", itemizationRow("20", "1", "2026-10-10", "125.0000000000000000", "2")),
				recordInsert("itemize-form", dynamics.DataSourceItemizationRows, itemizationRow("21", "0", "2026-10-10", "0", "0")),
			}
		},
		subcategoryStep("Hotel Tax"),
		rowUpdateStep("21", map[string]any{"DailyRate_field": "25.0000000000000000"}),
		rowUpdateStep("21", map[string]any{"Quantity_field": "2"}, amountControl("remaining", dynamics.ControlItemizationRemaining, "0.0000000000000000")),
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				deleteViewModel("itemize-form"),
				recordUpdate(details, savedLine("5001", "Hotel", "2026-10-10")),
				recordInsert(details, dynamics.DataSourceExpenseLines,
					savedLine("5002", "Daily Room Rate", "2026-10-10"), savedLine("5003", "Daily Room Rate", "2026-10-11"),
					savedLine("5004", "Hotel Tax", "2026-10-10"), savedLine("5005", "Hotel Tax", "2026-10-11")),
			}
		},
	)
	steps = append(steps, expenseDialogSteps("expense-dialog-2", "Airline Fees", "2026-10-11", "35.0000000000000000", "", "5006")...)
	steps = append(steps, func(dynamics.Envelope) []json.RawMessage {
		return []json.RawMessage{viewModelInteraction(map[string]any{
			"Id": "workspace", "Name": dynamics.FormExpenseWorkspace, "TypeName": "Form",
			"ChildViewModels": []any{control("new-report", dynamics.SelectedControlNewExpenseReportReportsTab)},
		})}
	})
	for index, override := range overrides {
		previous := steps[index]
		steps[index] = func(request dynamics.Envelope) []json.RawMessage { return override(previous(request)) }
	}
	return steps
}

func expenseDialogSteps(dialog, category, date, amount, merchant, recID string, saveExtra ...json.RawMessage) []scriptedStep {
	steps := []scriptedStep{
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				viewModelInteraction(map[string]any{
					"Id": dialog, "Name": dynamics.FormExpenseAddNewExpense, "TypeName": "Dialog",
					"ChildViewModels": []any{
						control("category", dynamics.ControlCategoryInput), control("date", dynamics.ControlDateInput),
						control("amount", dynamics.ControlAmountInput), control("currency", dynamics.ControlCurrencyInput),
						control("merchant", dynamics.ControlMerchantInput), control("notes", dynamics.ControlNotesInput),
						control("save", dynamics.ControlSaveButton),
					},
				}),
				recordUpdate(dialog, expenseRecord(map[string]any{
					"recId_internal": "0", "CostType_field": "", "TransDate_field": "2026-10-13",
					"AmountCurr_field": "0.0000000000000000", "ExchangeCode_field": "USD", "MerchantId_field": "",
				})),
			}
		},
		// Later responses carry only the changed field, as Dynamics does.
		fieldStep(dialog, "CostType_field", category),
		fieldStep(dialog, "TransDate_field", date),
		fieldStep(dialog, "AmountCurr_field", amount),
	}
	if merchant != "" {
		steps = append(steps, fieldStep(dialog, "MerchantId_field", merchant))
	}
	return append(steps, func(dynamics.Envelope) []json.RawMessage {
		return append([]json.RawMessage{
			deleteViewModel(dialog),
			recordInsert("details", dynamics.DataSourceExpenseLines, savedLine(recID, category, date)),
		}, saveExtra...)
	})
}

func fieldStep(dialog, field, value string) scriptedStep {
	return func(dynamics.Envelope) []json.RawMessage {
		return []json.RawMessage{recordUpdate(dialog, expenseRecord(map[string]any{field: value}))}
	}
}

func subcategoryStep(name string) scriptedStep {
	return func(dynamics.Envelope) []json.RawMessage {
		return []json.RawMessage{recordUpdate("itemize-form", map[string]any{
			"dataSourceName_internal": dynamics.DataSourceItemizationSubcats, "recId_internal": "77", "Name_field": name,
		})}
	}
}

func rowUpdateStep(trackingID string, fields map[string]any, extra ...json.RawMessage) scriptedStep {
	return func(dynamics.Envelope) []json.RawMessage {
		fields["dataSourceName_internal"] = dynamics.DataSourceItemizationRows
		fields["MasterTrackingId"] = trackingID
		return append([]json.RawMessage{recordUpdate("itemize-form", fields)}, extra...)
	}
}

func expenseRecord(fields map[string]any) map[string]any {
	fields["dataSourceName_internal"] = dynamics.DataSourceExpenseLines
	fields["MasterTrackingId"] = "9"
	return fields
}

func itemizationRow(trackingID, recID, startDate, rate, quantity string) map[string]any {
	return map[string]any{
		"dataSourceName_internal": dynamics.DataSourceItemizationRows, "MasterTrackingId": trackingID, "recId_internal": recID,
		"StartDate_field": startDate, "DailyRate_field": rate, "Quantity_field": quantity,
	}
}

func savedLine(recID, category, date string) map[string]any {
	return map[string]any{
		"dataSourceName_internal": dynamics.DataSourceExpenseLines, "recId_internal": recID,
		"TransDate_field": date, "categoryOrSubCategoryName_dataMethod": category,
	}
}

func control(id, name string) map[string]any {
	return map[string]any{"Id": id, "Name": name, "TypeName": "Control"}
}

func amountControl(id, name, value string) json.RawMessage {
	return controlUpdate("itemize-form", map[string]any{"Id": id, "Name": name, "TypeName": "Real", "ValueProperties": map[string]any{"Value": value}})
}

func rootedViewModel(rootID string, descriptor map[string]any) json.RawMessage {
	return mustRaw(map[string]any{"$type": "CreateViewModelInteraction", "RootId": rootID, "Descriptor": descriptor})
}

func controlUpdate(rootID string, descriptor map[string]any) json.RawMessage {
	return mustRaw(map[string]any{"$type": "UpdateViewModelInteraction", "RootId": rootID, "Descriptor": descriptor})
}

func recordUpdate(rootID string, properties map[string]any) json.RawMessage {
	return mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": rootID, "Descriptor": map[string]any{"Id": "record", "Properties": properties}})
}

func recordInsert(rootID, dataSource string, records ...map[string]any) json.RawMessage {
	items := make([]any, len(records))
	for index, record := range records {
		items[index] = map[string]any{"Item": map[string]any{"Properties": record}}
	}
	return mustRaw(map[string]any{"$type": "UpdateModelCollectionInteraction", "RootId": rootID, "Descriptor": map[string]any{
		"Name": dataSource, "SpliceOperations": []any{map[string]any{"Insert": items}},
	}})
}

func deleteViewModel(rootID string) json.RawMessage {
	return mustRaw(map[string]any{"$type": dynamics.InteractionTypeDeleteViewModel, "TargetId": rootID, "RootId": rootID})
}

// scriptedServer answers each request with the next step and records every
// emitted command as CommandName:TargetID.
func scriptedServer(t *testing.T, steps []scriptedStep) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var emitted []string
	next := 0
	serverSequence := int64(50)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var request dynamics.Envelope
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request.LastAcknowledgedSequenceNumber != serverSequence {
			t.Errorf("request %d acknowledged %d, want %d", next+1, request.LastAcknowledgedSequenceNumber, serverSequence)
		}
		for _, message := range request.Messages {
			for _, command := range mustCommands(t, message) {
				emitted = append(emitted, command.CommandName+":"+command.TargetID)
			}
		}
		if next >= len(steps) {
			t.Errorf("unexpected request %d", next+1)
			http.Error(w, "unexpected", http.StatusInternalServerError)
			return
		}
		serverSequence++
		interactions := steps[next](request)
		next++
		w.Header().Set("Content-Type", "application/json")
		writeEnvelope(t, w, responseEnvelope(7, request.Messages[len(request.Messages)-1].SequenceNumber, serverSequence, interactions...))
	}))
	t.Cleanup(server.Close)
	return server, &emitted
}
