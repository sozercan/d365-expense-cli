package expense_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func receiptCountUpdate(value string) json.RawMessage {
	return receiptViewModel("details", map[string]any{"Id": "count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": value}})
}

func addReceiptDialog(id string, children ...any) json.RawMessage {
	return receiptViewModel(id, map[string]any{
		"Id": id, "Name": dynamics.FormExpenseAddNewReceipt, "TypeName": "Dialog",
		"ValueProperties": map[string]any{"ParentTitleFields": "User : ER-0002"},
		"ChildViewModels": children,
	})
}

// addReceiptSteps mirror the live flow: an existing Draft's unchanged report
// record, and so its status, is not resent while receipts are added.
func addReceiptSteps(afterCount string) []scriptedStep {
	steps := openSteps(draftLine("160", "Ground Transportation", "2026-09-28", "40.00", "EXAMPLE RIDES"))
	open := steps[2]
	steps[2] = func(request dynamics.Envelope) []json.RawMessage {
		return append(open(request), controlUpdate("details", control("receipts-tab", dynamics.ControlReceiptsTabPage)), receiptCountUpdate("8"))
	}
	empty := func(dynamics.Envelope) []json.RawMessage { return nil }
	return append(steps,
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{controlUpdate("details", control("new-receipt", dynamics.ControlNewReceiptButton)), receiptCountUpdate("8")}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{receiptCountUpdate("8"), addReceiptDialog("preflight", control("preflight-close", dynamics.ControlCloseButtonAddNewTabPage))}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{receiptCountUpdate("8")}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{receiptCountUpdate("8"), addReceiptDialog("dialog",
				map[string]any{
					"Id": "upload", "Name": dynamics.ControlUploadControl, "TypeName": "DocumentUpload",
					"ValueProperties":           map[string]any{"AccessToken": "fresh-runtime-token", "CurrentRecId": "5648071694", "CurrentDocuRefRecId": "0", "SelectedDocumentType": "File"},
					"SerializedValueProperties": map[string]any{"CurrentTableId": "23090"},
				},
				control("ok", dynamics.ControlOKButtonAddNewTabPage),
				control("close", dynamics.ControlCloseButtonAddNewTabPage),
			)}
		},
		empty,
		empty,
		empty,
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{receiptCountUpdate(afterCount)}
		},
		workspaceStep,
	)
}

func rideReceipt() expense.CreateReportReceiptInput {
	png := []byte("\x89PNG\r\n\x1a\nunit")
	return expense.CreateReportReceiptInput{Notes: "Ride", Receipt: expense.ReceiptInput{
		Filename: "ride-receipt.png", MediaType: "image/png", Size: int64(len(png)),
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(png)), nil },
	}}
}

func TestAddReportReceiptsUploadsToTheOpenedDraftAndSaves(t *testing.T) {
	t.Parallel()
	server, emitted := scriptedServer(t, addReceiptSteps("9"))
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	result, err := client.AddReportReceipts(context.Background(), expense.AddReportReceiptsRequest{
		ReportNumber: "ER-0002", Receipts: []expense.CreateReportReceiptInput{rideReceipt()}, UploadContract: expense.BuiltinReceiptUploadContract(),
	})
	if err != nil {
		t.Fatalf("AddReportReceipts() error = %v", err)
	}
	if result.ReceiptCountBefore != 8 || result.ReceiptCountAfter != 9 || len(result.Receipts) != 1 {
		t.Fatalf("AddReportReceipts() = %#v", result)
	}
	if got, want := *emitted, []string{
		"ExecuteShortcuts:" + deleteWorkspace, "ChangeSelectedIndex:grid", "ExecuteHyperlink:link",
		"ActivateTab:receipts-tab",
		"UpdateLastSelectedControl:details", "Click:new-receipt", "Click:preflight-close",
		"UpdateLastSelectedControl:details", "Click:new-receipt",
	}; !reflect.DeepEqual(got[:len(want)], want) {
		t.Fatalf("emitted commands = %v, want prefix %v", got, want)
	}
	if got := *emitted; got[len(got)-1] != "Click:save-and-close" || !strings.Contains(strings.Join(got, " "), "upload") {
		t.Fatalf("emitted commands = %v, want an upload and a final save", got)
	}
}

func TestAddReportReceiptsDoesNotSaveWhenTheCountDoesNotIncrease(t *testing.T) {
	t.Parallel()
	server, emitted := scriptedServer(t, addReceiptSteps("8"))
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	_, err := client.AddReportReceipts(context.Background(), expense.AddReportReceiptsRequest{
		ReportNumber: "ER-0002", Receipts: []expense.CreateReportReceiptInput{rideReceipt()}, UploadContract: expense.BuiltinReceiptUploadContract(),
	})
	if err == nil || !strings.Contains(err.Error(), "did not increase by exactly one") {
		t.Fatalf("AddReportReceipts() error = %v", err)
	}
	if got := *emitted; got[len(got)-1] == "Click:save-and-close" {
		t.Fatalf("saved after an unverified upload: %v", got)
	}
}
