package expense

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func TestNewFromBootstrapSnapshotsIndependentState(t *testing.T) {
	profile := stateTestBootstrapProfile("https://example.test")
	client, err := NewFromBootstrap(profile)
	if err != nil {
		t.Fatalf("NewFromBootstrap() error = %v", err)
	}

	profile.Session.RequestHeaders.Set("ms-dyn-sid", "mutated-source-header")
	profile.Session.Cookies[0].Value = "mutated-source-cookie"
	profile.NewReport.RootID = "mutated-source-root"

	snapshot, err := client.SnapshotBootstrapProfile()
	if err != nil {
		t.Fatalf("SnapshotBootstrapProfile() error = %v", err)
	}
	if got, want := snapshot.Session.RequestHeaders.Get("ms-dyn-sid"), "bootstrap-header-secret"; got != want {
		t.Fatalf("snapshot ms-dyn-sid = %q, want %q", got, want)
	}
	if got, want := snapshot.Session.Cookies[0].Value, "bootstrap-cookie-secret"; got != want {
		t.Fatalf("snapshot cookie = %q, want %q", got, want)
	}
	if got, want := snapshot.NewReport.RootID, "bootstrap-workspace"; got != want {
		t.Fatalf("snapshot root = %q, want %q", got, want)
	}

	snapshot.Session.RequestHeaders.Set("ms-dyn-sid", "mutated-snapshot-header")
	snapshot.Session.Cookies[0].Value = "mutated-snapshot-cookie"
	second, err := client.BootstrapProfile()
	if err != nil {
		t.Fatalf("BootstrapProfile() error = %v", err)
	}
	if got := second.Session.RequestHeaders.Get("ms-dyn-sid"); got != "bootstrap-header-secret" {
		t.Fatalf("second snapshot header aliases first snapshot: %q", got)
	}
	if got := second.Session.Cookies[0].Value; got != "bootstrap-cookie-secret" {
		t.Fatalf("second snapshot cookie aliases first snapshot: %q", got)
	}
}

func TestDraftHTTPPathMergesCookieAndHeaderRotation(t *testing.T) {
	const (
		rotatedHeader = "rotated-draft-header"
		rotatedCookie = "rotated-draft-cookie"
	)
	stage := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		stage++
		if stage == 1 {
			if got := request.Header.Get("ms-dyn-sid"); got != "bootstrap-header-secret" {
				t.Errorf("initial ms-dyn-sid = %q", got)
			}
			if got := stateTestCookieValue(request, "DynamicsOwinAuth"); got != "bootstrap-cookie-secret" {
				t.Errorf("initial auth cookie = %q", got)
			}
			w.Header().Set("ms-dyn-sid", rotatedHeader)
			http.SetCookie(w, &http.Cookie{Name: "DynamicsOwinAuth", Value: rotatedCookie, Path: "/", Secure: true, HttpOnly: true})
		} else {
			if got := request.Header.Get("ms-dyn-sid"); got != rotatedHeader {
				t.Errorf("stage %d ms-dyn-sid = %q, want rotated value", stage, got)
			}
			if got := stateTestCookieValue(request, "DynamicsOwinAuth"); got != rotatedCookie {
				t.Errorf("stage %d auth cookie = %q, want rotated value", stage, got)
			}
		}

		var envelope dynamics.Envelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		last := envelope.Messages[len(envelope.Messages)-1].SequenceNumber
		w.Header().Set("Content-Type", "application/json")
		switch stage {
		case 1:
			stateTestWriteEnvelope(t, w, stateTestResponseEnvelope(7, last, 51,
				stateTestViewModel(map[string]any{
					"Id": "dialog", "Name": dynamics.FormExpenseNewExpenseReport, "TypeName": "Dialog",
					"ChildViewModels": []any{map[string]any{"Id": "purpose", "Name": dynamics.ControlNamePurpose, "TypeName": "Input"}},
				}),
			))
		case 2:
			stateTestWriteEnvelope(t, w, stateTestResponseEnvelope(7, last, 52,
				stateTestViewModel(map[string]any{
					"Id": "details", "Name": dynamics.FormExpenseReportDetails, "TypeName": "Form",
					"ChildModelCollections": map[string]any{"TrvExpTable_ds": map[string]any{"Items": []any{map[string]any{"Item": map[string]any{
						"Id": "record", "Properties": map[string]any{"dataSourceName_internal": "TrvExpTable_ds", "ExpNumber_field": "ER-ROTATE", "expenseReportStatus_dataMethod": "Draft"},
					}}}}},
					"ChildViewModels": []any{map[string]any{"Id": "save", "Name": dynamics.ControlSaveAndClose, "TypeName": "CommandButton"}},
				}),
			))
		case 3:
			stateTestWriteEnvelope(t, w, stateTestResponseEnvelope(7, last, 53))
		default:
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	client, err := NewFromBootstrap(stateTestBootstrapProfile(server.URL), WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewFromBootstrap() error = %v", err)
	}
	if _, err := client.CreateDraft(context.Background(), "Rotated session state"); err != nil {
		t.Fatalf("CreateDraft() error = %v", err)
	}

	snapshot, err := client.SnapshotBootstrapProfile()
	if err != nil {
		t.Fatalf("SnapshotBootstrapProfile() error = %v", err)
	}
	if got := snapshot.Session.RequestHeaders.Get("ms-dyn-sid"); got != rotatedHeader {
		t.Fatalf("snapshot ms-dyn-sid = %q", got)
	}
	if got := stateTestProfileCookie(snapshot.Session.Cookies, "DynamicsOwinAuth"); got != rotatedCookie {
		t.Fatalf("snapshot auth cookie = %q", got)
	}
	if got, want := snapshot.Session.LastServerSequence, int64(53); got != want {
		t.Fatalf("LastServerSequence = %d, want %d", got, want)
	}
	if got, want := snapshot.Session.NextClientSequence, int64(84); got != want {
		t.Fatalf("NextClientSequence = %d, want %d", got, want)
	}
}

func TestReceiptHTTPPathMergesCookieAndHeaderRotation(t *testing.T) {
	const (
		rotatedHeader = "rotated-receipt-header"
		rotatedCookie = "rotated-receipt-cookie"
	)
	stage := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		stage++
		if stage == 1 {
			w.Header().Set("ms-dyn-sid", rotatedHeader)
			http.SetCookie(w, &http.Cookie{Name: "DynamicsOwinAuth", Value: rotatedCookie, Path: "/", Secure: true, HttpOnly: true})
		} else {
			if got := request.Header.Get("ms-dyn-sid"); got != rotatedHeader {
				t.Errorf("second receipt ms-dyn-sid = %q", got)
			}
			if got := stateTestCookieValue(request, "DynamicsOwinAuth"); got != rotatedCookie {
				t.Errorf("second receipt auth cookie = %q", got)
			}
		}
		var envelope dynamics.Envelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		last := envelope.Messages[len(envelope.Messages)-1].SequenceNumber
		w.Header().Set("Content-Type", "application/json")
		stateTestWriteEnvelope(t, w, stateTestResponseEnvelope(7, last, int64(50+stage)))
	}))
	defer server.Close()

	client, err := NewReceiptClient(stateTestReceiptProfile(server.URL), WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewReceiptClient() error = %v", err)
	}
	targets := dynamics.ReceiptCommandTargets{DetailsRootID: "details", SaveAndCloseID: "save"}
	for index := 0; index < 2; index++ {
		message := dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, "details", "save")
		if _, err := client.sendReceipt(context.Background(), []dynamics.Message{message}, targets); err != nil {
			t.Fatalf("sendReceipt(%d) error = %v", index+1, err)
		}
	}
	if got := client.headers.Get("ms-dyn-sid"); got != rotatedHeader {
		t.Fatalf("receipt client ms-dyn-sid = %q", got)
	}
	if got := stateTestProfileCookie(client.cookies, "DynamicsOwinAuth"); got != rotatedCookie {
		t.Fatalf("receipt client auth cookie = %q", got)
	}
}

func TestSyncReceiptSessionCopiesHeadersCookiesAndSequences(t *testing.T) {
	clientHeaders := make(http.Header)
	clientHeaders.Set("ms-dyn-sid", "old")
	receiptHeaders := make(http.Header)
	receiptHeaders.Set("ms-dyn-sid", "rotated")
	client := &Client{
		headers: clientHeaders,
		cookies: []*http.Cookie{{Name: "DynamicsOwinAuth", Value: "old", Path: "/"}},
	}
	receiptClient := &ReceiptClient{
		headers:            receiptHeaders,
		cookies:            []*http.Cookie{{Name: "DynamicsOwinAuth", Value: "rotated", Path: "/"}},
		lastServerSequence: 91,
		nextClientSequence: 92,
	}

	client.syncReceiptSession(receiptClient)
	if got := client.headers.Get("ms-dyn-sid"); got != "rotated" {
		t.Fatalf("synced header = %q", got)
	}
	if got := stateTestProfileCookie(client.cookies, "DynamicsOwinAuth"); got != "rotated" {
		t.Fatalf("synced cookie = %q", got)
	}
	if client.lastServerSequence != 91 || client.nextClientSequence != 92 {
		t.Fatalf("synced sequences = %d/%d", client.lastServerSequence, client.nextClientSequence)
	}

	receiptClient.headers.Set("ms-dyn-sid", "mutated")
	receiptClient.cookies[0].Value = "mutated"
	if client.headers.Get("ms-dyn-sid") != "rotated" || client.cookies[0].Value != "rotated" {
		t.Fatal("synced state aliases receipt client state")
	}
}

func TestBuiltinReceiptUploadContractAndMaxSize(t *testing.T) {
	contract := BuiltinReceiptUploadContract()
	if err := contract.validate(); err != nil {
		t.Fatalf("BuiltinReceiptUploadContract().validate() error = %v", err)
	}
	if got, want := contract.MaxSupportedSingleFileSize(), int64(1024000); got != want {
		t.Fatalf("MaxSupportedSingleFileSize() = %d, want %d", got, want)
	}
	if !reflect.DeepEqual(contract, DefaultReceiptUploadContract()) {
		t.Fatal("default and built-in contracts differ")
	}
	if got := (ReceiptUploadContract{}).MaxSupportedSingleFileSize(); got != 0 {
		t.Fatalf("zero-value max size = %d, want 0", got)
	}
}

func stateTestBootstrapProfile(baseURL string) *capture.BootstrapProfile {
	return &capture.BootstrapProfile{
		Session: capture.SessionProfile{
			BaseURL:     baseURL,
			EndpointURL: baseURL + processMessagesPath + "?cmp=USMF&lng=en-us",
			RequestHeaders: http.Header{
				"Accept":           []string{"application/json"},
				"Content-Type":     []string{"application/json; charset=UTF-8"},
				"Origin":           []string{baseURL},
				"Referer":          []string{baseURL + "/?cmp=USMF"},
				"X-Requested-With": []string{"XMLHttpRequest"},
				"ms-dyn-aid":       []string{"bootstrap-header-secret"},
				"ms-dyn-bsid":      []string{"bootstrap-header-secret"},
				"ms-dyn-csrftoken": []string{"bootstrap-header-secret"},
				"ms-dyn-sid":       []string{"bootstrap-header-secret"},
			},
			Cookies: []*http.Cookie{
				{Name: "DynamicsOwinAuth", Value: "bootstrap-cookie-secret", Path: "/", Secure: true, HttpOnly: true},
				{Name: "ms-dyn-csrftoken", Value: "bootstrap-cookie-secret", Path: "/", Secure: true},
			},
			Company:            "USMF",
			Language:           "en-us",
			ChannelID:          7,
			LastServerSequence: 50,
			NextClientSequence: 80,
		},
		NewReport: capture.CommandTarget{
			CommandName: dynamics.CommandClick,
			RootID:      "bootstrap-workspace",
			TargetID:    "bootstrap-new-report",
			ControlName: dynamics.SelectedControlNewExpenseReportReportsTab,
		},
	}
}

func stateTestReceiptProfile(baseURL string) *capture.ReceiptProfile {
	bootstrap := stateTestBootstrapProfile(baseURL)
	return &capture.ReceiptProfile{
		Session:           bootstrap.Session,
		ReportNumber:      "RPT-STATE",
		ReportStatus:      "Draft",
		ReceiptCount:      0,
		DetailsFormRootID: "details",
		AddReceipts:       capture.CommandTarget{CommandName: dynamics.CommandClick, RootID: "details", TargetID: "add", ControlName: dynamics.ControlNewReceiptButton},
		SaveAndClose:      capture.CommandTarget{CommandName: dynamics.CommandClick, RootID: "details", TargetID: "save", ControlName: dynamics.ControlSaveAndClose},
		Expected: capture.ReceiptExpectedNames{
			DetailsForm: dynamics.FormExpenseReportDetails, AddReceiptForm: dynamics.FormExpenseAddNewReceipt,
			AddReceiptsControl: dynamics.ControlNewReceiptButton, UploadControl: dynamics.ControlUploadControl,
			OKControl: dynamics.ControlOKButtonAddNewTabPage, ReceiptCountControl: dynamics.ControlReceiptCount,
			SaveAndCloseControl: dynamics.ControlSaveAndClose,
		},
		Upload: capture.ReceiptUploadProfile{
			EndpointPath: "/filemanagement", MultipartFieldOrder: append([]string(nil), receiptMultipartFieldOrder...),
			MaxChunkSize: maxReceiptFileSize, DocumentType: receiptDocumentType, MaxSupportedSingleFileSize: maxReceiptFileSize,
		},
	}
}

func stateTestResponseEnvelope(channelID int, acknowledged, sequence int64, interactions ...json.RawMessage) dynamics.Envelope {
	return dynamics.Envelope{
		ChannelID:                      channelID,
		LastAcknowledgedSequenceNumber: acknowledged,
		Messages: []dynamics.Message{{
			SequenceNumber: sequence,
			Interactions:   interactions,
		}},
	}
}

func stateTestViewModel(descriptor map[string]any) json.RawMessage {
	return stateTestRaw(map[string]any{"$type": "CreateViewModelInteraction", "RootId": "", "Descriptor": descriptor})
}

func stateTestRaw(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func stateTestWriteEnvelope(t *testing.T, writer http.ResponseWriter, envelope dynamics.Envelope) {
	t.Helper()
	body, err := dynamics.MarshalEnvelope(envelope)
	if err != nil {
		t.Fatalf("MarshalEnvelope() error = %v", err)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatalf("write response: %v", err)
	}
}

func stateTestCookieValue(request *http.Request, name string) string {
	for _, cookie := range request.Cookies() {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func stateTestProfileCookie(cookies []*http.Cookie, name string) string {
	for _, cookie := range cookies {
		if cookie != nil && cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}
