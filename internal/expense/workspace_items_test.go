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

// unattachedRows returns linked workspace expense rows as Dynamics lists them.
func unattachedRows(active string, rows ...[3]string) []json.RawMessage {
	interactions := []json.RawMessage{mustRaw(map[string]any{"$type": "UpdateModelCollectionInteraction", "RootId": deleteWorkspace, "Descriptor": map[string]any{
		"Name": dynamics.DataSourceExpenseLines, "ActiveMasterTrackingId": active,
	}})}
	for index, row := range rows {
		fields := map[string]any{
			"dataSourceName_internal": dynamics.DataSourceExpenseLines, "MasterTrackingId": row[0], "MerchantId_field": row[1],
			"TransDate_field": "2026-10-10", "CostType_field": "Hotel", "amountWithCurrencyCode_dataMethod": row[2],
		}
		if index > 0 {
			fields["previous_MasterTrackingId"] = rows[index-1][0]
		}
		if index < len(rows)-1 {
			fields["next_MasterTrackingId"] = rows[index+1][0]
		}
		interactions = append(interactions, recordUpdate(deleteWorkspace, fields))
	}
	return interactions
}

func expensesTabStep(rows ...[3]string) scriptedStep {
	return func(dynamics.Envelope) []json.RawMessage {
		return append([]json.RawMessage{
			controlUpdate(deleteWorkspace, control("expenses-grid", dynamics.ControlExpensesGrid)),
			controlUpdate(deleteWorkspace, control("expenses-delete", dynamics.ControlDeleteExpenseButton)),
		}, unattachedRows(rows[0][0], rows...)...)
	}
}

func emptyStep(dynamics.Envelope) []json.RawMessage { return nil }

func TestDeleteUnattachedExpensesDeletesOnlyTheMerchantAndReturnsToReports(t *testing.T) {
	t.Parallel()
	real := [3]string{"57", "Real Hotel", "120.00 USD"}
	test := [3]string{"59", "CLI Test Hotel", "300.00 USD"}
	steps := []scriptedStep{
		expensesTabStep(real, test),
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				mustRaw(map[string]any{"$type": "UpdateModelCollectionInteraction", "RootId": deleteWorkspace, "Descriptor": map[string]any{"Name": dynamics.DataSourceExpenseLines, "ActiveMasterTrackingId": "59"}}),
				recordUpdate(deleteWorkspace, map[string]any{"dataSourceName_internal": dynamics.DataSourceExpenseLines, "MasterTrackingId": "59", "_RESERVED_IsMarked": "true"}),
			}
		},
		confirmStep,
		func(dynamics.Envelope) []json.RawMessage { return []json.RawMessage{deleteViewModel("confirm")} },
		func(dynamics.Envelope) []json.RawMessage { return unattachedRows("57", real) },
		emptyStep,
	}
	server, emitted := scriptedServer(t, steps)
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	removed, err := client.DeleteUnattachedExpenses(context.Background(), "CLI Test Hotel", 1)
	if err != nil || removed != 1 {
		t.Fatalf("DeleteUnattachedExpenses() = %d, %v; want 1", removed, err)
	}
	if got, want := *emitted, []string{
		"ActivateTab:expenses-tab", "ChangeSelectedIndex:expenses-grid",
		"UpdateLastSelectedControl:" + deleteWorkspace, "Click:expenses-delete", "Click:yes",
		"ExecuteShortcuts:" + deleteWorkspace, "ActivateTab:reports-tab",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted commands = %v, want %v", got, want)
	}
}

func TestDeleteUnattachedExpensesRefusesAMiscountBeforeChanges(t *testing.T) {
	t.Parallel()
	server, emitted := scriptedServer(t, []scriptedStep{expensesTabStep([3]string{"57", "CLI Test Hotel", "1.00 USD"}), emptyStep})
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	_, err := client.DeleteUnattachedExpenses(context.Background(), "CLI Test Hotel", 2)
	if err == nil || !strings.Contains(err.Error(), "found 1 unattached expenses") {
		t.Fatalf("DeleteUnattachedExpenses() error = %v, want miscount", err)
	}
	if got, want := *emitted, []string{"ActivateTab:expenses-tab", "ActivateTab:reports-tab"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted commands = %v, want %v", got, want)
	}
}

func TestListUnattachedExpensesFollowsTheLinkedRows(t *testing.T) {
	t.Parallel()
	server, _ := scriptedServer(t, []scriptedStep{expensesTabStep([3]string{"57", "A", "1.00 USD"}, [3]string{"59", "B", "2.00 USD"}), emptyStep})
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	expenses, err := client.ListUnattachedExpenses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(expenses) != 2 || expenses[0].Merchant != "A" || expenses[1].Merchant != "B" || expenses[1].Number != 2 {
		t.Fatalf("ListUnattachedExpenses() = %#v", expenses)
	}
}
