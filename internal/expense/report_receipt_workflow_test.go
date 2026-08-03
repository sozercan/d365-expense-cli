package expense_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/dynamics"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

const combinedReportNumber = "RPT-NEW-0042"

type combinedWorkflowOptions struct {
	createStatus               string
	includeCreateReceiptButton bool
	includeActivatedButton     bool
	activatedStatus            string
	postReceiptStatus          string
	afterReceiptCount          int
	activatedSubmitButton      map[string]any
	confirmationSubmitButton   map[string]any
}

type combinedWorkflowObservation struct {
	requestCount int
	commands     []string
	uploadNames  []string
	uploadValues map[string]string
	uploaded     []byte
	finalSave    bool
}

func draftReportWithReceiptRequest(purpose, notes string, contract expense.ReceiptUploadContract, receipt expense.ReceiptInput) expense.CreateReportWithReceiptsRequest {
	return expense.CreateReportWithReceiptsRequest{
		Purpose:        purpose,
		UploadContract: contract,
		FinalAction:    expense.ReportFinalActionSaveDraft,
		Receipts: []expense.CreateReportReceiptInput{{
			Notes:   notes,
			Receipt: receipt,
		}},
	}
}

func TestCreateReportWithReceiptUsesOneFreshSessionAndDynamicControls(t *testing.T) {
	fileBytes := []byte("\x89PNG\r\n\x1a\nsynthetic-receipt")
	server, observed := newCombinedWorkflowServer(t, combinedWorkflowOptions{
		createStatus:           "Draft",
		includeActivatedButton: true,
		activatedSubmitButton: map[string]any{
			"Id": "activated-tenant-submit", "Name": dynamics.ControlSubmitButton, "TypeName": "Button",
		},
		postReceiptStatus: "Draft",
		afterReceiptCount: 1,
	}, fileBytes)
	defer server.Close()

	client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	// Deliberately provide no receipt-session state. The adapter must consume
	// only this profile's non-secret Upload member.
	receiptProfile := &capture.ReceiptProfile{Upload: validReceiptProfile("https://unused.invalid").Upload}
	contract, err := expense.ReceiptUploadContractFromProfile(receiptProfile)
	if err != nil {
		t.Fatalf("ReceiptUploadContractFromProfile() error = %v", err)
	}

	opened := 0
	request := draftReportWithReceiptRequest(
		"CLI draft with test receipt",
		"Test receipt",
		contract,
		expense.ReceiptInput{
			Filename:  "test-receipt.png",
			MediaType: "image/png",
			Size:      int64(len(fileBytes)),
			Open: func() (io.ReadCloser, error) {
				opened++
				return io.NopCloser(bytes.NewReader(fileBytes)), nil
			},
		},
	)

	plan, err := client.PlanCreateReportWithReceipts(request)
	if err != nil {
		t.Fatalf("PlanCreateReportWithReceipts() error = %v", err)
	}
	if plan.Purpose != request.Purpose || plan.Receipts[0].Receipt.Filename != request.Receipts[0].Receipt.Filename || plan.RequestCount != 12 {
		t.Fatalf("plan = %#v", plan)
	}
	if opened != 0 || observed.requestCount != 0 {
		t.Fatalf("plan opened receipt or sent network request: opened=%d requests=%d", opened, observed.requestCount)
	}
	planText := fmt.Sprintf("%#v", plan)
	for _, secret := range []string{"unit-header-secret", "unit-cookie-secret", server.URL, "fresh-runtime-token", "uploaded-file-id"} {
		if strings.Contains(planText, secret) {
			t.Fatalf("plan exposed %q: %s", secret, planText)
		}
	}

	result, err := client.CreateReportWithReceipts(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateReportWithReceipts() error = %v", err)
	}
	want := expense.CreateReportWithReceiptsResult{
		Purpose:            request.Purpose,
		ReportNumber:       combinedReportNumber,
		Status:             "Draft",
		ReceiptCountBefore: 0,
		ReceiptCountAfter:  1,
		Receipts: []expense.CreateReportReceiptResult{{
			Attached:          expense.AttachedReceipt{Filename: "test-receipt.png", Size: int64(len(fileBytes))},
			ReceiptCountAfter: 1,
		}},
		SavedAndClosed: true,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("CreateReportWithReceipts() = %#v, want %#v", result, want)
	}
	if opened != 1 || observed.requestCount != 12 || !observed.finalSave {
		t.Fatalf("opened=%d requests=%d finalSave=%t", opened, observed.requestCount, observed.finalSave)
	}
	if !bytes.Equal(observed.uploaded, fileBytes) {
		t.Fatalf("uploaded bytes = %q, want %q", observed.uploaded, fileBytes)
	}
	wantUploadNames := []string{"clientId", "maxChunkSize", "tableid", "recid", "companyid", "accesstoken", "notes", "docuname", "docutypeid", "ischunked", "docuRefRecId", "files[]"}
	if !reflect.DeepEqual(observed.uploadNames, wantUploadNames) {
		t.Fatalf("multipart fields = %v, want %v", observed.uploadNames, wantUploadNames)
	}
	for name, wantValue := range map[string]string{
		"maxChunkSize": "1024000", "tableid": "23090", "recid": "5648071694",
		"companyid": "USMF", "accesstoken": "fresh-runtime-token", "notes": "Test receipt",
		"docuname": "test-receipt", "docutypeid": "File", "ischunked": "false", "docuRefRecId": "0",
	} {
		if got := observed.uploadValues[name]; got != wantValue {
			t.Fatalf("multipart %s = %q, want %q", name, got, wantValue)
		}
	}

	wantCommands := []string{
		"UpdateLastSelectedControl:captured-workspace-root", "Click:captured-new-button",
		"SetValue:new-purpose", "ExecuteShortcuts:new-dialog",
		"ActivateTab:new-receipts-tab",
		"UpdateLastSelectedControl:new-details", "Click:activated-new-receipt",
		"Click:preflight-close",
		"UpdateLastSelectedControl:new-details", "Click:activated-new-receipt",
		"CheckFile:receipt-upload", "CheckFile:receipt-upload", "CloseDialog:receipt-upload",
		"Click:receipt-ok", "Click:save-after-receipt",
	}
	if !reflect.DeepEqual(observed.commands, wantCommands) {
		t.Fatalf("commands = %v, want %v", observed.commands, wantCommands)
	}
	for _, command := range observed.commands {
		if strings.Contains(strings.ToLower(command), "submit") {
			t.Fatalf("forbidden command emitted: %s", command)
		}
	}
	resultText := fmt.Sprintf("%#v", result)
	for _, secret := range []string{"fresh-runtime-token", "uploaded-file-id"} {
		if strings.Contains(resultText, secret) {
			t.Fatalf("result exposed %q: %s", secret, resultText)
		}
	}
}

func TestCreateReportWithReceiptDraftIgnoresIrrelevantSubmitButtonMetadata(t *testing.T) {
	fileBytes := []byte("\x89PNG\r\n\x1a\ntenant-submit-metadata")
	server, observed := newCombinedWorkflowServer(t, combinedWorkflowOptions{
		createStatus:           "Draft",
		includeActivatedButton: true,
		postReceiptStatus:      "Draft",
		afterReceiptCount:      1,
		confirmationSubmitButton: map[string]any{
			"Id": "tenant-submit", "Name": dynamics.ControlSubmitButton, "TypeName": "Button",
		},
	}, fileBytes)
	defer server.Close()

	client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	contract, err := expense.ReceiptUploadContractFromProfile(&capture.ReceiptProfile{
		Upload: validReceiptProfile("https://unused.invalid").Upload,
	})
	if err != nil {
		t.Fatalf("ReceiptUploadContractFromProfile() error = %v", err)
	}
	request := draftReportWithReceiptRequest(
		"Draft with tenant-specific submit metadata",
		"Test receipt",
		contract,
		expense.ReceiptInput{
			Filename: "receipt.png", MediaType: "image/png", Size: int64(len(fileBytes)),
			Open: func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(fileBytes)), nil
			},
		},
	)

	result, err := client.CreateReportWithReceipts(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateReportWithReceipts() error = %v", err)
	}
	if !result.SavedAndClosed || result.Submitted || result.Status != "Draft" || result.ReceiptCountAfter != 1 {
		t.Fatalf("CreateReportWithReceipts() = %#v", result)
	}
	if observed.requestCount != 12 || !observed.finalSave {
		t.Fatalf("requests=%d finalSave=%t", observed.requestCount, observed.finalSave)
	}
}

func TestCreateReportWithReceiptRequiresDraftAndActivatedNewReceiptButton(t *testing.T) {
	fileBytes := []byte("\x89PNG\r\n\x1a\nfixture")
	tests := []struct {
		name         string
		options      combinedWorkflowOptions
		wantError    string
		wantRequests int
	}{
		{
			name: "new report is not Draft",
			options: combinedWorkflowOptions{
				createStatus: "Submitted", includeActivatedButton: true,
				postReceiptStatus: "Draft", afterReceiptCount: 1,
			},
			wantError: "status is not Draft", wantRequests: 2,
		},
		{
			name: "NewReceiptButton exists only in stale create response",
			options: combinedWorkflowOptions{
				createStatus: "Draft", includeCreateReceiptButton: true, includeActivatedButton: false,
				postReceiptStatus: "Draft", afterReceiptCount: 1,
			},
			wantError: "activated Receipts tab response lacks NewReceiptButton", wantRequests: 3,
		},
		{
			name: "activated tab reports non-Draft state",
			options: combinedWorkflowOptions{
				createStatus: "Draft", includeActivatedButton: true, activatedStatus: "Submitted",
				postReceiptStatus: "Draft", afterReceiptCount: 1,
			},
			wantError: "status is not Draft", wantRequests: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, observed := newCombinedWorkflowServer(t, test.options, fileBytes)
			defer server.Close()
			client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			contract, err := expense.ReceiptUploadContractFromProfile(&capture.ReceiptProfile{Upload: validReceiptProfile("https://unused.invalid").Upload})
			if err != nil {
				t.Fatal(err)
			}
			opened := 0
			request := draftReportWithReceiptRequest("Draft invariant test", "", contract,
				expense.ReceiptInput{Filename: "receipt.png", MediaType: "image/png", Size: int64(len(fileBytes)), Open: func() (io.ReadCloser, error) {
					opened++
					return io.NopCloser(bytes.NewReader(fileBytes)), nil
				}})
			_, err = client.CreateReportWithReceipts(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
			if observed.requestCount != test.wantRequests || opened != 0 || observed.finalSave {
				t.Fatalf("requests=%d opened=%d finalSave=%t", observed.requestCount, opened, observed.finalSave)
			}
		})
	}
}

func TestCreateReportWithReceiptDoesNotSaveUntilDraftCountIncreaseIsVerified(t *testing.T) {
	fileBytes := []byte("\x89PNG\r\n\x1a\nfixture")
	tests := []struct {
		name       string
		status     string
		afterCount int
		wantError  string
	}{
		{name: "status changed", status: "Submitted", afterCount: 1, wantError: "status is not Draft"},
		{name: "count did not increase", status: "Draft", afterCount: 0, wantError: "receipt count did not increase"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, observed := newCombinedWorkflowServer(t, combinedWorkflowOptions{
				createStatus: "Draft", includeActivatedButton: true,
				postReceiptStatus: test.status, afterReceiptCount: test.afterCount,
			}, fileBytes)
			defer server.Close()
			client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			contract, err := expense.ReceiptUploadContractFromProfile(&capture.ReceiptProfile{Upload: validReceiptProfile("https://unused.invalid").Upload})
			if err != nil {
				t.Fatal(err)
			}
			request := draftReportWithReceiptRequest("Post-attachment invariant test", "", contract,
				expense.ReceiptInput{Filename: "receipt.png", MediaType: "image/png", Size: int64(len(fileBytes)), Open: func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(fileBytes)), nil
				}})
			_, err = client.CreateReportWithReceipts(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
			if observed.requestCount != 11 || observed.finalSave {
				t.Fatalf("requests=%d finalSave=%t; SaveAndClose must not be sent", observed.requestCount, observed.finalSave)
			}
			for _, command := range observed.commands {
				if command == "Click:save-after-receipt" {
					t.Fatalf("SaveAndClose emitted before invariants passed: %v", observed.commands)
				}
			}
		})
	}
}

func TestCreateReportWithReceiptRejectsInsufficientSequenceHeadroomBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()

	profile := validProfile(server.URL)
	profile.Session.NextClientSequence = math.MaxInt64 - 12
	client, err := expense.New(profile, expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := expense.ReceiptUploadContractFromProfile(&capture.ReceiptProfile{Upload: validReceiptProfile("https://unused.invalid").Upload})
	if err != nil {
		t.Fatal(err)
	}
	request := draftReportWithReceiptRequest("Sequence headroom test", "", contract,
		expense.ReceiptInput{
			Filename: "receipt.png", MediaType: "image/png", Size: 1,
			Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil },
		})
	_, err = client.CreateReportWithReceipts(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "headroom") {
		t.Fatalf("error = %v, want headroom error", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestReceiptUploadContractFromProfileValidatesOnlyUploadContract(t *testing.T) {
	validUpload := validReceiptProfile("https://unused.invalid").Upload
	if _, err := expense.ReceiptUploadContractFromProfile(&capture.ReceiptProfile{Upload: validUpload}); err != nil {
		t.Fatalf("contract rejected without receipt session state: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*capture.ReceiptUploadProfile)
	}{
		{name: "endpoint", mutate: func(profile *capture.ReceiptUploadProfile) { profile.EndpointPath = "/other" }},
		{name: "field order", mutate: func(profile *capture.ReceiptUploadProfile) {
			profile.MultipartFieldOrder[0], profile.MultipartFieldOrder[1] = profile.MultipartFieldOrder[1], profile.MultipartFieldOrder[0]
		}},
		{name: "max size", mutate: func(profile *capture.ReceiptUploadProfile) { profile.MaxSupportedSingleFileSize-- }},
		{name: "document type", mutate: func(profile *capture.ReceiptUploadProfile) { profile.DocumentType = "URL" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := validUpload
			profile.MultipartFieldOrder = append([]string(nil), validUpload.MultipartFieldOrder...)
			test.mutate(&profile)
			if _, err := expense.ReceiptUploadContractFromProfile(&capture.ReceiptProfile{Upload: profile}); err == nil {
				t.Fatal("invalid upload contract was accepted")
			}
		})
	}
}

func newCombinedWorkflowServer(t *testing.T, options combinedWorkflowOptions, fileBytes []byte) (*httptest.Server, *combinedWorkflowObservation) {
	t.Helper()
	observed := &combinedWorkflowObservation{uploadValues: make(map[string]string)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed.requestCount++
		stage := observed.requestCount

		if r.Method != http.MethodPost {
			t.Errorf("stage %d method = %s, want POST", stage, r.Method)
		}
		if got, want := r.Header.Get("Origin"), "https://"+r.Host; got != want {
			t.Errorf("stage %d Origin = %q, want %q", stage, got, want)
		}
		for _, name := range []string{"ms-dyn-aid", "ms-dyn-bsid", "ms-dyn-csrftoken", "ms-dyn-sid"} {
			if got := r.Header.Get(name); got != "unit-header-secret" {
				t.Errorf("stage %d header %s = %q; fresh create session was not used", stage, name, got)
			}
		}
		cookies := map[string]string{}
		for _, cookie := range r.Cookies() {
			cookies[cookie.Name] = cookie.Value
		}
		for _, name := range []string{"ms-dyn-csrftoken", "DynamicsOwinAuth"} {
			if cookies[name] != "unit-cookie-secret" {
				t.Errorf("stage %d cookie %s = %q; fresh create session was not used", stage, name, cookies[name])
			}
		}

		if r.URL.Path == "/filemanagement" {
			if stage != 9 {
				t.Errorf("upload stage = %d, want 9", stage)
			}
			readCombinedUpload(t, r, observed)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"fileId":"uploaded-file-id"}]`)
			return
		}
		if r.URL.Path != processMessagesPath || r.URL.Query().Get("cmp") != "USMF" || r.URL.Query().Get("lng") != "en-us" {
			t.Errorf("stage %d URL = %s", stage, r.URL.RequestURI())
		}

		var request dynamics.Envelope
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("stage %d decode request: %v", stage, err)
			return
		}
		appendCombinedCommands(t, observed, request)
		assertCombinedSequences(t, stage, request)
		last := request.Messages[len(request.Messages)-1].SequenceNumber
		w.Header().Set("Content-Type", "application/json")

		switch stage {
		case 1:
			commands := mustCommands(t, request.Messages[0])
			if len(commands) != 2 || commands[1].RootID != "captured-workspace-root" || commands[1].TargetID != "captured-new-button" {
				t.Errorf("open commands = %#v", commands)
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 51,
				viewModelInteraction(map[string]any{
					"Id": "new-dialog", "Name": dynamics.FormExpenseNewExpenseReport, "TypeName": "Dialog",
					"ChildViewModels": []any{map[string]any{"Id": "new-purpose", "Name": dynamics.ControlNamePurpose, "TypeName": "Input"}},
				}),
			))
		case 2:
			children := []any{
				map[string]any{"Id": "new-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "0"}},
				map[string]any{"Id": "save-created", "Name": dynamics.ControlSaveAndClose, "TypeName": "CommandButton"},
				map[string]any{"Id": "new-receipts-tab", "Name": dynamics.ControlReceiptsTabPage, "TypeName": "PivotItem"},
			}
			if options.includeCreateReceiptButton {
				children = append(children, map[string]any{"Id": "stale-create-new-receipt", "Name": dynamics.ControlNewReceiptButton, "TypeName": "MenuItemButton"})
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 52,
				viewModelInteraction(map[string]any{
					"Id": "new-details", "Name": dynamics.FormExpenseReportDetails, "TypeName": "Form",
					"ChildModelCollections": map[string]any{"TrvExpTable_ds": map[string]any{
						"ActiveRecordIndex": 0,
						"Items": []any{map[string]any{"Item": map[string]any{"Id": "new-record", "Properties": map[string]any{
							"dataSourceName_internal": "TrvExpTable_ds", "ExpNumber_field": combinedReportNumber,
							"expenseReportStatus_dataMethod": options.createStatus,
						}}}},
					}},
					"ChildViewModels": children,
				}),
			))
		case 3:
			assertActivateTabRequest(t, request, "new-details", "new-receipts-tab")
			tabChildren := []any{}
			if options.includeActivatedButton {
				tabChildren = append(tabChildren, map[string]any{"Id": "activated-new-receipt", "Name": dynamics.ControlNewReceiptButton, "TypeName": "MenuItemButton"})
			}
			interactions := []json.RawMessage{
				receiptViewModel("new-details", map[string]any{
					"Id": "new-receipts-tab", "Name": dynamics.ControlReceiptsTabPage, "TypeName": "PivotItem", "ChildViewModels": tabChildren,
				}),
				receiptViewModel("new-details", map[string]any{"Id": "activated-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "0"}}),
			}
			if options.activatedStatus != "" {
				interactions = append(interactions, mustRaw(map[string]any{
					"$type": "UpdateModelInteraction", "RootId": "new-details",
					"Descriptor": map[string]any{"Id": "activated-record", "Properties": map[string]any{
						"ExpNumber_field": combinedReportNumber, "expenseReportStatus_dataMethod": options.activatedStatus,
					}},
				}))
			}
			if options.activatedSubmitButton != nil {
				interactions = append(interactions, receiptViewModel("new-details", options.activatedSubmitButton))
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 53, interactions...))
		case 4:
			assertOpenReceiptRequest(t, request, "new-details", "activated-new-receipt")
			writeEnvelope(t, w, responseEnvelope(7, last, 54,
				receiptViewModel("new-details", map[string]any{"Id": "preflight-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "0"}}),
				receiptViewModel("preflight-dialog", map[string]any{
					"Id": "preflight-dialog", "Name": dynamics.FormExpenseAddNewReceipt, "TypeName": "Dialog",
					"ValueProperties": map[string]any{"ParentTitleFields": "User : " + combinedReportNumber},
					"ChildViewModels": []any{map[string]any{"Id": "preflight-close", "Name": dynamics.ControlCloseButtonAddNewTabPage, "TypeName": "CommandButton"}},
				}),
			))
		case 5:
			assertSingleClick(t, request, "preflight-dialog", "preflight-close")
			writeEnvelope(t, w, responseEnvelope(7, last, 55,
				receiptViewModel("new-details", map[string]any{"Id": "closed-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "0"}}),
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "new-details", "Descriptor": map[string]any{"Id": "closed-record", "Properties": map[string]any{
					"ExpNumber_field": combinedReportNumber, "expenseReportStatus_dataMethod": "Draft",
				}}}),
			))
		case 6:
			assertOpenReceiptRequest(t, request, "new-details", "activated-new-receipt")
			writeEnvelope(t, w, responseEnvelope(7, last, 56,
				receiptViewModel("new-details", map[string]any{"Id": "reopen-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "0"}}),
				receiptViewModel("receipt-dialog", map[string]any{
					"Id": "receipt-dialog", "Name": dynamics.FormExpenseAddNewReceipt, "TypeName": "Dialog",
					"ValueProperties": map[string]any{"ParentTitleFields": "User : " + combinedReportNumber},
					"ChildViewModels": []any{
						map[string]any{
							"Id": "receipt-upload", "Name": dynamics.ControlUploadControl, "TypeName": "DocumentUpload",
							"ValueProperties":           map[string]any{"AccessToken": "fresh-runtime-token", "CurrentRecId": "5648071694", "CurrentDocuRefRecId": "0", "SelectedDocumentType": "File"},
							"SerializedValueProperties": map[string]any{"CurrentTableId": "23090"},
						},
						map[string]any{"Id": "receipt-ok", "Name": dynamics.ControlOKButtonAddNewTabPage, "TypeName": "CommandButton"},
						map[string]any{"Id": "receipt-close", "Name": dynamics.ControlCloseButtonAddNewTabPage, "TypeName": "CommandButton"},
					},
				}),
			))
		case 7:
			property, err := dynamics.ParsePropertyChangeInteraction(request.Messages[0].Interactions[0])
			if err != nil || property.PropertyName != dynamics.PropertyDocuName || property.RootID != "receipt-dialog" || property.TargetID != "receipt-upload" || property.NewValue != "test-receipt" && property.NewValue != "receipt" {
				t.Errorf("DocuName property = %#v, err=%v", property, err)
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 57))
		case 8:
			writeEnvelope(t, w, responseEnvelope(7, last, 58))
		case 10:
			property, err := dynamics.ParsePropertyChangeInteraction(request.Messages[0].Interactions[0])
			if err != nil || property.PropertyName != dynamics.PropertyUploadedFileID || property.NewValue != "uploaded-file-id" {
				t.Errorf("UploadedFileId property = %#v, err=%v", property, err)
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 59))
		case 11:
			assertSingleClick(t, request, "receipt-dialog", "receipt-ok")
			interactions := []json.RawMessage{
				receiptViewModel("new-details", map[string]any{"Id": "after-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": fmt.Sprint(options.afterReceiptCount)}}),
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "new-details", "Descriptor": map[string]any{"Id": "after-record", "Properties": map[string]any{
					"ExpNumber_field": combinedReportNumber, "expenseReportStatus_dataMethod": options.postReceiptStatus,
				}}}),
				receiptViewModel("new-details", map[string]any{"Id": "save-after-receipt", "Name": dynamics.ControlSaveAndClose, "TypeName": "CommandButton"}),
			}
			if options.confirmationSubmitButton != nil {
				interactions = append(interactions, receiptViewModel("new-details", options.confirmationSubmitButton))
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 60, interactions...))
		case 12:
			assertSingleClick(t, request, "new-details", "save-after-receipt")
			observed.finalSave = true
			writeEnvelope(t, w, responseEnvelope(7, last, 61,
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "workspace", "Descriptor": map[string]any{"Id": "saved-row", "Properties": map[string]any{"ExpNumber_field": combinedReportNumber}}}),
			))
		default:
			t.Errorf("unexpected request stage %d", stage)
			http.Error(w, "unexpected stage", http.StatusInternalServerError)
		}
	}))
	return server, observed
}

func appendCombinedCommands(t *testing.T, observed *combinedWorkflowObservation, request dynamics.Envelope) {
	t.Helper()
	for _, message := range request.Messages {
		for _, raw := range message.Interactions {
			var header struct {
				Type        string `json:"$type"`
				CommandName string `json:"CommandName"`
				TargetID    string `json:"TargetId"`
			}
			if err := json.Unmarshal(raw, &header); err != nil {
				t.Errorf("decode interaction header: %v", err)
				continue
			}
			if header.Type != dynamics.InteractionTypeCommand || header.CommandName == "" {
				continue
			}
			observed.commands = append(observed.commands, header.CommandName+":"+header.TargetID)
			if strings.Contains(strings.ToLower(header.CommandName), "submit") {
				t.Errorf("forbidden command emitted: %s", header.CommandName)
			}
		}
	}
}

func assertCombinedSequences(t *testing.T, stage int, request dynamics.Envelope) {
	t.Helper()
	type expectation struct {
		ack       int64
		sequences []int64
	}
	expected := map[int]expectation{
		1:  {ack: 50, sequences: []int64{80}},
		2:  {ack: 51, sequences: []int64{81, 82}},
		3:  {ack: 52, sequences: []int64{83}},
		4:  {ack: 53, sequences: []int64{84}},
		5:  {ack: 54, sequences: []int64{85}},
		6:  {ack: 55, sequences: []int64{86}},
		7:  {ack: 56, sequences: []int64{87, 88}},
		8:  {ack: 57, sequences: []int64{89}},
		10: {ack: 58, sequences: []int64{90}},
		11: {ack: 59, sequences: []int64{91}},
		12: {ack: 60, sequences: []int64{92}},
	}
	want, ok := expected[stage]
	if !ok {
		t.Fatalf("no sequence expectation for stage %d", stage)
	}
	gotSequences := make([]int64, len(request.Messages))
	for index, message := range request.Messages {
		gotSequences[index] = message.SequenceNumber
	}
	if request.LastAcknowledgedSequenceNumber != want.ack || !reflect.DeepEqual(gotSequences, want.sequences) {
		t.Errorf("stage %d ack/sequences = %d/%v, want %d/%v", stage, request.LastAcknowledgedSequenceNumber, gotSequences, want.ack, want.sequences)
	}
}

func assertActivateTabRequest(t *testing.T, request dynamics.Envelope, rootID, tabID string) {
	t.Helper()
	if len(request.Messages) != 1 || len(request.Messages[0].Interactions) != 1 {
		t.Fatalf("ActivateTab envelope = %#v", request)
	}
	var command map[string]any
	if err := json.Unmarshal(request.Messages[0].Interactions[0], &command); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"$type", "CallbackId", "CommandName", "FailureCallbackId", "NamedParameters", "NoAsyncIncrement", "PositionalParameters", "PriorityPosition", "ResetThrottleTime", "RootId", "TargetId", "Throttle", "ThrottleFirst", "ThrottleId", "ThrottleTimestamp", "ThrottleValue", "Telemetry"}
	gotKeys := make([]string, 0, len(command))
	for key := range command {
		gotKeys = append(gotKeys, key)
	}
	slicesSort(gotKeys)
	slicesSort(wantKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Fatalf("ActivateTab fields = %v, want %v", gotKeys, wantKeys)
	}
	if command["CommandName"] != dynamics.CommandActivateTab || command["RootId"] != rootID || command["TargetId"] != tabID ||
		command["ResetThrottleTime"] != "true" || command["ThrottleFirst"] != "true" || command["ThrottleId"] != rootID+"_TopTabs" ||
		command["Throttle"] != true || command["Telemetry"] != true || command["ThrottleValue"] != float64(300) {
		t.Fatalf("ActivateTab command = %#v", command)
	}
}

func assertOpenReceiptRequest(t *testing.T, request dynamics.Envelope, rootID, buttonID string) {
	t.Helper()
	commands := mustCommands(t, request.Messages[0])
	if len(commands) != 2 || commands[0].CommandName != dynamics.CommandUpdateLastSelectedControl || commands[1].CommandName != dynamics.CommandClick || commands[1].RootID != rootID || commands[1].TargetID != buttonID {
		t.Errorf("open receipt commands = %#v", commands)
	}
}

func assertSingleClick(t *testing.T, request dynamics.Envelope, rootID, targetID string) {
	t.Helper()
	commands := mustCommands(t, request.Messages[0])
	if len(commands) != 1 || commands[0].CommandName != dynamics.CommandClick || commands[0].RootID != rootID || commands[0].TargetID != targetID {
		t.Errorf("click = %#v, want %s/%s", commands, rootID, targetID)
	}
}

func readCombinedUpload(t *testing.T, request *http.Request, observed *combinedWorkflowObservation) {
	t.Helper()
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		t.Fatalf("upload Content-Type = %q, err=%v", request.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(request.Body, parameters["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart(): %v", err)
		}
		observed.uploadNames = append(observed.uploadNames, part.FormName())
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read multipart part: %v", err)
		}
		if part.FileName() != "" {
			if part.FileName() != "test-receipt.png" && part.FileName() != "receipt.png" {
				t.Errorf("uploaded filename = %q", part.FileName())
			}
			if part.Header.Get("Content-Type") != "image/png" {
				t.Errorf("uploaded media type = %q", part.Header.Get("Content-Type"))
			}
			observed.uploaded = data
		} else {
			observed.uploadValues[part.FormName()] = string(data)
		}
	}
}

func slicesSort(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
