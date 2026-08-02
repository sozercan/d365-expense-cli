package dynamics_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func TestDiscoverReceiptModelExtractsCapturedReceiptFields(t *testing.T) {
	const token = "secret-access-token-must-not-be-logged"
	fixture := []byte(`{
		"Messages":[{"SequenceNumber":1,"Interactions":[
			{"$type":"CreateViewModelInteraction","RootId":"details-root","Descriptor":{
				"Id":"details-root","Name":"ExpenseReportDetails_form","TypeName":"Form",
				"ChildModelCollections":{"TrvExpTable_ds":{"Name":"TrvExpTable_ds","ActiveRecordIndex":0,"Items":[{"Item":{"Properties":{
					"dataSourceName_internal":"TrvExpTable_ds","ExpNumber_field":"ER-00042","expenseReportStatus_dataMethod":"Draft"
				}}}]}},
				"ChildViewModels":[
					{"Id":"receipt-count","Name":"ReceiptCount","TypeName":"Integer","ValueProperties":{"Value":"2"}},
					{"Id":"receipts-tab","Name":"ReceiptsTabPage","TypeName":"PivotItem","ChildViewModels":[{"Id":"new-receipt","Name":"NewReceiptButton","TypeName":"MenuItemButton"}]},
					{"Id":"save-close","Name":"SaveAndClose","TypeName":"CommandButton"}
				]
			}},
			{"$type":"CreateViewModelInteraction","RootId":"dialog-root","Descriptor":{
				"Id":"dialog-root","Name":"ExpenseAddNewReceipt_form","TypeName":"Dialog",
				"ValueProperties":{"ParentTitleFields":"Example Person : ER-00042"},
				"ChildViewModels":[
					{"Id":"upload","Name":"UploadControl","TypeName":"DocumentUpload","ValueProperties":{
						"AccessToken":"` + token + `","CurrentRecId":"5647982574","CurrentDocuRefRecId":"0","SelectedDocumentType":"File"
					},"SerializedValueProperties":{"CurrentTableId":"23090"}},
					{"Id":"ok","Name":"OkButtonAddNewTabPage","TypeName":"CommandButton"},
					{"Id":"close","Name":"CloseButtonAddNewTabPage","TypeName":"CommandButton"}
				]
			}}
		]}]
	}`)

	model, err := dynamics.DiscoverReceiptModel(fixture)
	if err != nil {
		t.Fatalf("DiscoverReceiptModel() error = %v", err)
	}
	if model.AddNewReceiptForm.ID != "dialog-root" || model.UploadControl.ID != "upload" || model.OKButton.ID != "ok" || model.CloseButton.ID != "close" {
		t.Fatalf("dialog discovery = %#v", model)
	}
	if model.AddReceiptButton.ID != "new-receipt" || model.ReceiptsTabPage.ID != "receipts-tab" || model.SaveAndClose.ID != "save-close" {
		t.Fatalf("details controls = %#v", model)
	}
	if model.AccessToken != token || model.CurrentRecID != "5647982574" || model.CurrentDocuRefRecID != "0" || model.CurrentTableID != "23090" || model.SelectedDocumentType != "File" {
		t.Fatalf("upload metadata did not match")
	}
	if model.ReportNumber != "ER-00042" || model.Status != "Draft" || !model.ReceiptCountPresent || model.ReceiptCount != 2 {
		t.Fatalf("report fields = %#v", model)
	}
	targets := model.CommandTargets()
	if targets.DetailsRootID != "details-root" || targets.NewReceiptButtonID != "new-receipt" || targets.DialogRootID != "dialog-root" || targets.UploadControlID != "upload" || targets.OKButtonID != "ok" || targets.CloseButtonID != "close" || targets.SaveAndCloseID != "save-close" {
		t.Fatalf("CommandTargets() = %#v", targets)
	}
	for _, summary := range []string{model.SafeSummary(), fmt.Sprint(model), fmt.Sprintf("%v", model), fmt.Sprintf("%#v", model)} {
		if strings.Contains(summary, token) {
			t.Fatalf("summary leaked AccessToken: %s", summary)
		}
		if !strings.Contains(summary, "access-token-present=true") {
			t.Fatalf("summary omitted token presence marker: %s", summary)
		}
	}
}

func TestDiscoverReceiptModelSupportsAddReceiptsAndReceiptFormReportFallback(t *testing.T) {
	fixture := []byte(`{"RootId":"details","Descriptor":{"Id":"dialog","Name":"ExpenseAddNewReceipt_form","TypeName":"Dialog","ValueProperties":{"ParentTitleFields":"Person : D00000000000001"},"ChildViewModels":[{"Id":"add","Name":"AddReceipts","TypeName":"Button"}]}}`)
	model, err := dynamics.DiscoverReceiptModel(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if model.AddReceiptButton.ID != "add" || model.ReportNumber != "D00000000000001" {
		t.Fatalf("model = %#v", model)
	}
}

func TestMergeReceiptModelsKeepsStageDiscoveriesAndLatestReceiptCount(t *testing.T) {
	merged := dynamics.MergeReceiptModels(
		dynamics.ReceiptModel{AddReceiptButton: dynamics.ModelNode{ID: "add", RootID: "details"}, ReportNumber: "ER-1", ReceiptCount: 1, ReceiptCountPresent: true},
		dynamics.ReceiptModel{AddNewReceiptForm: dynamics.ModelNode{ID: "dialog"}, UploadControl: dynamics.ModelNode{ID: "upload", RootID: "dialog"}, AccessToken: "token", ReceiptCount: 2, ReceiptCountPresent: true},
	)
	if merged.AddReceiptButton.ID != "add" || merged.UploadControl.ID != "upload" || merged.AccessToken != "token" || merged.ReceiptCount != 2 || !merged.ReceiptCountPresent {
		t.Fatalf("merged = %#v", merged)
	}
}

func TestDiscoverReceiptModelErrorsDoNotExposeAccessToken(t *testing.T) {
	const token = "do-not-leak-this-token"
	_, err := dynamics.DiscoverReceiptModel([]byte(`{"AccessToken":"` + token + `"`))
	if err == nil {
		t.Fatal("DiscoverReceiptModel() unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaked AccessToken: %v", err)
	}
}

func TestDiscoverReceiptModelIgnoresStaleReportOutsideReceiptForms(t *testing.T) {
	fixture := []byte(`{
		"Messages":[{"Interactions":[
			{"$type":"UpdateModelInteraction","Descriptor":{"Properties":{"ExpNumber_field":"D00000000000002"}}},
			{"$type":"UpdateModelInteraction","Descriptor":{"Id":"upload","Name":"UploadControl","TypeName":"DocumentUpload"}}
		]}]
	}`)
	model, err := dynamics.DiscoverReceiptModel(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if model.ReportNumber != "" {
		t.Fatalf("ReportNumber = %q, want empty stale report", model.ReportNumber)
	}
}

func TestDiscoverReceiptModelPrefersReceiptDialogReportOverStaleDetails(t *testing.T) {
	fixture := []byte(`{
		"Messages":[{"Interactions":[
			{"$type":"CreateViewModelInteraction","Descriptor":{"Id":"old-details","Name":"ExpenseReportDetails_form","TypeName":"Form","Properties":{"ExpNumber_field":"D00000000000002"}}},
			{"$type":"CreateViewModelInteraction","Descriptor":{"Id":"dialog","Name":"ExpenseAddNewReceipt_form","TypeName":"Dialog","ValueProperties":{"ParentTitleFields":"Person : D00000000000003"}}}
		]}]
	}`)
	model, err := dynamics.DiscoverReceiptModel(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if model.ReportNumber != "D00000000000003" {
		t.Fatalf("ReportNumber = %q, want receipt-dialog report", model.ReportNumber)
	}
}

func TestDiscoverReceiptModelIgnoresStaleStatusOutsideReceiptDetails(t *testing.T) {
	fixture := []byte(`{
		"Messages":[{"Interactions":[
			{"$type":"UpdateModelInteraction","Descriptor":{"Properties":{"expenseReportStatus_dataMethod":"Approved"}}},
			{"$type":"UpdateModelInteraction","Descriptor":{"Id":"upload","Name":"UploadControl","TypeName":"DocumentUpload"}}
		]}]
	}`)
	model, err := dynamics.DiscoverReceiptModel(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if model.Status != "" {
		t.Fatalf("Status = %q, want empty stale status", model.Status)
	}
}
