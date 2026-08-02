package expense_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/dynamics"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func TestAttachReceiptUsesFreshDynamicStateAndStreamsExactMultipart(t *testing.T) {
	t.Parallel()

	const (
		reportNumber = "RPT-0001"
		accessToken  = "fresh-runtime-access-token"
		fileID       = "runtime-file-id-secret"
	)
	fileBytes := []byte("synthetic-png-bytes")
	var opened int
	var requestNumber int
	var requestMu sync.Mutex
	var commands []string
	var uploadBoundary string
	var uploadClientID string

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMu.Lock()
		requestNumber++
		stage := requestNumber
		requestMu.Unlock()

		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got, want := r.Header.Get("Origin"), "https://"+r.Host; got != want {
			t.Errorf("Origin = %q, want %q", got, want)
		}
		for _, name := range []string{"ms-dyn-aid", "ms-dyn-bsid", "ms-dyn-csrftoken", "ms-dyn-sid"} {
			if got := r.Header.Get(name); got != "receipt-header-secret" {
				t.Errorf("header %s = %q", name, got)
			}
		}
		if got := r.Header.Get("X-Unsafe-Captured"); got != "" {
			t.Errorf("unsafe captured header was replayed: %q", got)
		}
		cookies := map[string]string{}
		for _, cookie := range r.Cookies() {
			cookies[cookie.Name] = cookie.Value
		}
		for _, name := range []string{"ms-dyn-csrftoken", "DynamicsOwinAuth"} {
			if cookies[name] != "receipt-cookie-secret" {
				t.Errorf("cookie %s was not applied", name)
			}
		}

		if r.URL.Path == "/filemanagement" {
			if stage != 6 {
				t.Errorf("upload stage = %d, want 6", stage)
			}
			mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
				t.Errorf("upload Content-Type = %q, err = %v", r.Header.Get("Content-Type"), err)
				return
			}
			uploadBoundary = parameters["boundary"]
			reader := multipart.NewReader(r.Body, uploadBoundary)
			var names []string
			values := map[string]string{}
			var uploaded []byte
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Errorf("NextPart(): %v", err)
					return
				}
				names = append(names, part.FormName())
				data, err := io.ReadAll(part)
				if err != nil {
					t.Errorf("read multipart part: %v", err)
					return
				}
				if part.FileName() != "" {
					if part.FileName() != "receipt.png" || part.Header.Get("Content-Type") != "image/png" {
						t.Errorf("file part = filename %q type %q", part.FileName(), part.Header.Get("Content-Type"))
					}
					uploaded = data
				} else {
					values[part.FormName()] = string(data)
				}
			}
			wantNames := []string{"clientId", "maxChunkSize", "tableid", "recid", "companyid", "accesstoken", "notes", "docuname", "docutypeid", "ischunked", "docuRefRecId", "files[]"}
			if !reflect.DeepEqual(names, wantNames) {
				t.Errorf("multipart names = %v, want %v", names, wantNames)
			}
			uploadClientID = values["clientId"]
			if !regexp.MustCompile(`^[A-Z0-9]{9}$`).MatchString(uploadClientID) {
				t.Errorf("clientId = %q", uploadClientID)
			}
			wantValues := map[string]string{
				"maxChunkSize": "1024000", "tableid": "23090", "recid": "5647982574",
				"companyid": "USMF", "accesstoken": accessToken, "notes": "Taxi receipt",
				"docuname": "receipt", "docutypeid": "File", "ischunked": "false", "docuRefRecId": "0",
			}
			for name, want := range wantValues {
				if values[name] != want {
					t.Errorf("multipart %s = %q, want %q", name, values[name], want)
				}
			}
			if !bytes.Equal(uploaded, fileBytes) {
				t.Errorf("uploaded bytes = %q, want %q", uploaded, fileBytes)
			}
			if r.ContentLength <= int64(len(fileBytes)) {
				t.Errorf("Content-Length = %d, must include multipart metadata", r.ContentLength)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"fileId":"`+fileID+`"}]`)
			return
		}

		if r.URL.Path != processMessagesPath || r.URL.Query().Get("cmp") != "USMF" || r.URL.Query().Get("lng") != "en-us" {
			t.Errorf("RCM URL = %s", r.URL.RequestURI())
		}
		var request dynamics.Envelope
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode RCM request: %v", err)
			return
		}
		if request.ChannelID != 7 || request.CompanyID != "USMF" || request.Language != "en-us" {
			t.Errorf("session metadata = %#v", request)
		}
		for _, message := range request.Messages {
			for _, raw := range message.Interactions {
				command, err := dynamics.ParseCommandInteraction(raw)
				if err != nil || command.Type != dynamics.InteractionTypeCommand {
					continue
				}
				commands = append(commands, command.CommandName+":"+command.TargetID)
				lower := strings.ToLower(command.CommandName)
				for _, forbidden := range []string{"submit", "workflow", "posting", "approval", "recall"} {
					if strings.Contains(lower, forbidden) {
						t.Errorf("forbidden command emitted: %s", command.CommandName)
					}
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		last := request.Messages[len(request.Messages)-1].SequenceNumber
		switch stage {
		case 1:
			if request.LastAcknowledgedSequenceNumber != 50 || last != 80 {
				t.Errorf("preflight open sequence state = ack %d last %d", request.LastAcknowledgedSequenceNumber, last)
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 51,
				receiptViewModel("captured-details", map[string]any{"Id": "before-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "2"}}),
				receiptViewModel("preflight-dialog", map[string]any{
					"Id": "preflight-dialog", "Name": dynamics.FormExpenseAddNewReceipt, "TypeName": "Dialog",
					"ValueProperties": map[string]any{"ParentTitleFields": "Fixture User : " + reportNumber},
					"ChildViewModels": []any{
						map[string]any{"Id": "preflight-close", "Name": dynamics.ControlCloseButtonAddNewTabPage, "TypeName": "CommandButton"},
					},
				}),
			))
		case 2:
			clicks := mustCommands(t, request.Messages[0])
			if len(clicks) != 1 || clicks[0].RootID != "preflight-dialog" || clicks[0].TargetID != "preflight-close" {
				t.Errorf("preflight close click = %#v", clicks)
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 52,
				receiptViewModel("captured-details", map[string]any{"Id": "preflight-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "2"}}),
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "captured-details", "Descriptor": map[string]any{"Id": "preflight-model", "Properties": map[string]any{"ExpNumber_field": reportNumber, "expenseReportStatus_dataMethod": "Draft"}}}),
			))
		case 3:
			writeEnvelope(t, w, responseEnvelope(7, last, 53,
				receiptViewModel("captured-details", map[string]any{"Id": "reopen-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "2"}}),
				receiptViewModel("dynamic-dialog", map[string]any{
					"Id": "dynamic-dialog", "Name": dynamics.FormExpenseAddNewReceipt, "TypeName": "Dialog",
					"ValueProperties": map[string]any{"ParentTitleFields": "Fixture User : " + reportNumber},
					"ChildViewModels": []any{
						map[string]any{
							"Id": "dynamic-upload", "Name": dynamics.ControlUploadControl, "TypeName": "DocumentUpload",
							"ValueProperties":           map[string]any{"AccessToken": accessToken, "CurrentRecId": "5647982574", "CurrentDocuRefRecId": "0", "SelectedDocumentType": "File"},
							"SerializedValueProperties": map[string]any{"CurrentTableId": "23090"},
						},
						map[string]any{"Id": "dynamic-ok", "Name": dynamics.ControlOKButtonAddNewTabPage, "TypeName": "CommandButton"},
						map[string]any{"Id": "dynamic-close", "Name": dynamics.ControlCloseButtonAddNewTabPage, "TypeName": "CommandButton"},
					},
				}),
			))
		case 4:
			if len(request.Messages) != 2 || request.Messages[0].SequenceNumber != 83 || request.Messages[1].SequenceNumber != 84 {
				t.Errorf("DocuName/CheckFile sequences = %#v", request.Messages)
			}
			property, err := dynamics.ParsePropertyChangeInteraction(request.Messages[0].Interactions[0])
			if err != nil || property.PropertyName != dynamics.PropertyDocuName || property.NewValue != "receipt" || property.RootID != "dynamic-dialog" || property.TargetID != "dynamic-upload" {
				t.Errorf("DocuName property = %#v, err = %v", property, err)
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 54))
		case 5:
			if len(request.Messages) != 1 || request.Messages[0].SequenceNumber != 85 {
				t.Errorf("second CheckFile request = %#v", request)
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 55))
		case 7:
			if request.Messages[0].SequenceNumber != 86 {
				t.Errorf("completion sequence = %d", request.Messages[0].SequenceNumber)
			}
			property, err := dynamics.ParsePropertyChangeInteraction(request.Messages[0].Interactions[0])
			if err != nil || property.PropertyName != dynamics.PropertyUploadedFileID || property.NewValue != fileID {
				t.Errorf("UploadedFileId property shape was wrong")
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 56))
		case 8:
			clicks := mustCommands(t, request.Messages[0])
			if len(clicks) != 1 || clicks[0].RootID != "dynamic-dialog" || clicks[0].TargetID != "dynamic-ok" {
				t.Errorf("OK click = %#v", clicks)
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 57,
				receiptViewModel("captured-details", map[string]any{"Id": "after-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "3"}}),
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "captured-details", "Descriptor": map[string]any{"Id": "report-model", "Properties": map[string]any{"ExpNumber_field": reportNumber, "expenseReportStatus_dataMethod": "Draft"}}}),
				receiptViewModel("captured-details", map[string]any{"Id": "dynamic-save", "Name": dynamics.ControlSaveAndClose, "TypeName": "CommandButton"}),
			))
		case 9:
			clicks := mustCommands(t, request.Messages[0])
			if len(clicks) != 1 || clicks[0].RootID != "captured-details" || clicks[0].TargetID != "dynamic-save" {
				t.Errorf("SaveAndClose click = %#v", clicks)
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 58,
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "workspace", "Descriptor": map[string]any{"Id": "row", "Properties": map[string]any{"ExpNumber_field": reportNumber}}}),
			))
		default:
			t.Errorf("unexpected request stage %d", stage)
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	profile := validReceiptProfile(server.URL)
	client, err := expense.NewReceiptClient(profile, expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewReceiptClient() error = %v", err)
	}
	request := expense.AttachReceiptRequest{
		ReportNumber: reportNumber,
		Notes:        "Taxi receipt",
		Receipt: expense.ReceiptInput{
			Filename: "receipt.png", MediaType: "image/png", Size: int64(len(fileBytes)),
			Open: func() (io.ReadCloser, error) {
				opened++
				return io.NopCloser(bytes.NewReader(fileBytes)), nil
			},
		},
	}

	plan, err := client.PlanAttachReceipt(request)
	if err != nil {
		t.Fatalf("PlanAttachReceipt() error = %v", err)
	}
	if plan.RequestCount != 9 || opened != 0 || requestNumber != 0 || plan.ReportNumber != reportNumber || plan.Receipt.Filename != "receipt.png" {
		t.Fatalf("plan = %#v, opened = %d, requests = %d", plan, opened, requestNumber)
	}
	planText := fmt.Sprintf("%#v", plan)
	for _, secret := range []string{"receipt-header-secret", "receipt-cookie-secret", "captured-details", server.URL, accessToken, fileID} {
		if strings.Contains(planText, secret) {
			t.Fatalf("plan exposed %q: %s", secret, planText)
		}
	}

	result, err := client.AttachReceipt(context.Background(), request)
	if err != nil {
		t.Fatalf("AttachReceipt() error = %v", err)
	}
	want := expense.AttachReceiptResult{
		ReportNumber: reportNumber, Status: "Draft", ReceiptCount: 3,
		Attached: expense.AttachedReceipt{Filename: "receipt.png", Size: int64(len(fileBytes))}, SavedAndClosed: true,
	}
	if result != want {
		t.Fatalf("AttachReceipt() = %#v, want %#v", result, want)
	}
	if opened != 1 || requestNumber != 9 || uploadBoundary == "" || uploadClientID == "" {
		t.Fatalf("opened = %d requests = %d boundary = %q clientId = %q", opened, requestNumber, uploadBoundary, uploadClientID)
	}
	if got, want := commands, []string{
		"UpdateLastSelectedControl:captured-details", "Click:captured-add",
		"Click:preflight-close",
		"UpdateLastSelectedControl:captured-details", "Click:captured-add",
		"CheckFile:dynamic-upload", "CheckFile:dynamic-upload", "CloseDialog:dynamic-upload",
		"Click:dynamic-ok", "Click:dynamic-save",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	resultText := fmt.Sprintf("%#v", result)
	for _, secret := range []string{accessToken, fileID} {
		if strings.Contains(resultText, secret) {
			t.Fatalf("result exposed transient secret %q", secret)
		}
	}
}

func TestAttachReceiptRejectsInputWithoutOpeningReader(t *testing.T) {
	t.Parallel()
	client, err := expense.NewReceiptClient(validReceiptProfile("https://example.test"))
	if err != nil {
		t.Fatal(err)
	}
	opened := 0
	valid := expense.AttachReceiptRequest{
		ReportNumber: "RPT-0001",
		Receipt: expense.ReceiptInput{
			Filename: "receipt.png", MediaType: "image/png", Size: 1,
			Open: func() (io.ReadCloser, error) { opened++; return io.NopCloser(strings.NewReader("x")), nil },
		},
	}
	tests := map[string]func(*expense.AttachReceiptRequest){
		"wrong report": func(request *expense.AttachReceiptRequest) { request.ReportNumber = "RPT-OTHER" },
		"path":         func(request *expense.AttachReceiptRequest) { request.Receipt.Filename = "dir/receipt.png" },
		"empty":        func(request *expense.AttachReceiptRequest) { request.Receipt.Filename = "" },
		"wrong media":  func(request *expense.AttachReceiptRequest) { request.Receipt.MediaType = "image/jpeg" },
		"zero size":    func(request *expense.AttachReceiptRequest) { request.Receipt.Size = 0 },
		"too large":    func(request *expense.AttachReceiptRequest) { request.Receipt.Size = 1024001 },
		"nil reader":   func(request *expense.AttachReceiptRequest) { request.Receipt.Open = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if _, err := client.PlanAttachReceipt(request); err == nil {
				t.Fatal("PlanAttachReceipt() unexpectedly succeeded")
			}
			if _, err := client.AttachReceipt(context.Background(), request); err == nil {
				t.Fatal("AttachReceipt() unexpectedly succeeded")
			}
		})
	}
	if opened != 0 {
		t.Fatalf("invalid input opened reader %d times", opened)
	}
}

func TestAttachReceiptRejectsShortAndLongReaders(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		declared int64
		data     string
		want     string
	}{
		{name: "short", declared: 4, data: "abc", want: "before its declared size"},
		{name: "long", declared: 3, data: "abcd", want: "exceeded its declared size"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := receiptServerUntilUpload(t, "RPT-0001", func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `[{"fileId":"must-not-leak"}]`)
			})
			defer server.Close()
			client, err := expense.NewReceiptClient(validReceiptProfile(server.URL), expense.WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.AttachReceipt(context.Background(), expense.AttachReceiptRequest{
				ReportNumber: "RPT-0001",
				Receipt: expense.ReceiptInput{Filename: "receipt.png", MediaType: "image/png", Size: test.declared,
					Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(test.data)), nil }},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("AttachReceipt() error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("error leaked file identifier: %v", err)
			}
		})
	}
}

func TestAttachReceiptRejectsStaleWrongReportNonDraftCountAndRedirect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "stale", mode: "stale", want: "stale"},
		{name: "wrong report", mode: "wrong-report", want: "different report"},
		{name: "non Draft", mode: "non-draft", want: "not Draft"},
		{name: "count", mode: "count", want: "did not increase"},
		{name: "redirect", mode: "redirect", want: "redirects are not allowed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := receiptFailureServer(t, test.mode)
			defer server.Close()
			client, err := expense.NewReceiptClient(validReceiptProfile(server.URL), expense.WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("png")
			_, err = client.AttachReceipt(context.Background(), expense.AttachReceiptRequest{
				ReportNumber: "RPT-0001",
				Receipt: expense.ReceiptInput{Filename: "receipt.png", MediaType: "image/png", Size: int64(len(data)),
					Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }},
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("AttachReceipt() error = %v, want %q", err, test.want)
			}
		})
	}
}

func validReceiptProfile(baseURL string) *capture.ReceiptProfile {
	return &capture.ReceiptProfile{
		Session: capture.SessionProfile{
			BaseURL: baseURL, EndpointURL: baseURL + processMessagesPath + "?cmp=USMF&lng=en-us",
			RequestHeaders: http.Header{
				"Accept":            []string{"application/json"},
				"Content-Type":      []string{"application/json; charset=UTF-8"},
				"Origin":            []string{baseURL},
				"Referer":           []string{baseURL + "/?cmp=USMF"},
				"X-Requested-With":  []string{"XMLHttpRequest"},
				"X-Unsafe-Captured": []string{"do-not-replay"},
				"ms-dyn-aid":        []string{"receipt-header-secret"},
				"ms-dyn-bsid":       []string{"receipt-header-secret"},
				"ms-dyn-csrftoken":  []string{"receipt-header-secret"},
				"ms-dyn-sid":        []string{"receipt-header-secret"},
			},
			Cookies: []*http.Cookie{
				{Name: "ms-dyn-csrftoken", Value: "receipt-cookie-secret", Path: "/", Secure: true},
				{Name: "DynamicsOwinAuth", Value: "receipt-cookie-secret", Path: "/", Secure: true, HttpOnly: true},
			},
			Company: "USMF", Language: "en-us", ChannelID: 7, LastServerSequence: 50, NextClientSequence: 80,
		},
		ReportNumber: "RPT-0001", ReportStatus: "Draft", ReceiptCount: 2, DetailsFormRootID: "captured-details",
		AddReceipts:  capture.CommandTarget{CommandName: dynamics.CommandClick, RootID: "captured-details", TargetID: "captured-add", ControlName: dynamics.ControlNewReceiptButton},
		SaveAndClose: capture.CommandTarget{CommandName: dynamics.CommandClick, RootID: "captured-details", TargetID: "captured-save", ControlName: dynamics.ControlSaveAndClose},
		Expected: capture.ReceiptExpectedNames{
			DetailsForm: dynamics.FormExpenseReportDetails, AddReceiptForm: dynamics.FormExpenseAddNewReceipt,
			AddReceiptsControl: dynamics.ControlNewReceiptButton, UploadControl: dynamics.ControlUploadControl,
			OKControl: dynamics.ControlOKButtonAddNewTabPage, ReceiptCountControl: dynamics.ControlReceiptCount,
			SaveAndCloseControl: dynamics.ControlSaveAndClose,
		},
		Upload: capture.ReceiptUploadProfile{
			EndpointPath:        "/filemanagement",
			MultipartFieldOrder: []string{"clientId", "maxChunkSize", "tableid", "recid", "companyid", "accesstoken", "notes", "docuname", "docutypeid", "ischunked", "docuRefRecId", "files[]"},
			MaxChunkSize:        1024000, DocumentType: "File", MaxSupportedSingleFileSize: 1024000,
		},
	}
}

func receiptViewModel(rootID string, descriptor map[string]any) json.RawMessage {
	return mustRaw(map[string]any{"$type": "UpdateViewModelInteraction", "RootId": rootID, "Descriptor": descriptor})
}

func receiptServerUntilUpload(t *testing.T, reportNumber string, upload http.HandlerFunc) *httptest.Server {
	t.Helper()
	stage := 0
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stage++
		if r.URL.Path == "/filemanagement" {
			upload(w, r)
			return
		}
		var request dynamics.Envelope
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		last := request.Messages[len(request.Messages)-1].SequenceNumber
		w.Header().Set("Content-Type", "application/json")
		switch stage {
		case 1:
			writeEnvelope(t, w, responseEnvelope(7, last, 51,
				receiptViewModel("details", map[string]any{"Id": "before-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "2"}}),
				receiptViewModel("preflight", map[string]any{
					"Id": "preflight", "Name": dynamics.FormExpenseAddNewReceipt, "TypeName": "Dialog",
					"ValueProperties": map[string]any{"ParentTitleFields": "User : " + reportNumber},
					"ChildViewModels": []any{map[string]any{"Id": "preflight-close", "Name": dynamics.ControlCloseButtonAddNewTabPage, "TypeName": "CommandButton"}},
				}),
			))
		case 2:
			writeEnvelope(t, w, responseEnvelope(7, last, 52,
				receiptViewModel("details", map[string]any{"Id": "preflight-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "2"}}),
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "details", "Descriptor": map[string]any{"Id": "model", "Properties": map[string]any{"ExpNumber_field": reportNumber, "expenseReportStatus_dataMethod": "Draft"}}}),
			))
		case 3:
			writeEnvelope(t, w, responseEnvelope(7, last, 53,
				receiptViewModel("details", map[string]any{"Id": "reopen-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "2"}}),
				receiptViewModel("dialog", map[string]any{
					"Id": "dialog", "Name": dynamics.FormExpenseAddNewReceipt, "TypeName": "Dialog",
					"ValueProperties": map[string]any{"ParentTitleFields": "User : " + reportNumber},
					"ChildViewModels": []any{
						map[string]any{"Id": "upload", "Name": dynamics.ControlUploadControl, "TypeName": "DocumentUpload", "ValueProperties": map[string]any{"AccessToken": "fresh-token", "CurrentRecId": "1", "CurrentDocuRefRecId": "0", "SelectedDocumentType": "File"}, "SerializedValueProperties": map[string]any{"CurrentTableId": "2"}},
						map[string]any{"Id": "ok", "Name": dynamics.ControlOKButtonAddNewTabPage, "TypeName": "CommandButton"},
						map[string]any{"Id": "close", "Name": dynamics.ControlCloseButtonAddNewTabPage, "TypeName": "CommandButton"},
					},
				}),
			))
		case 4:
			writeEnvelope(t, w, responseEnvelope(7, last, 54))
		case 5:
			writeEnvelope(t, w, responseEnvelope(7, last, 55))
		default:
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
}

func receiptFailureServer(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	stage := 0
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stage++
		if mode == "redirect" && stage == 1 {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		if r.URL.Path == "/filemanagement" {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"fileId":"secret-file-id"}]`)
			return
		}
		var request dynamics.Envelope
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		last := request.Messages[len(request.Messages)-1].SequenceNumber
		w.Header().Set("Content-Type", "application/json")
		if mode == "stale" && stage == 1 {
			writeEnvelope(t, w, responseEnvelope(7, last-1, 51))
			return
		}
		switch stage {
		case 1:
			report := "RPT-0001"
			if mode == "wrong-report" {
				report = "RPT-9999"
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 51,
				receiptViewModel("details", map[string]any{"Id": "before-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "2"}}),
				receiptViewModel("preflight", map[string]any{
					"Id": "preflight", "Name": dynamics.FormExpenseAddNewReceipt, "TypeName": "Dialog", "ValueProperties": map[string]any{"ParentTitleFields": "User : " + report},
					"ChildViewModels": []any{map[string]any{"Id": "preflight-close", "Name": dynamics.ControlCloseButtonAddNewTabPage, "TypeName": "CommandButton"}},
				}),
			))
		case 2:
			status := "Draft"
			if mode == "non-draft" {
				status = "Submitted"
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 52,
				receiptViewModel("details", map[string]any{"Id": "preflight-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "2"}}),
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "details", "Descriptor": map[string]any{"Id": "model", "Properties": map[string]any{"ExpNumber_field": "RPT-0001", "expenseReportStatus_dataMethod": status}}}),
			))
		case 3:
			writeEnvelope(t, w, responseEnvelope(7, last, 53,
				receiptViewModel("details", map[string]any{"Id": "reopen-count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": "2"}}),
				receiptViewModel("dialog", map[string]any{
					"Id": "dialog", "Name": dynamics.FormExpenseAddNewReceipt, "TypeName": "Dialog", "ValueProperties": map[string]any{"ParentTitleFields": "User : RPT-0001"},
					"ChildViewModels": []any{
						map[string]any{"Id": "upload", "Name": dynamics.ControlUploadControl, "TypeName": "DocumentUpload", "ValueProperties": map[string]any{"AccessToken": "fresh-token", "CurrentRecId": "1", "CurrentDocuRefRecId": "0", "SelectedDocumentType": "File"}, "SerializedValueProperties": map[string]any{"CurrentTableId": "2"}},
						map[string]any{"Id": "ok", "Name": dynamics.ControlOKButtonAddNewTabPage, "TypeName": "CommandButton"},
					},
				}),
			))
		case 4:
			writeEnvelope(t, w, responseEnvelope(7, last, 54))
		case 5:
			writeEnvelope(t, w, responseEnvelope(7, last, 55))
		case 7:
			writeEnvelope(t, w, responseEnvelope(7, last, 56))
		case 8:
			count := "3"
			if mode == "count" {
				count = "2"
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 57,
				receiptViewModel("details", map[string]any{"Id": "count", "Name": dynamics.ControlReceiptCount, "TypeName": "Integer", "ValueProperties": map[string]any{"Value": count}}),
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "details", "Descriptor": map[string]any{"Id": "model", "Properties": map[string]any{"expenseReportStatus_dataMethod": "Draft"}}}),
			))
		case 9:
			writeEnvelope(t, w, responseEnvelope(7, last, 58))
		default:
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
}
