package capture_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/capture"
)

func TestParseReceiptExtractsSafeReplayProfile(t *testing.T) {
	profile, err := capture.ParseReceipt(strings.NewReader(validReceiptHAR(t)))
	if err != nil {
		t.Fatalf("ParseReceipt() error = %v", err)
	}

	if got, want := profile.ReportNumber, "RPT-0001"; got != want {
		t.Errorf("ReportNumber = %q, want %q", got, want)
	}
	if got, want := profile.ReportStatus, "Draft"; got != want {
		t.Errorf("ReportStatus = %q, want %q", got, want)
	}
	if got, want := profile.ReceiptCount, 1; got != want {
		t.Errorf("ReceiptCount = %d, want %d", got, want)
	}
	if got, want := profile.DetailsFormRootID, "current-details-root"; got != want {
		t.Errorf("DetailsFormRootID = %q, want %q", got, want)
	}
	if got, want := profile.AddReceipts, (capture.CommandTarget{CommandName: "Click", RootID: "current-details-root", TargetID: "current-add-target", ControlName: "NewReceiptButton"}); got != want {
		t.Errorf("AddReceipts = %#v, want %#v", got, want)
	}
	if got, want := profile.SaveAndClose, (capture.CommandTarget{CommandName: "Click", RootID: "current-details-root", TargetID: "current-save-target", ControlName: "SaveAndClose"}); got != want {
		t.Errorf("SaveAndClose = %#v, want %#v", got, want)
	}
	if got, want := profile.Expected, (capture.ReceiptExpectedNames{
		DetailsForm:         "ExpenseReportDetails_form",
		AddReceiptForm:      "ExpenseAddNewReceipt_form",
		AddReceiptsControl:  "NewReceiptButton",
		UploadControl:       "UploadControl",
		OKControl:           "OkButtonAddNewTabPage",
		ReceiptCountControl: "ReceiptCount",
		SaveAndCloseControl: "SaveAndClose",
	}); got != want {
		t.Errorf("Expected = %#v, want %#v", got, want)
	}
	if got, want := profile.Upload.EndpointPath, "/filemanagement"; got != want {
		t.Errorf("Upload.EndpointPath = %q, want %q", got, want)
	}
	wantFields := []string{"clientId", "maxChunkSize", "tableid", "recid", "companyid", "accesstoken", "notes", "docuname", "docutypeid", "ischunked", "docuRefRecId", "files[]"}
	if !reflect.DeepEqual(profile.Upload.MultipartFieldOrder, wantFields) {
		t.Errorf("Upload.MultipartFieldOrder = %v, want %v", profile.Upload.MultipartFieldOrder, wantFields)
	}
	if got, want := profile.Upload.MaxChunkSize, int64(1024000); got != want {
		t.Errorf("Upload.MaxChunkSize = %d, want %d", got, want)
	}
	if got, want := profile.Upload.MaxSupportedSingleFileSize, int64(1024000); got != want {
		t.Errorf("Upload.MaxSupportedSingleFileSize = %d, want %d", got, want)
	}
	if got, want := profile.Upload.DocumentType, "File"; got != want {
		t.Errorf("Upload.DocumentType = %q, want %q", got, want)
	}
	if got, want := profile.Session.LastServerSequence, int64(12); got != want {
		t.Errorf("LastServerSequence = %d, want %d", got, want)
	}
	if got, want := profile.Session.NextClientSequence, int64(17); got != want {
		t.Errorf("NextClientSequence = %d, want %d", got, want)
	}
	if err := profile.Validate(); err != nil {
		t.Errorf("Validate() error = %v", err)
	}
}

func TestLoadReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.har")
	if err := os.WriteFile(path, []byte(validReceiptHAR(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	profile, err := capture.LoadReceipt(path)
	if err != nil {
		t.Fatalf("LoadReceipt() error = %v", err)
	}
	if profile.ReportNumber != "RPT-0001" {
		t.Errorf("ReportNumber = %q", profile.ReportNumber)
	}
}

func TestReceiptProfileDoesNotRetainUploadSecrets(t *testing.T) {
	profile, err := capture.ParseReceipt(strings.NewReader(validReceiptHAR(t)))
	if err != nil {
		t.Fatal(err)
	}
	representations := []string{fmt.Sprintf("%#v", profile), profile.SafeSummary()}
	for _, representation := range representations {
		for _, secret := range []string{
			receiptUploadAccessSecret,
			receiptUploadClientSecret,
			receiptUploadFileIDSecret,
			receiptBoundarySecret,
			receiptBytesSecret,
		} {
			if strings.Contains(representation, secret) {
				t.Errorf("receipt profile representation retained upload secret %q", secret)
			}
		}
	}
	for _, safeName := range []string{"NewReceiptButton", "UploadControl", "OkButtonAddNewTabPage", "SaveAndClose", "/filemanagement"} {
		if !strings.Contains(profile.SafeSummary(), safeName) {
			t.Errorf("SafeSummary() is missing safe protocol name %q", safeName)
		}
	}
}

func TestParseReceiptRejectsMixedSessions(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "ProcessMessages session",
			mutate: func(document map[string]any) {
				request := harEntries(document)[7].(map[string]any)["request"].(map[string]any)
				setNamedValue(request["headers"].([]any), "ms-dyn-bsid", "different-browser-session")
			},
		},
		{
			name: "upload session",
			mutate: func(document map[string]any) {
				request := harEntries(document)[1].(map[string]any)["request"].(map[string]any)
				setNamedValue(request["headers"].([]any), "ms-dyn-bsid", "different-browser-session")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			har := mutateHAR(t, validReceiptHAR(t), test.mutate)
			_, err := capture.ParseReceipt(strings.NewReader(har))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "session") {
				t.Fatalf("ParseReceipt() error = %v, want coherent-session rejection", err)
			}
			if strings.Contains(err.Error(), "different-browser-session") {
				t.Fatalf("ParseReceipt() error leaked session value: %v", err)
			}
		})
	}
}

func TestParseReceiptRejectsForbiddenAndAmbiguousFlows(t *testing.T) {
	t.Run("forbidden workflow command", func(t *testing.T) {
		har := mutateHAR(t, validReceiptHAR(t), func(document map[string]any) {
			mutateEntryRequestEnvelope(t, document, 0, func(body map[string]any) {
				interactions := body["Messages"].([]any)[0].(map[string]any)["Interactions"].([]any)
				interactions = append(interactions, command("SubmitWorkflow", "old-details-root", "submit-secret-target", "", "", []any{"forbidden-secret"}))
				body["Messages"].([]any)[0].(map[string]any)["Interactions"] = interactions
			})
		})
		_, err := capture.ParseReceipt(strings.NewReader(har))
		if err == nil || !strings.Contains(err.Error(), "forbidden") {
			t.Fatalf("ParseReceipt() error = %v, want forbidden-flow rejection", err)
		}
		if strings.Contains(err.Error(), "forbidden-secret") || strings.Contains(err.Error(), "submit-secret-target") {
			t.Fatalf("ParseReceipt() error leaked command data: %v", err)
		}
	})

	t.Run("multiple uploads", func(t *testing.T) {
		har := mutateHAR(t, validReceiptHAR(t), func(document map[string]any) {
			entries := harEntries(document)
			entries = append(entries[:2], append([]any{cloneJSONValue(t, entries[1])}, entries[2:]...)...)
			document["log"].(map[string]any)["entries"] = entries
		})
		_, err := capture.ParseReceipt(strings.NewReader(har))
		if err == nil || !strings.Contains(err.Error(), "multiple successful upload") {
			t.Fatalf("ParseReceipt() error = %v, want ambiguous-upload rejection", err)
		}
		for _, secret := range []string{receiptUploadAccessSecret, receiptUploadClientSecret, receiptUploadFileIDSecret, receiptBoundarySecret, receiptBytesSecret} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("ParseReceipt() error leaked upload secret %q", secret)
			}
		}
	})
}

func TestParseReceiptRejectsSanitizedCapture(t *testing.T) {
	har := mutateHAR(t, validReceiptHAR(t), func(document map[string]any) {
		forEachProcessRequest(document, func(request map[string]any) {
			for _, name := range []string{"ms-dyn-aid", "ms-dyn-bsid", "ms-dyn-csrftoken", "ms-dyn-sid"} {
				setNamedValue(request["headers"].([]any), name, "<redacted>")
			}
			for _, raw := range request["cookies"].([]any) {
				raw.(map[string]any)["value"] = "<redacted>"
			}
		})
	})
	_, err := capture.ParseReceipt(strings.NewReader(har))
	if !errors.Is(err, capture.ErrCredentials) {
		t.Fatalf("ParseReceipt() error = %v, want errors.Is(ErrCredentials)", err)
	}
	if strings.Contains(err.Error(), receiptSessionHeaderSecret) || strings.Contains(err.Error(), receiptSessionCookieSecret) {
		t.Fatalf("ParseReceipt() error leaked credential value: %v", err)
	}
}

func TestParseReceiptRequiresExactDraftStatus(t *testing.T) {
	har := mutateHAR(t, validReceiptHAR(t), func(document map[string]any) {
		mutateEntryResponseEnvelope(t, document, 7, func(body map[string]any) {
			descriptor := body["Messages"].([]any)[0].(map[string]any)["Interactions"].([]any)[1].(map[string]any)["Descriptor"].(map[string]any)
			descriptor["Properties"].(map[string]any)["expenseReportStatus_dataMethod"] = "Submitted"
		})
	})
	_, err := capture.ParseReceipt(strings.NewReader(har))
	if err == nil || !strings.Contains(err.Error(), "exactly Draft") {
		t.Fatalf("ParseReceipt() error = %v, want exact Draft rejection", err)
	}
	if strings.Contains(err.Error(), "Submitted") {
		t.Fatalf("ParseReceipt() error retained observed status: %v", err)
	}
}

func setNamedValue(values []any, name, value string) {
	for _, raw := range values {
		item := raw.(map[string]any)
		if strings.EqualFold(item["name"].(string), name) {
			item["value"] = value
		}
	}
}
