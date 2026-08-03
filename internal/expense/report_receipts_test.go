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
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/dynamics"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

type multiReceiptServerOptions struct {
	receiptCount                    int
	submit                          bool
	failReceiptIndex                int
	failedReceiptStatus             string
	failedReceiptCount              int
	includeFailedCount              bool
	activatedSubmitButton           map[string]any
	activatedSubmitButtonRootID     string
	activatedCurrentSubmitButton    map[string]any
	confirmationSubmitButton        map[string]any
	confirmationSubmitButtonRootID  string
	confirmationCurrentSubmitButton map[string]any
	omitConfirmationSubmitButton    bool
	expectedSubmitButtonID          string
}

type observedMultiUpload struct {
	filename string
	notes    string
	data     []byte
	names    []string
}

type multiReceiptObservation struct {
	requestCount  int
	activateCount int
	saveCount     int
	submitCount   int
	commands      []string
	openButtons   []string
	uploads       []observedMultiUpload
}

func TestCreateReportWithReceiptsAttachesInOrderAndSavesOnlyAfterAllSucceed(t *testing.T) {
	files := [][]byte{
		[]byte("\x89PNG\r\n\x1a\nfirst-receipt"),
		[]byte("\x89PNG\r\n\x1a\nsecond-receipt"),
	}
	server, observed := newMultiReceiptWorkflowServer(t, multiReceiptServerOptions{receiptCount: 2}, files)
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

	opened := make([]int, len(files))
	request := expense.CreateReportWithReceiptsRequest{
		Purpose:        "Conference travel",
		UploadContract: contract,
		FinalAction:    expense.ReportFinalActionSaveDraft,
		Receipts: []expense.CreateReportReceiptInput{
			{
				Notes: "Ground transport outbound",
				Receipt: expense.ReceiptInput{
					Filename:  "transport-outbound.png",
					MediaType: "image/png",
					Size:      int64(len(files[0])),
					Open: func() (io.ReadCloser, error) {
						opened[0]++
						return io.NopCloser(bytes.NewReader(files[0])), nil
					},
				},
			},
			{
				Notes: "Ground transport return",
				Receipt: expense.ReceiptInput{
					Filename:  "transport-return.png",
					MediaType: "image/png",
					Size:      int64(len(files[1])),
					Open: func() (io.ReadCloser, error) {
						opened[1]++
						return io.NopCloser(bytes.NewReader(files[1])), nil
					},
				},
			},
		},
	}

	plan, err := client.PlanCreateReportWithReceipts(request)
	if err != nil {
		t.Fatalf("PlanCreateReportWithReceipts() error = %v", err)
	}
	if plan.Purpose != request.Purpose || plan.RequestCount != 20 || len(plan.Receipts) != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	if plan.Receipts[0].Receipt.Filename != "transport-outbound.png" || plan.Receipts[1].Receipt.Filename != "transport-return.png" {
		t.Fatalf("plan receipt order = %#v", plan.Receipts)
	}
	if strings.Contains(fmt.Sprintf("%#v", plan), "Ground transport") {
		t.Fatalf("plan exposed arbitrary receipt notes: %#v", plan)
	}
	if !reflect.DeepEqual(opened, []int{0, 0}) || observed.requestCount != 0 {
		t.Fatalf("plan opened files or sent requests: opened=%v requests=%d", opened, observed.requestCount)
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
		ReceiptCountAfter:  2,
		Receipts: []expense.CreateReportReceiptResult{
			{Attached: expense.AttachedReceipt{Filename: "transport-outbound.png", Size: int64(len(files[0]))}, ReceiptCountAfter: 1},
			{Attached: expense.AttachedReceipt{Filename: "transport-return.png", Size: int64(len(files[1]))}, ReceiptCountAfter: 2},
		},
		SavedAndClosed: true,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("CreateReportWithReceipts() = %#v, want %#v", result, want)
	}
	if !reflect.DeepEqual(opened, []int{1, 1}) {
		t.Fatalf("receipt readers opened = %v, want [1 1]", opened)
	}
	if observed.requestCount != 20 || observed.activateCount != 1 || observed.saveCount != 1 {
		t.Fatalf("requests=%d activate=%d save=%d, want 20/1/1", observed.requestCount, observed.activateCount, observed.saveCount)
	}
	if !reflect.DeepEqual(observed.openButtons, []string{
		"new-receipt-1", "new-receipt-1", "new-receipt-2", "new-receipt-2",
	}) {
		t.Fatalf("New receipt buttons = %v", observed.openButtons)
	}
	if len(observed.uploads) != 2 {
		t.Fatalf("uploads = %#v", observed.uploads)
	}
	for index, upload := range observed.uploads {
		if upload.filename != request.Receipts[index].Receipt.Filename ||
			upload.notes != request.Receipts[index].Notes ||
			!bytes.Equal(upload.data, files[index]) {
			t.Fatalf("upload %d = %#v", index+1, upload)
		}
		if !reflect.DeepEqual(upload.names, receiptMultipartFieldOrderForTest()) {
			t.Fatalf("upload %d fields = %v", index+1, upload.names)
		}
	}
	for _, command := range observed.commands {
		if strings.Contains(strings.ToLower(command), "submit") {
			t.Fatalf("forbidden command emitted: %s", command)
		}
	}
}

func TestCreateReportWithReceiptsSubmitsOnlyAfterEveryReceiptSucceeds(t *testing.T) {
	file := []byte("\x89PNG\r\n\x1a\nsubmit-receipt")
	tests := []struct {
		name    string
		options multiReceiptServerOptions
	}{
		{
			name: "activation delta",
			options: multiReceiptServerOptions{
				receiptCount:                1,
				submit:                      true,
				activatedSubmitButton:       submitButtonDescriptor("unrelated-activated-submit"),
				activatedSubmitButtonRootID: "unrelated-activated-details",
				activatedCurrentSubmitButton: map[string]any{
					"Id": "submit-activated-current", "Name": dynamics.ControlSubmitButton,
				},
				omitConfirmationSubmitButton: true,
				expectedSubmitButtonID:       "submit-activated-current",
			},
		},
		{
			name: "confirmation delta",
			options: multiReceiptServerOptions{
				receiptCount:                   1,
				submit:                         true,
				activatedSubmitButton:          submitButtonDescriptor("unrelated-activated-submit"),
				activatedSubmitButtonRootID:    "unrelated-activated-details",
				confirmationSubmitButton:       submitButtonDescriptor("unrelated-submit"),
				confirmationSubmitButtonRootID: "unrelated-details",
				confirmationCurrentSubmitButton: map[string]any{
					"Id": "submit-confirmed-current", "Name": dynamics.ControlSubmitButton,
				},
				expectedSubmitButtonID: "submit-confirmed-current",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, observed := newMultiReceiptWorkflowServer(t, test.options, [][]byte{file})
			defer server.Close()

			client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			contract, err := expense.ReceiptUploadContractFromProfile(&capture.ReceiptProfile{Upload: validReceiptProfile("https://unused.invalid").Upload})
			if err != nil {
				t.Fatal(err)
			}
			request := expense.CreateReportWithReceiptsRequest{
				Purpose:        "Submit with receipt",
				UploadContract: contract,
				FinalAction:    expense.ReportFinalActionSubmit,
				Receipts: []expense.CreateReportReceiptInput{{
					Notes: "travel",
					Receipt: expense.ReceiptInput{
						Filename: "submit.png", MediaType: "image/png", Size: int64(len(file)),
						Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(file)), nil },
					},
				}},
			}

			plan, err := client.PlanCreateReportWithReceipts(request)
			if err != nil {
				t.Fatal(err)
			}
			if plan.RequestCount != 12 || !strings.Contains(strings.Join(plan.Actions, " "), "SubmitButton") || observed.requestCount != 0 {
				t.Fatalf("plan=%#v requests=%d", plan, observed.requestCount)
			}

			result, err := client.CreateReportWithReceipts(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Submitted || result.SavedAndClosed || result.Status != "2" || result.ReceiptCountAfter != 1 {
				t.Fatalf("result = %#v", result)
			}
			if observed.submitCount != 1 || observed.saveCount != 0 || observed.requestCount != 12 {
				t.Fatalf("requests=%d submit=%d save=%d", observed.requestCount, observed.submitCount, observed.saveCount)
			}
		})
	}
}

func TestCreateReportWithReceiptsRejectsConflictingDynamicSubmitButtonWhenSubmitting(t *testing.T) {
	file := []byte("\x89PNG\r\n\x1a\ninvalid-submit-metadata")
	server, observed := newMultiReceiptWorkflowServer(t, multiReceiptServerOptions{
		receiptCount: 1,
		submit:       true,
		confirmationSubmitButton: map[string]any{
			"Id": "tenant-submit", "Name": dynamics.ControlSubmitButton, "TypeName": "",
		},
	}, [][]byte{file})
	defer server.Close()

	client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := expense.ReceiptUploadContractFromProfile(&capture.ReceiptProfile{
		Upload: validReceiptProfile("https://unused.invalid").Upload,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := expense.CreateReportWithReceiptsRequest{
		Purpose:        "Submit with tenant-specific metadata",
		UploadContract: contract,
		FinalAction:    expense.ReportFinalActionSubmit,
		Receipts: []expense.CreateReportReceiptInput{{
			Receipt: expense.ReceiptInput{
				Filename: "receipt.png", MediaType: "image/png", Size: int64(len(file)),
				Open: func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(file)), nil
				},
			},
		}},
	}

	_, err = client.CreateReportWithReceipts(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "submit control is unavailable or unsupported") {
		t.Fatalf("CreateReportWithReceipts() error = %v", err)
	}
	if strings.Contains(err.Error(), "attach receipt") {
		t.Fatalf("SubmitButton was validated during receipt attachment: %v", err)
	}
	if observed.requestCount != 11 || observed.submitCount != 0 || observed.saveCount != 0 {
		t.Fatalf("requests=%d submit=%d save=%d, want 11/0/0", observed.requestCount, observed.submitCount, observed.saveCount)
	}
}

func TestCreateReportWithReceiptsValidatesEveryInputBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()

	client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := expense.ReceiptUploadContractFromProfile(&capture.ReceiptProfile{
		Upload: validReceiptProfile("https://unused.invalid").Upload,
	})
	if err != nil {
		t.Fatal(err)
	}
	opened := 0
	request := expense.CreateReportWithReceiptsRequest{
		Purpose:        "Validate all receipts",
		UploadContract: contract,
		FinalAction:    expense.ReportFinalActionSaveDraft,
		Receipts: []expense.CreateReportReceiptInput{
			{Receipt: expense.ReceiptInput{
				Filename: "first.png", MediaType: "image/png", Size: 1,
				Open: func() (io.ReadCloser, error) {
					opened++
					return io.NopCloser(strings.NewReader("x")), nil
				},
			}},
			{Receipt: expense.ReceiptInput{
				Filename: "second.png", MediaType: "image/jpeg", Size: 1,
				Open: func() (io.ReadCloser, error) {
					opened++
					return io.NopCloser(strings.NewReader("y")), nil
				},
			}},
		},
	}

	if _, err := client.PlanCreateReportWithReceipts(request); err == nil || !strings.Contains(err.Error(), "receipt 2") {
		t.Fatalf("PlanCreateReportWithReceipts() error = %v, want receipt 2 validation error", err)
	}
	if _, err := client.CreateReportWithReceipts(context.Background(), request); err == nil || !strings.Contains(err.Error(), "receipt 2") {
		t.Fatalf("CreateReportWithReceipts() error = %v, want receipt 2 validation error", err)
	}
	if requests != 0 || opened != 0 {
		t.Fatalf("invalid request performed side effects: requests=%d opened=%d", requests, opened)
	}

	request.Receipts = request.Receipts[:1]
	request.FinalAction = ""
	if _, err := client.PlanCreateReportWithReceipts(request); err == nil || !strings.Contains(err.Error(), "final action") {
		t.Fatalf("invalid final action plan error = %v", err)
	}
	if _, err := client.CreateReportWithReceipts(context.Background(), request); err == nil || !strings.Contains(err.Error(), "final action") {
		t.Fatalf("invalid final action execute error = %v", err)
	}
	if requests != 0 || opened != 0 {
		t.Fatalf("invalid final action performed side effects: requests=%d opened=%d", requests, opened)
	}

	request.Receipts = nil
	request.FinalAction = expense.ReportFinalActionSaveDraft
	if _, err := client.PlanCreateReportWithReceipts(request); err == nil || !strings.Contains(err.Error(), "at least one receipt") {
		t.Fatalf("empty receipt plan error = %v", err)
	}
	if _, err := client.CreateReportWithReceipts(context.Background(), request); err == nil || !strings.Contains(err.Error(), "at least one receipt") {
		t.Fatalf("empty receipt execute error = %v", err)
	}
	if requests != 0 || opened != 0 {
		t.Fatalf("empty request performed side effects: requests=%d opened=%d", requests, opened)
	}
}

func TestCreateReportWithReceiptsDoesNotSaveWhenLaterCumulativeCountFails(t *testing.T) {
	files := [][]byte{[]byte("first"), []byte("second")}
	server, observed := newMultiReceiptWorkflowServer(t, multiReceiptServerOptions{
		receiptCount:        2,
		failReceiptIndex:    1,
		failedReceiptStatus: "Draft",
		failedReceiptCount:  1,
		includeFailedCount:  true,
	}, files)
	defer server.Close()

	client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := expense.ReceiptUploadContractFromProfile(&capture.ReceiptProfile{
		Upload: validReceiptProfile("https://unused.invalid").Upload,
	})
	if err != nil {
		t.Fatal(err)
	}
	opened := []int{0, 0}
	receipts := make([]expense.CreateReportReceiptInput, 2)
	for index := range receipts {
		index := index
		receipts[index] = expense.CreateReportReceiptInput{
			Notes: fmt.Sprintf("receipt %d", index+1),
			Receipt: expense.ReceiptInput{
				Filename: fmt.Sprintf("receipt-%d.png", index+1), MediaType: "image/png", Size: int64(len(files[index])),
				Open: func() (io.ReadCloser, error) {
					opened[index]++
					return io.NopCloser(bytes.NewReader(files[index])), nil
				},
			},
		}
	}

	_, err = client.CreateReportWithReceipts(context.Background(), expense.CreateReportWithReceiptsRequest{
		Purpose: "Cumulative count failure", Receipts: receipts, UploadContract: contract,
		FinalAction: expense.ReportFinalActionSaveDraft,
	})
	if err == nil || !strings.Contains(err.Error(), "attach receipt 2") || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("CreateReportWithReceipts() error = %v", err)
	}
	if !reflect.DeepEqual(opened, []int{1, 1}) || len(observed.uploads) != 2 {
		t.Fatalf("opened=%v uploads=%d", opened, len(observed.uploads))
	}
	if observed.requestCount != 19 || observed.activateCount != 1 || observed.saveCount != 0 {
		t.Fatalf("requests=%d activate=%d save=%d, want 19/1/0", observed.requestCount, observed.activateCount, observed.saveCount)
	}
	for _, command := range observed.commands {
		if strings.Contains(command, "save-after-") {
			t.Fatalf("SaveAndClose emitted after failed receipt: %v", observed.commands)
		}
	}
}

func TestCreateReportWithReceiptsRejectsAggregateSequenceExhaustionBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()

	profile := validProfile(server.URL)
	// Two receipts require 21 client sequence numbers: five fixed plus eight
	// for each attachment. This leaves only 20.
	profile.Session.NextClientSequence = math.MaxInt64 - 20
	client, err := expense.New(profile, expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := expense.ReceiptUploadContractFromProfile(&capture.ReceiptProfile{
		Upload: validReceiptProfile("https://unused.invalid").Upload,
	})
	if err != nil {
		t.Fatal(err)
	}
	opened := 0
	receipt := func(name string) expense.CreateReportReceiptInput {
		return expense.CreateReportReceiptInput{Receipt: expense.ReceiptInput{
			Filename: name, MediaType: "image/png", Size: 1,
			Open: func() (io.ReadCloser, error) {
				opened++
				return io.NopCloser(strings.NewReader("x")), nil
			},
		}}
	}
	_, err = client.CreateReportWithReceipts(context.Background(), expense.CreateReportWithReceiptsRequest{
		Purpose: "Aggregate headroom", UploadContract: contract,
		Receipts:    []expense.CreateReportReceiptInput{receipt("one.png"), receipt("two.png")},
		FinalAction: expense.ReportFinalActionSaveDraft,
	})
	if err == nil || !strings.Contains(err.Error(), "headroom") {
		t.Fatalf("CreateReportWithReceipts() error = %v, want headroom error", err)
	}
	if requests != 0 || opened != 0 {
		t.Fatalf("headroom rejection performed side effects: requests=%d opened=%d", requests, opened)
	}
}

func newMultiReceiptWorkflowServer(t *testing.T, options multiReceiptServerOptions, files [][]byte) (*httptest.Server, *multiReceiptObservation) {
	t.Helper()
	if options.receiptCount != len(files) || options.receiptCount < 1 {
		t.Fatalf("invalid multi-receipt fixture: count=%d files=%d", options.receiptCount, len(files))
	}
	observed := &multiReceiptObservation{}
	expectedNextClientSequence := int64(80)
	expectedLastServerSequence := int64(50)
	serverSequence := int64(50)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		observed.requestCount++
		stage := observed.requestCount
		if request.Method != http.MethodPost {
			t.Errorf("stage %d method = %s, want POST", stage, request.Method)
		}
		if got, want := request.Header.Get("Origin"), "https://"+request.Host; got != want {
			t.Errorf("stage %d Origin = %q, want %q", stage, got, want)
		}
		for _, name := range []string{"ms-dyn-aid", "ms-dyn-bsid", "ms-dyn-csrftoken", "ms-dyn-sid"} {
			if got := request.Header.Get(name); got != "unit-header-secret" {
				t.Errorf("stage %d header %s = %q", stage, name, got)
			}
		}
		cookies := map[string]string{}
		for _, cookie := range request.Cookies() {
			cookies[cookie.Name] = cookie.Value
		}
		for _, name := range []string{"ms-dyn-csrftoken", "DynamicsOwinAuth"} {
			if cookies[name] != "unit-cookie-secret" {
				t.Errorf("stage %d cookie %s = %q", stage, name, cookies[name])
			}
		}

		if request.URL.Path == "/filemanagement" {
			receiptIndex, offset, ok := multiReceiptStage(stage, options.receiptCount)
			if !ok || offset != 5 {
				t.Fatalf("unexpected upload stage %d", stage)
			}
			upload := readMultiReceiptUpload(t, request)
			if !bytes.Equal(upload.data, files[receiptIndex]) {
				t.Errorf("receipt %d upload bytes = %q, want %q", receiptIndex+1, upload.data, files[receiptIndex])
			}
			observed.uploads = append(observed.uploads, upload)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"fileId":"uploaded-file-%d"}]`, receiptIndex+1)
			return
		}
		if request.URL.Path != processMessagesPath || request.URL.Query().Get("cmp") != "USMF" || request.URL.Query().Get("lng") != "en-us" {
			t.Errorf("stage %d URL = %s", stage, request.URL.RequestURI())
		}

		var envelope dynamics.Envelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			t.Fatalf("stage %d decode request: %v", stage, err)
		}
		appendMultiReceiptCommands(t, observed, envelope)
		if envelope.LastAcknowledgedSequenceNumber != expectedLastServerSequence {
			t.Errorf("stage %d acknowledged sequence = %d, want %d", stage, envelope.LastAcknowledgedSequenceNumber, expectedLastServerSequence)
		}
		for index, message := range envelope.Messages {
			want := expectedNextClientSequence + int64(index)
			if message.SequenceNumber != want {
				t.Errorf("stage %d message %d sequence = %d, want %d", stage, index, message.SequenceNumber, want)
			}
		}
		last := envelope.Messages[len(envelope.Messages)-1].SequenceNumber
		expectedNextClientSequence = last + 1
		serverSequence++
		expectedLastServerSequence = serverSequence
		w.Header().Set("Content-Type", "application/json")

		switch stage {
		case 1:
			commands := mustCommands(t, envelope.Messages[0])
			if len(commands) != 2 || commands[1].RootID != "captured-workspace-root" || commands[1].TargetID != "captured-new-button" {
				t.Errorf("open commands = %#v", commands)
			}
			writeEnvelope(t, w, responseEnvelope(7, last, serverSequence,
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
			if options.submit {
				children = append(children, submitButtonDescriptor("submit-created"))
			}
			writeEnvelope(t, w, responseEnvelope(7, last, serverSequence,
				viewModelInteraction(map[string]any{
					"Id": "new-details", "Name": dynamics.FormExpenseReportDetails, "TypeName": "Form",
					"ChildModelCollections": map[string]any{"TrvExpTable_ds": map[string]any{
						"ActiveRecordIndex": 0,
						"Items": []any{map[string]any{"Item": map[string]any{"Id": "new-record", "Properties": map[string]any{
							"dataSourceName_internal": "TrvExpTable_ds", "ExpNumber_field": combinedReportNumber,
							"expenseReportStatus_dataMethod": "Draft",
						}}}},
					}},
					"ChildViewModels": children,
				}),
			))
		case 3:
			assertActivateTabRequest(t, envelope, "new-details", "new-receipts-tab")
			observed.activateCount++
			interactions := []json.RawMessage{
				receiptViewModel("new-details", map[string]any{
					"Id": "new-receipts-tab", "Name": dynamics.ControlReceiptsTabPage, "TypeName": "PivotItem",
					"ChildViewModels": []any{map[string]any{"Id": "new-receipt-1", "Name": dynamics.ControlNewReceiptButton, "TypeName": "MenuItemButton"}},
				}),
				receiptViewModel("new-details", map[string]any{"Id": "activated-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "0"}}),
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "new-details", "Descriptor": map[string]any{"Id": "activated-record", "Properties": map[string]any{
					"ExpNumber_field": combinedReportNumber, "expenseReportStatus_dataMethod": "Draft",
				}}}),
			}
			if options.activatedSubmitButton != nil {
				rootID := "new-details"
				if options.activatedSubmitButtonRootID != "" {
					rootID = options.activatedSubmitButtonRootID
				}
				interactions = append(interactions, receiptViewModel(rootID, options.activatedSubmitButton))
			}
			if options.activatedCurrentSubmitButton != nil {
				interactions = append(interactions, receiptViewModel("new-details", options.activatedCurrentSubmitButton))
			}
			writeEnvelope(t, w, responseEnvelope(7, last, serverSequence, interactions...))
		default:
			receiptIndex, offset, ok := multiReceiptStage(stage, options.receiptCount)
			if ok {
				handleMultiReceiptStage(t, w, envelope, last, serverSequence, receiptIndex, offset, options, observed)
				return
			}
			finalStage := createReportWithReceiptsRequestCountForTest(options.receiptCount)
			if stage != finalStage {
				t.Fatalf("unexpected stage %d", stage)
			}
			if options.submit {
				targetID := options.expectedSubmitButtonID
				if targetID == "" {
					targetID = fmt.Sprintf("submit-after-%d", options.receiptCount)
				}
				assertSingleClick(t, envelope, "new-details", targetID)
				observed.submitCount++
			} else {
				assertSingleClick(t, envelope, "new-details", fmt.Sprintf("save-after-%d", options.receiptCount))
				observed.saveCount++
			}
			properties := map[string]any{"ExpNumber_field": combinedReportNumber}
			if options.submit {
				properties["ApprovalStatus_field"] = "2"
			}
			writeEnvelope(t, w, responseEnvelope(7, last, serverSequence,
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "workspace", "Descriptor": map[string]any{"Id": "saved-row", "Properties": properties}}),
				viewModelInteraction(map[string]any{
					"Id": "workspace-after-final-action", "Name": dynamics.FormExpenseWorkspace, "TypeName": "Form",
					"ChildViewModels": []any{map[string]any{
						"Id": "new-report-after-final-action", "Name": dynamics.SelectedControlNewExpenseReportReportsTab, "TypeName": "MenuItemButton",
					}},
				}),
			))
		}
	}))
	return server, observed
}

func handleMultiReceiptStage(
	t *testing.T,
	writer http.ResponseWriter,
	envelope dynamics.Envelope,
	acknowledged, serverSequence int64,
	receiptIndex, offset int,
	options multiReceiptServerOptions,
	observed *multiReceiptObservation,
) {
	t.Helper()
	countBefore := receiptIndex
	suffix := receiptIndex + 1
	buttonID := fmt.Sprintf("new-receipt-%d", suffix)
	preflightID := fmt.Sprintf("preflight-%d", suffix)
	dialogID := fmt.Sprintf("receipt-dialog-%d", suffix)
	uploadID := fmt.Sprintf("receipt-upload-%d", suffix)
	okID := fmt.Sprintf("receipt-ok-%d", suffix)

	switch offset {
	case 0:
		assertOpenReceiptRequest(t, envelope, "new-details", buttonID)
		observed.openButtons = append(observed.openButtons, buttonID)
		writeEnvelope(t, writer, responseEnvelope(7, acknowledged, serverSequence,
			receiptViewModel("new-details", map[string]any{"Id": fmt.Sprintf("preflight-count-%d", suffix), "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": fmt.Sprint(countBefore)}}),
			receiptViewModel(preflightID, map[string]any{
				"Id": preflightID, "Name": dynamics.FormExpenseAddNewReceipt, "TypeName": "Dialog",
				"ValueProperties": map[string]any{"ParentTitleFields": "User : " + combinedReportNumber},
				"ChildViewModels": []any{map[string]any{"Id": preflightID + "-close", "Name": dynamics.ControlCloseButtonAddNewTabPage, "TypeName": "CommandButton"}},
			}),
		))
	case 1:
		assertSingleClick(t, envelope, preflightID, preflightID+"-close")
		writeEnvelope(t, writer, responseEnvelope(7, acknowledged, serverSequence,
			receiptViewModel("new-details", map[string]any{"Id": fmt.Sprintf("closed-count-%d", suffix), "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": fmt.Sprint(countBefore)}}),
			mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "new-details", "Descriptor": map[string]any{"Id": fmt.Sprintf("closed-record-%d", suffix), "Properties": map[string]any{
				"ExpNumber_field": combinedReportNumber, "expenseReportStatus_dataMethod": "Draft",
			}}}),
		))
	case 2:
		assertOpenReceiptRequest(t, envelope, "new-details", buttonID)
		observed.openButtons = append(observed.openButtons, buttonID)
		writeEnvelope(t, writer, responseEnvelope(7, acknowledged, serverSequence,
			receiptViewModel("new-details", map[string]any{"Id": fmt.Sprintf("reopen-count-%d", suffix), "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": fmt.Sprint(countBefore)}}),
			receiptViewModel(dialogID, map[string]any{
				"Id": dialogID, "Name": dynamics.FormExpenseAddNewReceipt, "TypeName": "Dialog",
				"ValueProperties": map[string]any{"ParentTitleFields": "User : " + combinedReportNumber},
				"ChildViewModels": []any{
					map[string]any{
						"Id": uploadID, "Name": dynamics.ControlUploadControl, "TypeName": "DocumentUpload",
						"ValueProperties": map[string]any{
							"AccessToken": fmt.Sprintf("fresh-token-%d", suffix), "CurrentRecId": fmt.Sprintf("564807169%d", suffix),
							"CurrentDocuRefRecId": "0", "SelectedDocumentType": "File",
						},
						"SerializedValueProperties": map[string]any{"CurrentTableId": "23090"},
					},
					map[string]any{"Id": okID, "Name": dynamics.ControlOKButtonAddNewTabPage, "TypeName": "CommandButton"},
					map[string]any{"Id": dialogID + "-close", "Name": dynamics.ControlCloseButtonAddNewTabPage, "TypeName": "CommandButton"},
				},
			}),
		))
	case 3:
		property, err := dynamics.ParsePropertyChangeInteraction(envelope.Messages[0].Interactions[0])
		wantDocumentName := strings.TrimSuffix(observedFilenameForReceipt(envelope, suffix), filepath.Ext(observedFilenameForReceipt(envelope, suffix)))
		if err != nil || property.PropertyName != dynamics.PropertyDocuName || property.RootID != dialogID || property.TargetID != uploadID || property.NewValue != wantDocumentName {
			t.Errorf("receipt %d DocuName property = %#v, err=%v, want %q", suffix, property, err, wantDocumentName)
		}
		writeEnvelope(t, writer, responseEnvelope(7, acknowledged, serverSequence))
	case 4:
		writeEnvelope(t, writer, responseEnvelope(7, acknowledged, serverSequence))
	case 6:
		property, err := dynamics.ParsePropertyChangeInteraction(envelope.Messages[0].Interactions[0])
		wantFileID := fmt.Sprintf("uploaded-file-%d", suffix)
		if err != nil || property.PropertyName != dynamics.PropertyUploadedFileID || property.RootID != dialogID || property.TargetID != uploadID || property.NewValue != wantFileID {
			t.Errorf("receipt %d UploadedFileId property = %#v, err=%v", suffix, property, err)
		}
		writeEnvelope(t, writer, responseEnvelope(7, acknowledged, serverSequence))
	case 7:
		assertSingleClick(t, envelope, dialogID, okID)
		status := "Draft"
		count := countBefore + 1
		includeCount := true
		if (options.failedReceiptStatus != "" || options.includeFailedCount) && receiptIndex == options.failReceiptIndex {
			if options.failedReceiptStatus != "" {
				status = options.failedReceiptStatus
			}
			if options.includeFailedCount {
				count = options.failedReceiptCount
			} else {
				includeCount = false
			}
		}
		interactions := []json.RawMessage{
			mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "new-details", "Descriptor": map[string]any{"Id": fmt.Sprintf("after-record-%d", suffix), "Properties": map[string]any{
				"ExpNumber_field": combinedReportNumber, "expenseReportStatus_dataMethod": status,
			}}}),
			receiptViewModel("new-details", map[string]any{"Id": fmt.Sprintf("new-receipt-%d", suffix+1), "Name": dynamics.ControlNewReceiptButton, "TypeName": "MenuItemButton"}),
			receiptViewModel("new-details", map[string]any{"Id": fmt.Sprintf("save-after-%d", suffix), "Name": dynamics.ControlSaveAndClose, "TypeName": "CommandButton"}),
		}
		if options.submit && !options.omitConfirmationSubmitButton {
			descriptor := submitButtonDescriptor(fmt.Sprintf("submit-after-%d", suffix))
			if options.confirmationSubmitButton != nil {
				descriptor = options.confirmationSubmitButton
			}
			rootID := "new-details"
			if options.confirmationSubmitButtonRootID != "" {
				rootID = options.confirmationSubmitButtonRootID
			}
			interactions = append(interactions, receiptViewModel(rootID, descriptor))
			if options.confirmationCurrentSubmitButton != nil {
				interactions = append(interactions, receiptViewModel("new-details", options.confirmationCurrentSubmitButton))
			}
		}
		if includeCount {
			interactions = append([]json.RawMessage{
				receiptViewModel("new-details", map[string]any{"Id": fmt.Sprintf("after-count-%d", suffix), "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": fmt.Sprint(count)}}),
			}, interactions...)
		}
		writeEnvelope(t, writer, responseEnvelope(7, acknowledged, serverSequence, interactions...))
	default:
		t.Fatalf("unexpected receipt %d offset %d", suffix, offset)
	}
}

func multiReceiptStage(stage, receiptCount int) (receiptIndex, offset int, ok bool) {
	if stage < 4 || stage >= 4+receiptCount*8 {
		return 0, 0, false
	}
	relative := stage - 4
	return relative / 8, relative % 8, true
}

func createReportWithReceiptsRequestCountForTest(receiptCount int) int {
	return 4 + receiptCount*8
}

func appendMultiReceiptCommands(t *testing.T, observed *multiReceiptObservation, envelope dynamics.Envelope) {
	t.Helper()
	for _, message := range envelope.Messages {
		for _, raw := range message.Interactions {
			command, err := dynamics.ParseCommandInteraction(raw)
			if err != nil || command.Type != dynamics.InteractionTypeCommand || command.CommandName == "" {
				continue
			}
			observed.commands = append(observed.commands, command.CommandName+":"+command.TargetID)
			if strings.Contains(strings.ToLower(command.CommandName), "submit") {
				t.Errorf("forbidden command emitted: %s", command.CommandName)
			}
		}
	}
}

func readMultiReceiptUpload(t *testing.T, request *http.Request) observedMultiUpload {
	t.Helper()
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		t.Fatalf("upload Content-Type = %q, err=%v", request.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(request.Body, parameters["boundary"])
	result := observedMultiUpload{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart(): %v", err)
		}
		result.names = append(result.names, part.FormName())
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read multipart part: %v", err)
		}
		if part.FileName() != "" {
			result.filename = part.FileName()
			result.data = data
			if part.Header.Get("Content-Type") != "image/png" {
				t.Errorf("uploaded media type = %q", part.Header.Get("Content-Type"))
			}
		} else if part.FormName() == "notes" {
			result.notes = string(data)
		}
	}
	return result
}

func receiptMultipartFieldOrderForTest() []string {
	return []string{
		"clientId", "maxChunkSize", "tableid", "recid", "companyid", "accesstoken",
		"notes", "docuname", "docutypeid", "ischunked", "docuRefRecId", "files[]",
	}
}

func observedFilenameForReceipt(envelope dynamics.Envelope, suffix int) string {
	if len(envelope.Messages) < 2 || len(envelope.Messages[1].Interactions) == 0 {
		return fmt.Sprintf("receipt-%d.png", suffix)
	}
	command, err := dynamics.ParseCommandInteraction(envelope.Messages[1].Interactions[0])
	if err != nil || len(command.PositionalParameters) < 2 {
		return fmt.Sprintf("receipt-%d.png", suffix)
	}
	filename, _ := command.PositionalParameters[1].(string)
	if filename == "" {
		return fmt.Sprintf("receipt-%d.png", suffix)
	}
	return filename
}
