package expense_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func popupRow(tracking, date, amount, merchant string) map[string]any {
	return map[string]any{
		"dataSourceName_internal": dynamics.DataSourceExpenseLines, "MasterTrackingId": tracking,
		"TransDate_field": date, "AmountCurr_field": amount, "MerchantId_field": merchant,
	}
}

func receiptMatchSteps(attachRemovesLine bool) []scriptedStep {
	ride := draftLine("160", "Ground Transportation", "2026-10-02", "45.50", "EXAMPLE RIDES")
	hotel := draftLine("162", "Hotel", "2026-10-03", "120.00", "EXAMPLE HOTEL")
	steps := openSteps(ride, hotel)
	open := steps[2]
	steps[2] = func(request dynamics.Envelope) []json.RawMessage {
		return append(open(request), controlUpdate("details", control("receipts-tab", dynamics.ControlReceiptsTabPage)))
	}
	receipts := func(active string) json.RawMessage {
		return mustRaw(map[string]any{"$type": "UpdateModelCollectionInteraction", "RootId": "details", "Descriptor": map[string]any{
			"Name": dynamics.DataSourceReceipts, "ActiveMasterTrackingId": active,
		}})
	}
	return append(steps,
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				controlUpdate("details", control("receipts-grid", dynamics.ControlReceiptsGrid)),
				controlUpdate("details", control("match", dynamics.ControlMatchExpensesButton)),
				recordUpdate("details", map[string]any{"dataSourceName_internal": dynamics.DataSourceReceipts, "MasterTrackingId": "212", "FileName_field": "ride-receipt"}),
				recordUpdate("details", map[string]any{"dataSourceName_internal": dynamics.DataSourceReceipts, "MasterTrackingId": "214", "FileName_field": "hotel-folio"}),
				receipts("212"),
			}
		},
		func(dynamics.Envelope) []json.RawMessage { return []json.RawMessage{receipts("214")} },
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{rootedViewModel("popup", map[string]any{
				"Id": "popup", "Name": dynamics.FormReceiptMatch, "TypeName": "DropDialogPopup",
				"ChildModelCollections": map[string]any{dynamics.DataSourceExpenseLines: map[string]any{
					"Name": dynamics.DataSourceExpenseLines, "IsLastPage": true, "ActiveMasterTrackingId": "354",
					"Items": []any{
						map[string]any{"Item": map[string]any{"Properties": popupRow("354", "2026-10-02", "45.5000000000000000", "EXAMPLE RIDES")}},
						map[string]any{"Item": map[string]any{"Properties": popupRow("356", "2026-10-03", "120.0000000000000000", "EXAMPLE HOTEL")}},
					},
				}},
				"ChildViewModels": []any{control("popup-grid", dynamics.ControlLinesGrid), control("attach", dynamics.ControlAttachButton)},
			})}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{mustRaw(map[string]any{"$type": "UpdateModelCollectionInteraction", "RootId": "popup", "Descriptor": map[string]any{
				"Name": dynamics.DataSourceExpenseLines, "ActiveMasterTrackingId": "356",
			}})}
		},
		func(dynamics.Envelope) []json.RawMessage {
			remaining := []any{map[string]any{"Item": map[string]any{"Properties": map[string]any{"MasterTrackingId": "405"}}}}
			if !attachRemovesLine {
				remaining = append(remaining, map[string]any{"Item": map[string]any{"Properties": map[string]any{"MasterTrackingId": "406"}}})
			}
			return []json.RawMessage{
				recordUpdate("details", map[string]any{"dataSourceName_internal": dynamics.DataSourceReceiptMatches, "MasterTrackingId": "404", "recId_internal": "9"}),
				mustRaw(map[string]any{"$type": "UpdateModelCollectionInteraction", "RootId": "popup", "Descriptor": map[string]any{
					"Name": dynamics.DataSourceExpenseLines, "IsLastPage": true,
					"SpliceOperations": []any{map[string]any{"ClearCache": true, "Insert": []any{}}, map[string]any{"Index": 0, "Delete": 2, "Insert": remaining}},
				}}),
				recordUpdate("popup", popupRow("405", "2026-10-02", "45.5000000000000000", "EXAMPLE RIDES")),
				recordUpdate("popup", popupRow("406", "2026-10-03", "120.0000000000000000", "EXAMPLE HOTEL")),
			}
		},
		func(dynamics.Envelope) []json.RawMessage { return []json.RawMessage{deleteViewModel("popup")} },
		workspaceStep,
	)
}

func TestAttachReportReceiptSelectsReceiptAndLineThenVerifiesTheMatch(t *testing.T) {
	t.Parallel()
	server, emitted := scriptedServer(t, receiptMatchSteps(true))
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	result, err := client.AttachReportReceipt(context.Background(), expense.AttachReportReceiptRequest{ReportNumber: "ER-0002", Line: 2, Receipt: "hotel-folio"})
	if err != nil {
		t.Fatalf("AttachReportReceipt() error = %v", err)
	}
	if want := (expense.AttachReportReceiptResult{Receipt: "hotel-folio", Date: "2026-10-03", Category: "Hotel", Amount: "120.00 USD"}); result != want {
		t.Fatalf("AttachReportReceipt() = %#v, want %#v", result, want)
	}
	if got, want := (*emitted)[3:], []string{
		"ActivateTab:receipts-tab", "ChangeSelectedIndex:receipts-grid",
		"UpdateLastSelectedControl:details", "RequestPopup:match",
		"ChangeSelectedIndex:popup-grid", "Click:attach", "RequestClose:popup", "Click:save-and-close",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted commands = %v, want %v", got, want)
	}
}

func TestAttachReportReceiptFailsWhenTheLineIsStillOffered(t *testing.T) {
	t.Parallel()
	steps := receiptMatchSteps(false)
	server, emitted := scriptedServer(t, steps[:len(steps)-2])
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	_, err := client.AttachReportReceipt(context.Background(), expense.AttachReportReceiptRequest{ReportNumber: "ER-0002", Line: 2, Receipt: "hotel-folio"})
	if err == nil || !strings.Contains(err.Error(), "not removed from the offered lines") {
		t.Fatalf("AttachReportReceipt() error = %v, want unverified match", err)
	}
	for _, command := range *emitted {
		if command == "Click:save-and-close" {
			t.Fatal("report was saved after an unverified attach")
		}
	}
}

func TestAttachReportReceiptRejectsUnknownReceiptBeforeChanges(t *testing.T) {
	t.Parallel()
	steps := receiptMatchSteps(true)
	server, emitted := scriptedServer(t, steps[:4])
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	_, err := client.AttachReportReceipt(context.Background(), expense.AttachReportReceiptRequest{ReportNumber: "ER-0002", Line: 2, Receipt: "missing"})
	if err == nil || !strings.Contains(err.Error(), `no receipt named "missing"`) {
		t.Fatalf("AttachReportReceipt() error = %v, want unknown receipt", err)
	}
	if last := (*emitted)[len(*emitted)-1]; last != "ActivateTab:receipts-tab" {
		t.Fatalf("last command = %s, want the Receipts tab activation", last)
	}
}
