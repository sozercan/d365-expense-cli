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

func receiptCountControl(count string) json.RawMessage {
	return controlUpdate("details", map[string]any{"Id": "receipt-count", "Name": dynamics.ControlReceiptCount, "ValueProperties": map[string]any{"Value": count}})
}

// receiptReload reloads the Receipts tab as Dynamics does after a removal.
func receiptReload(active string, names map[string]string, order ...string) []json.RawMessage {
	inserts := make([]any, len(order))
	interactions := []json.RawMessage{}
	for index, tracking := range order {
		inserts[index] = map[string]any{"Item": map[string]any{"Properties": map[string]any{"MasterTrackingId": tracking}}}
		interactions = append(interactions,
			recordUpdate("details", map[string]any{"dataSourceName_internal": dynamics.DataSourceReceipts, "MasterTrackingId": tracking, "FileName_field": names[tracking], "_RESERVED_IsMarked": "false"}),
			recordUpdate("details", map[string]any{"dataSourceName_internal": dynamics.DataSourceReceiptRefs, "MasterTrackingId": tracking, "getAttachmentStatus_dataMethod": ""}),
		)
	}
	collection := mustRaw(map[string]any{"$type": "UpdateModelCollectionInteraction", "RootId": "details", "Descriptor": map[string]any{
		"Name": dynamics.DataSourceReceipts, "IsLastPage": true, "ActiveMasterTrackingId": active,
		"SpliceOperations": []any{map[string]any{"ClearCache": true, "Insert": []any{}}, map[string]any{"Index": 0, "Delete": 0, "Insert": inserts}},
	}})
	return append([]json.RawMessage{collection}, interactions...)
}

func confirmStep(dynamics.Envelope) []json.RawMessage {
	return []json.RawMessage{rootedViewModel("confirm", map[string]any{
		"Id": "confirm", "Name": dynamics.FormConfirmation, "TypeName": "LightBox",
		"ChildViewModels": []any{control("yes", dynamics.ControlConfirmYes), control("no", dynamics.ControlConfirmNo)},
	})}
}

func receiptRemoveSteps(attachedStatus string) []scriptedStep {
	steps := openSteps(draftLine("160", "Hotel", "2026-10-03", "120.00", "EXAMPLE HOTEL"))
	open := steps[2]
	steps[2] = func(request dynamics.Envelope) []json.RawMessage {
		items := []any{}
		for _, tracking := range []string{"167", "169", "171"} {
			items = append(items, map[string]any{"Item": map[string]any{"Properties": map[string]any{"dataSourceName_internal": dynamics.DataSourceReceipts, "MasterTrackingId": tracking}}})
		}
		return append(open(request),
			controlUpdate("details", control("receipts-tab", dynamics.ControlReceiptsTabPage)),
			controlUpdate("details", control("remove", dynamics.ControlRemoveReceiptButton)),
			receiptCountControl("3"),
			mustRaw(map[string]any{"$type": "UpdateModelCollectionInteraction", "RootId": "details", "Descriptor": map[string]any{
				"Name": dynamics.DataSourceReceipts, "IsLastPage": true, "ActiveMasterTrackingId": "167", "Items": items,
			}}),
		)
	}
	return append(steps,
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				controlUpdate("details", control("receipts-grid", dynamics.ControlReceiptsGrid)),
				recordUpdate("details", map[string]any{"dataSourceName_internal": dynamics.DataSourceReceipts, "MasterTrackingId": "167", "FileName_field": "test-receipt"}),
				recordUpdate("details", map[string]any{"dataSourceName_internal": dynamics.DataSourceReceipts, "MasterTrackingId": "169", "FileName_field": "test-receipt"}),
				recordUpdate("details", map[string]any{"dataSourceName_internal": dynamics.DataSourceReceipts, "MasterTrackingId": "171", "FileName_field": "hotel-folio"}),
				recordUpdate("details", map[string]any{"dataSourceName_internal": dynamics.DataSourceReceiptRefs, "MasterTrackingId": "169", "getAttachmentStatus_dataMethod": attachedStatus}),
			}
		},
		confirmStep,
		func(dynamics.Envelope) []json.RawMessage {
			return append([]json.RawMessage{deleteViewModel("confirm"), receiptCountControl("2")},
				receiptReload("276", map[string]string{"276": "test-receipt", "277": "hotel-folio"}, "276", "277")...)
		},
		confirmStep,
		func(dynamics.Envelope) []json.RawMessage {
			return append([]json.RawMessage{deleteViewModel("confirm"), receiptCountControl("1")},
				receiptReload("301", map[string]string{"301": "hotel-folio"}, "301")...)
		},
		workspaceStep,
	)
}

func TestRemoveReportReceiptsRemovesEachNamedReceiptAndVerifiesTheList(t *testing.T) {
	t.Parallel()
	server, emitted := scriptedServer(t, receiptRemoveSteps(""))
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	removed, err := client.RemoveReportReceipts(context.Background(), expense.RemoveReportReceiptsRequest{ReportNumber: "ER-0002", Receipt: "test-receipt", Count: 2})
	if err != nil || removed != 2 {
		t.Fatalf("RemoveReportReceipts() = %d, %v; want 2", removed, err)
	}
	if got, want := (*emitted)[3:], []string{
		"ActivateTab:receipts-tab",
		"UpdateLastSelectedControl:details", "Click:remove", "Click:yes",
		"UpdateLastSelectedControl:details", "Click:remove", "Click:yes",
		"Click:save-and-close",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted commands = %v, want %v", got, want)
	}
}

func TestRemoveReportReceiptsRefusesAttachedOrMiscountedReceipts(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		status string
		count  int
		want   string
	}{
		"attached": {"Attached to expense", 2, "attached to an expense line"},
		"count":    {"", 3, `has 2 receipts named "test-receipt", not 3`},
	} {
		t.Run(name, func(t *testing.T) {
			server, emitted := scriptedServer(t, receiptRemoveSteps(test.status)[:4])
			client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))
			_, err := client.RemoveReportReceipts(context.Background(), expense.RemoveReportReceiptsRequest{ReportNumber: "ER-0002", Receipt: "test-receipt", Count: test.count})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("RemoveReportReceipts() error = %v, want %q", err, test.want)
			}
			if last := (*emitted)[len(*emitted)-1]; last != "ActivateTab:receipts-tab" {
				t.Fatalf("last command = %s, want no Remove", last)
			}
		})
	}
}
