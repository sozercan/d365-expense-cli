package dynamics_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func TestDiscoverResponseModelRecursesThroughFormsControlsAndProperties(t *testing.T) {
	fixture := []byte(`{
		"ChannelId":0,
		"LastAcknowledgedSequenceNumber":28,
		"Messages":[{"SequenceNumber":16,"Interactions":[
			{"$type":"CreateModelInteraction","Descriptor":{
				"Id":"dialog-root","Name":"ExpenseNewExpenseReport_form",
				"Properties":{"ForceLegacyGrid":"0"},
				"ChildModelCollections":{}
			}},
			{"$type":"CreateViewModelInteraction","Descriptor":{
				"Id":"dialog-root","Name":"ExpenseNewExpenseReport_form","TypeName":"Dialog",
				"ChildViewModels":[
					{"Id":"group","Name":"Fields","ChildViewModels":[
						{"Id":"purpose-id","Name":"NamePurpose","TypeName":"Input","Properties":{"Binding":"Txt2"}},
						{"Id":"description-id","Name":"Description","TypeName":"MultilineInput"}
					]},
					{"Id":"buttons","Name":"Buttons","ChildViewModels":[
						{"Id":"create-id","Name":"CreateButton","TypeName":"Button"}
					]}
				]
			}},
			{"$type":"CreateViewModelInteraction","FutureWrapper":{"anything":true},"Descriptor":{
				"Id":"details-root","Name":"ExpenseReportDetails_form","TypeName":"Form",
				"ChildModelCollections":{"TrvExpTable_ds":{"Items":[{"Item":{"Id":"record-id","Properties":{
					"dataSourceName_internal":"TrvExpTable_ds",
					"ExpNumber_field":"ER-00042",
					"expenseReportStatus_dataMethod":"Draft"
				}}}]}},
				"ChildViewModels":[{"Id":"pane","Name":"Pane","ChildViewModels":[{"Id":"nested","Name":"Nested","ChildViewModels":[
					{"Id":"save-id","Name":"SaveAndClose","TypeName":"CommandButton"}
				]}]}]
			}}
		]}]
	}`)

	model, err := dynamics.DiscoverResponseModel(fixture)
	if err != nil {
		t.Fatalf("DiscoverResponseModel() error = %v", err)
	}
	for name, wantID := range map[string]string{
		dynamics.FormExpenseNewExpenseReport: "dialog-root",
		dynamics.FormExpenseReportDetails:    "details-root",
	} {
		node, ok := model.FindForm(name)
		if !ok {
			t.Fatalf("FindForm(%q) not found", name)
		}
		if node.ID != wantID || node.RootID != wantID {
			t.Errorf("FindForm(%q) = %#v", name, node)
		}
		var raw map[string]any
		if err := json.Unmarshal(node.Raw, &raw); err != nil {
			t.Fatalf("form Raw invalid: %v", err)
		}
		if _, ok := raw["TypeName"]; !ok {
			t.Errorf("FindForm(%q) did not prefer view-model descriptor", name)
		}
	}

	wantControls := map[string]struct {
		id   string
		root string
	}{
		dynamics.ControlNamePurpose:  {"purpose-id", "dialog-root"},
		dynamics.ControlDescription:  {"description-id", "dialog-root"},
		dynamics.ControlCreateButton: {"create-id", "dialog-root"},
		dynamics.ControlSaveAndClose: {"save-id", "details-root"},
	}
	for name, want := range wantControls {
		node, ok := model.FindControl(name)
		if !ok {
			t.Fatalf("FindControl(%q) not found", name)
		}
		if node.ID != want.id || node.RootID != want.root {
			t.Errorf("FindControl(%q) = %#v, want id=%q root=%q", name, node, want.id, want.root)
		}
		if len(node.Path) == 0 {
			t.Errorf("FindControl(%q) has empty path", name)
		}
	}
	purpose, _ := model.FindControl(dynamics.ControlNamePurpose)
	if got := string(purpose.Properties["Binding"]); got != `"Txt2"` {
		t.Errorf("purpose Binding = %s", got)
	}
	if got, want := model.ReportNumber, "ER-00042"; got != want {
		t.Errorf("ReportNumber = %q, want %q", got, want)
	}
	if got, want := model.Status, "Draft"; got != want {
		t.Errorf("Status = %q, want %q", got, want)
	}
	if got, ok := model.FindModel(dynamics.FormExpenseReportDetails); !ok || got.ID != "details-root" {
		t.Errorf("FindModel(details) = %#v, %v", got, ok)
	}

	envelope, err := dynamics.ParseEnvelope(fixture)
	if err != nil {
		t.Fatal(err)
	}
	fromEnvelope, err := dynamics.DiscoverEnvelopeModel(envelope)
	if err != nil {
		t.Fatalf("DiscoverEnvelopeModel() error = %v", err)
	}
	if !reflect.DeepEqual(fromEnvelope.Forms, model.Forms) || fromEnvelope.ReportNumber != model.ReportNumber || fromEnvelope.Status != model.Status {
		t.Errorf("DiscoverEnvelopeModel() differs from byte discovery")
	}
}

func TestDiscoverResponseModelRequiresUniqueFormRoot(t *testing.T) {
	fixture := []byte(`{
		"Descriptors":[
			{"Id":"workspace-a","Name":"ExpenseWorkspace_form","TypeName":"Form"},
			{"Id":"workspace-b","Name":"ExpenseWorkspace_form","TypeName":"Form"}
		]
	}`)
	model, err := dynamics.DiscoverResponseModel(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if form, ok := model.FindUniqueForm(dynamics.FormExpenseWorkspace); ok || form.ID != "" {
		t.Fatalf("FindUniqueForm() = %#v, %v; want ambiguity", form, ok)
	}

	fixture = []byte(`{
		"Descriptors":[
			{"Id":"workspace","Name":"ExpenseWorkspace_form","TypeName":"Form"},
			{"Id":"workspace","Name":"ExpenseWorkspace_form","TypeName":"Form","ValueProperties":{"Caption":"updated"}}
		]
	}`)
	model, err = dynamics.DiscoverResponseModel(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if form, ok := model.FindUniqueForm(dynamics.FormExpenseWorkspace); !ok || form.ID != "workspace" {
		t.Fatalf("FindUniqueForm() = %#v, %v; want repeated workspace ID", form, ok)
	}
}

func TestDiscoverResponseModelPrefersSubmitButtonInDetailsRoot(t *testing.T) {
	fixture := []byte(`{
		"Descriptors":[
			{"Id":"unrelated-root","Name":"Unrelated_form","TypeName":"Form","ChildViewModels":[
				{"Id":"unrelated-submit","Name":"SubmitButton","TypeName":"Button"}
			]},
			{"Id":"details-root","Name":"ExpenseReportDetails_form","TypeName":"Form","ChildViewModels":[
				{"Id":"details-submit","Name":"SubmitButton","TypeName":"Button"}
			]}
		]
	}`)

	model, err := dynamics.DiscoverResponseModel(fixture)
	if err != nil {
		t.Fatalf("DiscoverResponseModel() error = %v", err)
	}
	details, ok := model.FindForm(dynamics.FormExpenseReportDetails)
	if !ok {
		t.Fatal("details form not discovered")
	}
	button, ok := model.FindControl(dynamics.ControlSubmitButton)
	if !ok {
		t.Fatal("SubmitButton not discovered")
	}
	if button.ID != "details-submit" || button.RootID != details.ID {
		t.Fatalf("FindControl(SubmitButton) = %#v, want details-root candidate", button)
	}

	for rootID, wantID := range map[string]string{
		"unrelated-root": "unrelated-submit",
		"details-root":   "details-submit",
	} {
		button, ok := model.FindControlInRoot(dynamics.ControlSubmitButton, rootID)
		if !ok || button.ID != wantID {
			t.Errorf("FindControlInRoot(SubmitButton, %q) = %#v, %v; want ID %q", rootID, button, ok, wantID)
		}
	}

	receiptModel, err := dynamics.DiscoverReceiptModel(fixture)
	if err != nil {
		t.Fatalf("DiscoverReceiptModel() error = %v", err)
	}
	if receiptModel.SubmitButton.ID != "details-submit" || receiptModel.SubmitButton.RootID != details.ID {
		t.Fatalf("receipt SubmitButton = %#v, want details-root candidate", receiptModel.SubmitButton)
	}
}

func TestDiscoverReceiptModelRetainsSubmitButtonsByRootWithoutDetailsForm(t *testing.T) {
	fixture := []byte(`{
		"Messages":[{"Interactions":[
			{"RootId":"unrelated-root","Descriptor":{"Id":"unrelated-submit","Name":"SubmitButton","TypeName":"Button"}},
			{"RootId":"details-root","Descriptor":{"Id":"details-submit","Name":"SubmitButton","TypeName":"Button"}}
		]}]
	}`)

	model, err := dynamics.DiscoverReceiptModel(fixture)
	if err != nil {
		t.Fatalf("DiscoverReceiptModel() error = %v", err)
	}
	for rootID, wantID := range map[string]string{
		"unrelated-root": "unrelated-submit",
		"details-root":   "details-submit",
	} {
		button, ok := model.FindSubmitButtonInRoot(rootID)
		if !ok || button.ID != wantID || button.RootID != rootID {
			t.Errorf("FindSubmitButtonInRoot(%q) = %#v, %v; want ID %q", rootID, button, ok, wantID)
		}
	}
	if button, ok := model.FindSubmitButtonInRoot("missing-root"); ok || button.ID != "" {
		t.Fatalf("FindSubmitButtonInRoot(missing-root) = %#v, %v", button, ok)
	}
}

func TestDiscoverResponseModelRejectsInvalidOrMultipleJSONValues(t *testing.T) {
	for _, fixture := range [][]byte{[]byte(`{"Messages":`), []byte(`{} {}`)} {
		if _, err := dynamics.DiscoverResponseModel(fixture); err == nil {
			t.Errorf("DiscoverResponseModel(%q) unexpectedly succeeded", fixture)
		}
	}
}

func TestDiscoverResponseModelSelectsActiveExpenseReportRecord(t *testing.T) {
	tests := map[string]string{
		"active record index": `
			"ActiveRecordIndex":1,
			"Items":[
				{"Id":"old_0","Item":{"Id":"old_0","Properties":{"dataSourceName_internal":"TrvExpTable_ds","MasterTrackingId":"old","ExpNumber_field":"ER-OLD","expenseReportStatus_dataMethod":"Submitted"}}},
				{"Id":"new_0","Item":{"Id":"new_0","Properties":{"dataSourceName_internal":"TrvExpTable_ds","MasterTrackingId":"new","ExpNumber_field":"ER-NEW","expenseReportStatus_dataMethod":"Draft"}}}
			]`,
		"active master tracking id": `
			"ActiveRecordIndex":0,
			"ActiveMasterTrackingId":"new",
			"Items":[
				{"Id":"old_0","Item":{"Id":"old_0","Properties":{"dataSourceName_internal":"TrvExpTable_ds","MasterTrackingId":"old","ExpNumber_field":"ER-OLD","expenseReportStatus_dataMethod":"Submitted"}}},
				{"Id":"new_0","Item":{"Id":"new_0","Properties":{"dataSourceName_internal":"TrvExpTable_ds","MasterTrackingId":"new","ExpNumber_field":"ER-NEW","expenseReportStatus_dataMethod":"Draft"}}}
			]`,
	}

	for name, collection := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := []byte(`{
				"Unrelated":{"Id":"workspace","Name":"ExpenseWorkspace_form","ChildModelCollections":{"TrvExpTable_ds":{"Items":[
					{"Item":{"Properties":{"dataSourceName_internal":"TrvExpTable_ds","ExpNumber_field":"ER-UNRELATED","expenseReportStatus_dataMethod":"Approved"}}}
				]}}},
				"Details":{"Id":"details","Name":"ExpenseReportDetails_form","ChildModelCollections":{"TrvExpTable_ds":{` + collection + `}}}
			}`)

			model, err := dynamics.DiscoverResponseModel(fixture)
			if err != nil {
				t.Fatalf("DiscoverResponseModel() error = %v", err)
			}
			if got, want := model.ReportNumber, "ER-NEW"; got != want {
				t.Errorf("ReportNumber = %q, want %q", got, want)
			}
			if got, want := model.Status, "Draft"; got != want {
				t.Errorf("Status = %q, want %q", got, want)
			}
		})
	}
}

func TestDiscoverResponseModelUsesDetailsFormTitleWhenActiveRecordOmitsNumber(t *testing.T) {
	response := map[string]any{
		"Messages": []any{map[string]any{
			"SequenceNumber": 16,
			"Interactions": []any{map[string]any{
				"$type": "CreateViewModelInteraction",
				"Descriptor": map[string]any{
					"Id":       "details-root",
					"Name":     dynamics.FormExpenseReportDetails,
					"TypeName": "Form",
					"ValueProperties": map[string]any{
						"ParentTitleFields": "Example Person : D00000000000001",
					},
					"ChildModelCollections": map[string]any{
						"TrvExpTable_ds": map[string]any{
							"Name":              "TrvExpTable_ds",
							"ActiveRecordIndex": 0,
							"Items": []any{map[string]any{"Item": map[string]any{
								"Properties": map[string]any{
									"dataSourceName_internal":        "TrvExpTable_ds",
									"expenseReportStatus_dataMethod": "Draft",
								},
							}}},
						},
					},
				},
			}},
		}},
	}
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	model, err := dynamics.DiscoverResponseModel(data)
	if err != nil {
		t.Fatalf("DiscoverResponseModel() error = %v", err)
	}
	if got, want := model.ReportNumber, "D00000000000001"; got != want {
		t.Fatalf("ReportNumber = %q, want %q", got, want)
	}
	if got, want := model.Status, "Draft"; got != want {
		t.Fatalf("Status = %q, want %q", got, want)
	}
}

func TestDiscoverResponseModelAcceptsUTF8BOM(t *testing.T) {
	data := append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"Id":"form","Name":"ExpenseReportDetails_form","TypeName":"Form"}`)...)
	model, err := dynamics.DiscoverResponseModel(data)
	if err != nil {
		t.Fatalf("DiscoverResponseModel() error = %v", err)
	}
	if _, ok := model.FindForm(dynamics.FormExpenseReportDetails); !ok {
		t.Fatal("details form not discovered")
	}
}
