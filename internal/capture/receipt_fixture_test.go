package capture_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/textproto"
	"testing"
)

const (
	receiptSessionHeaderSecret = "receipt-session-header-secret"
	receiptSessionCookieSecret = "receipt-session-cookie-secret"
	receiptUploadAccessSecret  = "receipt-upload-access-secret"
	receiptUploadClientSecret  = "receipt-upload-client-secret"
	receiptUploadFileIDSecret  = "receipt-upload-file-id-secret"
	receiptBoundarySecret      = "receipt-boundary-secret"
	receiptBytesSecret         = "receipt-bytes-secret"
)

func validReceiptHAR(t *testing.T) string {
	t.Helper()

	endpoint := "https://tenant.operations.dynamics.com/Services/ReliableCommunicationManager.svc/ProcessMessages?cmp=USMF&lng=en-us"
	headers := receiptHeaders(receiptSessionHeaderSecret, receiptSessionCookieSecret)
	cookies := receiptCookies(receiptSessionCookieSecret)

	checkRequest := envelope(7, "USMF", "en-us", 5,
		message(10,
			command("CheckFile", "old-dialog-root", "old-upload-target", "", "", []any{"receipt", "receipt.png"}),
		),
	)
	checkResponse := envelope(7, "", "", 10,
		message(6, updateView("old-dialog-root", map[string]any{
			"Id": "old-upload-target", "Name": "UploadControl", "TypeName": "DocumentUpload",
		})),
	)

	closeRequest := envelope(7, "USMF", "en-us", 6,
		message(11,
			map[string]any{
				"$type":        "PropertyChangeInteraction",
				"RootId":       "old-dialog-root",
				"TargetId":     "old-upload-target",
				"PropertyName": "UploadedFileId",
				"NewValue":     receiptUploadFileIDSecret,
			},
			command("CloseDialog", "old-dialog-root", "old-upload-target", "", "", nil),
		),
	)
	closeResponse := envelope(7, "", "", 11,
		message(7,
			updateView("old-dialog-root", map[string]any{
				"Id": "old-upload-target", "Name": "UploadControl", "TypeName": "DocumentUpload",
				"ValueProperties": map[string]any{"UploadedFileId": receiptUploadFileIDSecret},
			}),
			updateView("old-dialog-root", map[string]any{
				"Id": "old-ok-target", "Name": "OkButtonAddNewTabPage", "TypeName": "CommandButton",
			}),
		),
	)

	okRequest := envelope(7, "USMF", "en-us", 7,
		message(12, command("Click", "old-dialog-root", "old-ok-target", "", "", nil)),
	)
	okResponse := envelope(7, "", "", 12,
		message(8,
			map[string]any{"$type": "DeleteViewModelInteraction", "RootId": "old-dialog-root", "TargetId": "old-dialog-root"},
			updateView("old-details-root", map[string]any{
				"Id": "old-count-target", "Name": "ReceiptCount", "TypeName": "Integer",
				"ValueProperties": map[string]any{"Value": "1"},
			}),
			map[string]any{
				"$type":  "UpdateModelInteraction",
				"RootId": "old-details-root",
				"Descriptor": map[string]any{
					"Id":         "old-report-model",
					"Properties": map[string]any{"expenseReportStatus_dataMethod": "Draft"},
				},
			},
			updateView("old-details-root", map[string]any{
				"Id": "old-details-root", "Name": "ExpenseReportDetails_form", "TypeName": "Form",
				"ChildViewModels": []any{
					map[string]any{"Id": "old-save-target", "Name": "SaveAndClose", "TypeName": "CommandButton"},
				},
			}),
		),
	)

	saveRequest := envelope(7, "USMF", "en-us", 8,
		message(13, command("Click", "old-details-root", "old-save-target", "", "", nil)),
	)
	saveResponse := envelope(7, "", "", 13,
		message(9,
			map[string]any{"$type": "DeleteViewModelInteraction", "RootId": "old-details-root", "TargetId": "old-details-root"},
			map[string]any{
				"$type":  "UpdateModelInteraction",
				"RootId": "workspace-root",
				"Descriptor": map[string]any{
					"Id": "workspace-report-row",
					"Properties": map[string]any{
						"ExpNumber_field":                "RPT-0001",
						"hasReceiptsAttached_dataMethod": "1",
					},
				},
			},
		),
	)

	currentRequest := envelope(7, "USMF", "en-us", 9,
		message(14, command("ExecuteHyperlink", "workspace-root", "report-link", "", "", nil)),
	)
	currentResponse := envelope(7, "", "", 14,
		message(10,
			map[string]any{
				"$type":  "CreateModelInteraction",
				"RootId": "current-details-root",
				"Descriptor": map[string]any{
					"Id": "current-details-root", "Name": "ExpenseReportDetails_form",
					"Properties": map[string]any{"DisplayParameters": "{}"},
					"ChildModelCollections": map[string]any{
						"TrvExpTable_ds": map[string]any{
							"Items": []any{map[string]any{"Item": map[string]any{
								"Properties": map[string]any{"expenseReportStatus_dataMethod": "Draft"},
							}}},
						},
					},
				},
			},
			map[string]any{
				"$type":  "CreateViewModelInteraction",
				"RootId": "current-details-root",
				"Descriptor": map[string]any{
					"Id": "current-details-root", "Name": "ExpenseReportDetails_form", "TypeName": "Form",
					"ValueProperties": map[string]any{"ParentTitleFields": "Fixture User : RPT-0001"},
					"ChildViewModels": []any{
						map[string]any{"Id": "current-count-target", "Name": "ReceiptCount", "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "1"}},
						map[string]any{"Id": "current-add-target", "Name": "NewReceiptButton", "TypeName": "MenuItemButton"},
						map[string]any{"Id": "current-save-target", "Name": "SaveAndClose", "TypeName": "CommandButton"},
					},
				},
			},
		),
	)

	addRequest := envelope(7, "USMF", "en-us", 10,
		message(15, command("Click", "current-details-root", "current-add-target", "", "", nil)),
	)
	addResponse := envelope(7, "", "", 15,
		message(11,
			updateView("current-details-root", map[string]any{"Id": "current-details-root", "Name": "ExpenseReportDetails_form", "TypeName": "Form"}),
			map[string]any{
				"$type":      "CreateModelInteraction",
				"RootId":     "current-dialog-root",
				"Descriptor": map[string]any{"Id": "current-dialog-root", "Name": "ExpenseAddNewReceipt_form"},
			},
			map[string]any{
				"$type":  "CreateViewModelInteraction",
				"RootId": "current-dialog-root",
				"Descriptor": map[string]any{
					"Id": "current-dialog-root", "Name": "ExpenseAddNewReceipt_form", "TypeName": "Dialog",
					"ValueProperties": map[string]any{"ParentTitleFields": "Fixture User : RPT-0001"},
					"ChildViewModels": []any{
						map[string]any{
							"Id": "current-upload-target", "Name": "UploadControl", "TypeName": "DocumentUpload",
							"ValueProperties": map[string]any{"SelectedDocumentType": "File"},
						},
						map[string]any{"Id": "current-ok-target", "Name": "OkButtonAddNewTabPage", "TypeName": "CommandButton"},
						map[string]any{"Id": "current-close-target", "Name": "CloseButtonAddNewTabPage", "TypeName": "CommandButton"},
					},
				},
			},
		),
	)

	cancelRequest := envelope(7, "USMF", "en-us", 11,
		message(16, command("Click", "current-dialog-root", "current-close-target", "", "", nil)),
	)
	cancelResponse := envelope(7, "", "", 16,
		message(12,
			map[string]any{"$type": "DeleteViewModelInteraction", "RootId": "current-dialog-root", "TargetId": "current-dialog-root"},
			map[string]any{
				"$type":  "UpdateModelInteraction",
				"RootId": "current-details-root",
				"Descriptor": map[string]any{
					"Id":         "current-report-model",
					"Properties": map[string]any{"expenseReportStatus_dataMethod": "Draft"},
				},
			},
		),
	)

	document := map[string]any{
		"log": map[string]any{
			"version": "1.2",
			"creator": map[string]any{"name": "synthetic-receipt-test", "version": "1"},
			"entries": []any{
				entry("POST", endpoint, headers, cookies, checkRequest, 200, checkResponse),
				receiptUploadEntry(t, headers, cookies),
				entry("POST", endpoint, headers, cookies, closeRequest, 200, closeResponse),
				entry("POST", endpoint, headers, cookies, okRequest, 200, okResponse),
				entry("POST", endpoint, headers, cookies, saveRequest, 200, saveResponse),
				entry("POST", endpoint, headers, cookies, currentRequest, 200, currentResponse),
				entry("POST", endpoint, headers, cookies, addRequest, 200, addResponse),
				entry("POST", endpoint, headers, cookies, cancelRequest, 200, cancelResponse),
			},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("json.Marshal(receipt fixture): %v", err)
	}
	return string(encoded)
}

func receiptHeaders(headerSecret, cookieSecret string) []any {
	return []any{
		nameValue("Accept", "application/json"),
		nameValue("Content-Type", "application/json; charset=UTF-8"),
		nameValue("Cookie", "ms-dyn-csrftoken="+cookieSecret+"; DynamicsOwinAuth="+cookieSecret),
		nameValue("Origin", "https://tenant.operations.dynamics.com"),
		nameValue("Referer", "https://tenant.operations.dynamics.com/?cmp=USMF"),
		nameValue("User-Agent", "receipt-fixture-agent"),
		nameValue("X-Requested-With", "XMLHttpRequest"),
		nameValue("ms-dyn-aid", headerSecret),
		nameValue("ms-dyn-bsid", headerSecret),
		nameValue("ms-dyn-csrftoken", headerSecret),
		nameValue("ms-dyn-sid", headerSecret),
	}
}

func receiptCookies(cookieSecret string) []any {
	return []any{
		map[string]any{"name": "ms-dyn-csrftoken", "value": cookieSecret, "path": "/", "secure": true},
		map[string]any{"name": "DynamicsOwinAuth", "value": cookieSecret, "path": "/", "secure": true, "httpOnly": true},
	}
}

func receiptUploadEntry(t *testing.T, headers, cookies []any) map[string]any {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary(receiptBoundarySecret); err != nil {
		t.Fatal(err)
	}
	fields := []struct{ name, value string }{
		{"clientId", receiptUploadClientSecret},
		{"maxChunkSize", "1024000"},
		{"tableid", "23090"},
		{"recid", "5647982574"},
		{"companyid", "USMF"},
		{"accesstoken", receiptUploadAccessSecret},
		{"notes", ""},
		{"docuname", "receipt"},
		{"docutypeid", "File"},
		{"ischunked", "false"},
		{"docuRefRecId", "0"},
	}
	for _, field := range fields {
		if err := writer.WriteField(field.name, field.value); err != nil {
			t.Fatal(err)
		}
	}
	fileHeader := make(textproto.MIMEHeader)
	fileHeader.Set("Content-Disposition", `form-data; name="files[]"; filename="receipt.png"`)
	fileHeader.Set("Content-Type", "image/png")
	part, err := writer.CreatePart(fileHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(receiptBytesSecret)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	uploadHeaders := cloneJSONValue(t, headers).([]any)
	for _, raw := range uploadHeaders {
		header := raw.(map[string]any)
		if header["name"] == "Content-Type" {
			header["value"] = writer.FormDataContentType()
		}
	}
	result := entry("POST", "https://tenant.operations.dynamics.com/filemanagement", uploadHeaders, cookies, nil, 200, []any{map[string]any{"fileId": receiptUploadFileIDSecret}})
	request := result["request"].(map[string]any)
	request["postData"] = map[string]any{"mimeType": writer.FormDataContentType(), "text": body.String()}
	return result
}

func updateView(root string, descriptor map[string]any) map[string]any {
	return map[string]any{"$type": "UpdateViewModelInteraction", "RootId": root, "Descriptor": descriptor}
}
