package dynamics_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func expenseTargets() dynamics.ExpenseLineCommandTargets {
	return dynamics.ExpenseLineCommandTargets{
		DetailsRootID:      "165_0",
		NewExpenseButtonID: "254_0",
		CardMenuID:         "405_0",
		ItemizeButtonID:    "410_0",
		DialogRootID:       "274_0",
		DialogFieldIDs:     []string{"298_0", "299_0", "301_0"},
		SaveButtonID:       "306_0",
		ItemizeRootID:      "411_0",
		SubcategoryID:      "452_0",
		SubcategoryNameID:  "453_0",
		ItemizeFieldIDs:    []string{"454_0", "455_0", "456_0"},
		ItemizeNewButtonID: "446_0",
		ItemizeCloseID:     "458_0",
	}
}

func TestValidateExpenseLineCommandsAllowsCapturedShapes(t *testing.T) {
	targets := expenseTargets()
	tests := map[string][]dynamics.Message{
		"open new expense": {dynamics.BuildOpenNewExpenseMessage(14, "165_0", "254_0")},
		"set category":     {dynamics.BuildSetValueMessage(15, "274_0", "298_0", "Hotel", "", "")},
		"save":             {dynamics.BuildSaveAndCloseClickMessage(16, "274_0", "306_0")},
		"open card menu":   {dynamics.BuildOpenExpenseCardMenuMessage(17, "165_0", "405_0")},
		"open itemize":     {dynamics.BuildOpenItemizeExpenseMessage(18, "165_0", "410_0")},
		"subcategory":      dynamics.BuildSubcategoryMessages(19, "411_0", "452_0", "453_0", "Daily Room Rate"),
		"daily rate":       {dynamics.BuildSetValueMessage(21, "411_0", "455_0", "125.00", "", "")},
		"new row":          {dynamics.BuildNewItemizationRowMessage(22, "411_0", "446_0")},
		"close itemize":    {dynamics.BuildSaveAndCloseClickMessage(23, "411_0", "458_0")},
	}
	for name, messages := range tests {
		t.Run(name, func(t *testing.T) {
			if err := dynamics.ValidateExpenseLineCommands(dynamics.Envelope{Messages: messages}, targets); err != nil {
				t.Fatalf("ValidateExpenseLineCommands() error = %v", err)
			}
		})
	}
}

func TestValidateExpenseLineCommandsRejectsUnlistedTargetsAndShapes(t *testing.T) {
	targets := expenseTargets()
	submit := dynamics.BuildSaveAndCloseClickMessage(16, "274_0", "306_0")
	command := mustCommands(t, submit)[0]
	command.CommandName = "SubmitToWorkflow"
	submit.Interactions[0] = mustRawCommand(t, command)

	openWithNull := dynamics.BuildOpenItemizeExpenseMessage(18, "165_0", "410_0")
	click := mustCommands(t, openWithNull)[1]
	click.PositionalParameters = nil
	openWithNull.Interactions[1] = mustRawCommand(t, click)

	subcategory := dynamics.BuildSubcategoryMessages(19, "411_0", "452_0", "453_0", "Hotel Tax")
	subcategory[1].SequenceNumber = 30

	tests := map[string][]dynamics.Message{
		"forbidden name":           {submit},
		"report SaveAndClose":      {dynamics.BuildSaveAndCloseClickMessage(16, "165_0", "246_0")},
		"unlisted dialog field":    {dynamics.BuildSetValueMessage(15, "274_0", "303_0", "Card", "", "")},
		"field in wrong root":      {dynamics.BuildSetValueMessage(15, "411_0", "298_0", "Hotel", "", "")},
		"itemize click shape":      {openWithNull},
		"wrong selected control":   {dynamics.BuildOpenNewExpenseReportMessage(14, "165_0", "254_0")},
		"non-consecutive messages": subcategory,
	}
	for name, messages := range tests {
		t.Run(name, func(t *testing.T) {
			err := dynamics.ValidateExpenseLineCommands(dynamics.Envelope{Messages: messages}, targets)
			if !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}

func TestDiscoverDataRecordsTracksRootsAndInserts(t *testing.T) {
	body := []byte(`{"Messages":[{"Interactions":[
		{"$type":"UpdateModelInteraction","RootId":"274_0","Descriptor":{"Properties":{"dataSourceName_internal":"TrvExpTrans_ds","recId_internal":"0","CostType_field":"Hotel"}}},
		{"$type":"UpdateModelCollectionInteraction","RootId":"165_0","Descriptor":{"SpliceOperations":[{"Insert":[{"Item":{"Properties":{"dataSourceName_internal":"TrvExpTrans_ds","recId_internal":"5001","AmountMST_field":300}}}]}]}},
		{"$type":"DeleteViewModelInteraction","TargetId":"274_0","RootId":"274_0"}
	]}]}`)
	records, err := dynamics.DiscoverDataRecords(body, dynamics.DataSourceExpenseLines)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].RootID != "274_0" || records[0].Inserted || records[0].Fields["CostType_field"] != "Hotel" ||
		records[1].RootID != "165_0" || !records[1].Inserted || records[1].RecID != "5001" || records[1].Fields["AmountMST_field"] != "300" {
		t.Fatalf("DiscoverDataRecords() = %#v", records)
	}
	for rootID, want := range map[string]bool{"274_0": true, "165_0": false} {
		if got, err := dynamics.ResponseDeletesViewModel(body, rootID); err != nil || got != want {
			t.Fatalf("ResponseDeletesViewModel(%s) = %t, %v; want %t", rootID, got, err, want)
		}
	}
}

func TestValidateItemizeButtonRequiresCapturedMenuItem(t *testing.T) {
	node := dynamics.ModelNode{ID: "410_0", Name: dynamics.ControlItemizeExpenseButton, RootID: "165_0"}
	if err := dynamics.ValidateItemizeButton(node, "165_0"); err == nil {
		t.Fatal("ValidateItemizeButton() accepted a node without menu-item metadata")
	}
}

func TestDiscoverFullListReadsItemsAndClearCacheReloads(t *testing.T) {
	for name, test := range map[string]struct {
		body string
		want []string
		ok   bool
	}{
		"initial items": {`{"Messages":[{"Interactions":[{"RootId":"167_0","Descriptor":{"Name":"TrvExpTrans_ds","IsLastPage":true,
			"Items":[{"Item":{"Properties":{"MasterTrackingId":"160"}}},{"Item":{"Properties":{"MasterTrackingId":"162"}}}]}}]}]}`, []string{"160", "162"}, true},
		"empty new report": {`{"Messages":[{"Interactions":[{"RootId":"167_0","Descriptor":{"Name":"TrvExpTrans_ds","IsLastPage":false,"Items":[]}}]}]}`, []string{}, true},
		"clear cache reload": {`{"Messages":[{"Interactions":[{"RootId":"167_0","Descriptor":{"Name":"TrvExpTrans_ds","IsLastPage":true,"SpliceOperations":[
			{"ClearCache":true,"Insert":[]},{"Index":0,"Delete":5,"Insert":[{"Item":{"Properties":{"MasterTrackingId":"1058"}}},{"Item":{"Properties":{"MasterTrackingId":"1059"}}}]}]}}]}]}`, []string{"1058", "1059"}, true},
		"incremental insert": {`{"Messages":[{"Interactions":[{"RootId":"167_0","Descriptor":{"Name":"TrvExpTrans_ds","IsLastPage":true,"SpliceOperations":[
			{"Index":3,"Delete":0,"Insert":[{"Item":{"Properties":{"MasterTrackingId":"9"}}}]}]}}]}]}`, nil, false},
		"partial page": {`{"Messages":[{"Interactions":[{"RootId":"167_0","Descriptor":{"Name":"TrvExpTrans_ds","IsLastPage":false,
			"Items":[{"Item":{"Properties":{"MasterTrackingId":"160"}}}]}}]}]}`, []string{"160"}, false},
		"other root": {`{"Messages":[{"Interactions":[{"RootId":"3_0","Descriptor":{"Name":"TrvExpTrans_ds","IsLastPage":true,"Items":[]}}]}]}`, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok, err := dynamics.DiscoverFullList([]byte(test.body), dynamics.DataSourceExpenseLines, "167_0")
			if err != nil {
				t.Fatal(err)
			}
			if ok != test.ok || (test.ok && !reflect.DeepEqual(got, test.want)) {
				t.Fatalf("DiscoverFullList() = %v, %t; want %v, %t", got, ok, test.want, test.ok)
			}
		})
	}
}
