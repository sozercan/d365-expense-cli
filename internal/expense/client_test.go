package expense_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/capture"
	"github.com/sozercan/d365-expense-cli/internal/dynamics"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

const processMessagesPath = "/Services/ReliableCommunicationManager.svc/ProcessMessages"

func TestCreateReportSaveDraftUsesResponseIDsAndOnlySavesAndCloses(t *testing.T) {
	t.Parallel()

	var requestNumber atomic.Int32
	var emitted []string
	var emittedMu sync.Mutex

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != processMessagesPath || r.URL.Query().Get("cmp") != "USMF" || r.URL.Query().Get("lng") != "en-us" {
			t.Errorf("request URL = %s", r.URL.RequestURI())
		}
		if got, want := r.Header.Get("Origin"), "https://"+r.Host; got != want {
			t.Errorf("Origin = %q, want %q", got, want)
		}
		for _, name := range []string{"ms-dyn-aid", "ms-dyn-bsid", "ms-dyn-csrftoken", "ms-dyn-sid"} {
			if got := r.Header.Get(name); got != "unit-header-secret" {
				t.Errorf("header %s was not applied", name)
			}
		}
		cookies := make(map[string]string)
		for _, cookie := range r.Cookies() {
			cookies[cookie.Name] = cookie.Value
		}
		for _, name := range []string{"ms-dyn-csrftoken", "DynamicsOwinAuth"} {
			if cookies[name] != "unit-cookie-secret" {
				t.Errorf("cookie %s was not applied", name)
			}
		}

		var request dynamics.Envelope
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if request.ChannelID != 7 || request.CompanyID != "USMF" || request.Language != "en-us" {
			t.Errorf("request session metadata = %#v", request)
		}
		for _, message := range request.Messages {
			commands, err := message.CommandInteractions()
			if err != nil {
				t.Errorf("parse commands: %v", err)
				continue
			}
			emittedMu.Lock()
			for _, command := range commands {
				emitted = append(emitted, command.CommandName+":"+command.TargetID)
				if strings.Contains(strings.ToLower(command.CommandName), "submit") {
					t.Errorf("forbidden Submit command emitted: %s", command.CommandName)
				}
			}
			emittedMu.Unlock()
		}

		w.Header().Set("Content-Type", "application/json")
		switch requestNumber.Add(1) {
		case 1:
			if request.LastAcknowledgedSequenceNumber != 50 || len(request.Messages) != 1 || request.Messages[0].SequenceNumber != 80 {
				t.Errorf("open request sequences = %#v", request)
			}
			commands := mustCommands(t, request.Messages[0])
			if len(commands) != 2 || commands[0].CommandName != dynamics.CommandUpdateLastSelectedControl || commands[1].CommandName != dynamics.CommandClick || commands[1].RootID != "captured-workspace-root" || commands[1].TargetID != "captured-new-button" {
				t.Errorf("open commands = %#v", commands)
			}
			writeEnvelope(t, w, responseEnvelope(7, 80, 51,
				viewModelInteraction(map[string]any{
					"Id":       "dynamic-dialog-root",
					"Name":     dynamics.FormExpenseNewExpenseReport,
					"TypeName": "Dialog",
					"ChildViewModels": []any{
						map[string]any{"Id": "dynamic-purpose", "Name": dynamics.ControlNamePurpose, "TypeName": "Input"},
					},
				}),
			))
		case 2:
			if request.LastAcknowledgedSequenceNumber != 51 || len(request.Messages) != 2 || request.Messages[0].SequenceNumber != 81 || request.Messages[1].SequenceNumber != 82 {
				t.Errorf("create request sequences = %#v", request)
			}
			set := mustCommands(t, request.Messages[0])
			invoke := mustCommands(t, request.Messages[1])
			if len(set) != 1 || set[0].CommandName != dynamics.CommandSetValue || set[0].RootID != "dynamic-dialog-root" || set[0].TargetID != "dynamic-purpose" || !reflect.DeepEqual(set[0].PositionalParameters, []any{"Conference travel"}) {
				t.Errorf("SetValue command = %#v", set)
			}
			if len(invoke) != 1 || invoke[0].CommandName != dynamics.CommandExecuteShortcuts || invoke[0].RootID != "dynamic-dialog-root" || invoke[0].TargetID != "dynamic-dialog-root" || !reflect.DeepEqual(invoke[0].PositionalParameters, []any{dynamics.ShortcutInvokeDefaultButton}) {
				t.Errorf("InvokeDefaultButton command = %#v", invoke)
			}
			writeEnvelope(t, w, responseEnvelope(7, 82, 52,
				viewModelInteraction(map[string]any{
					"Id":       "dynamic-details-root",
					"Name":     dynamics.FormExpenseReportDetails,
					"TypeName": "Form",
					"ChildModelCollections": map[string]any{
						"TrvExpTable_ds": map[string]any{"Items": []any{map[string]any{"Item": map[string]any{
							"Id": "record", "Properties": map[string]any{
								"dataSourceName_internal":        "TrvExpTable_ds",
								"ExpNumber_field":                "ER-0042",
								"expenseReportStatus_dataMethod": "Draft",
							},
						}}}},
					},
					"ChildViewModels": []any{
						map[string]any{"Id": "dynamic-save", "Name": dynamics.ControlSaveAndClose, "TypeName": "CommandButton"},
						map[string]any{"Id": "dynamic-submit", "Name": "SubmitButton", "TypeName": "CommandButton"},
					},
				}),
			))
		case 3:
			if request.LastAcknowledgedSequenceNumber != 52 || len(request.Messages) != 1 || request.Messages[0].SequenceNumber != 83 {
				t.Errorf("save request sequences = %#v", request)
			}
			commands := mustCommands(t, request.Messages[0])
			if len(commands) != 1 || commands[0].CommandName != dynamics.CommandClick || commands[0].RootID != "dynamic-details-root" || commands[0].TargetID != "dynamic-save" {
				t.Errorf("save commands = %#v", commands)
			}
			writeEnvelope(t, w, responseEnvelope(7, 83, 53,
				viewModelInteraction(map[string]any{"Id": "workspace-after-save", "Name": "ExpenseWorkspace_form", "TypeName": "Form"}),
			))
		default:
			t.Errorf("unexpected request %d", requestNumber.Load())
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	profile := validProfile(server.URL)
	client, err := expense.New(profile, expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := expense.CreateReportRequest{Purpose: "Conference travel", FinalAction: expense.ReportFinalActionSaveDraft}
	plan, err := client.PlanCreateReport(request)
	if err != nil {
		t.Fatalf("PlanCreateReport() error = %v", err)
	}
	if plan.Purpose != "Conference travel" || plan.RequestCount != 3 || requestNumber.Load() != 0 {
		t.Fatalf("PlanCreateReport() = %#v, requests = %d", plan, requestNumber.Load())
	}
	planText := fmt.Sprintf("%#v", plan)
	for _, secret := range []string{"unit-header-secret", "unit-cookie-secret", "captured-workspace-root", server.URL} {
		if strings.Contains(planText, secret) {
			t.Fatalf("plan exposed %q: %s", secret, planText)
		}
	}

	report, err := client.CreateReport(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateReport() error = %v", err)
	}
	if want := (expense.ReportResult{Purpose: "Conference travel", ReportNumber: "ER-0042", Status: "Draft", SavedAndClosed: true}); report != want {
		t.Fatalf("CreateReport() = %#v, want %#v", report, want)
	}
	if requestNumber.Load() != 3 {
		t.Fatalf("requests = %d, want 3", requestNumber.Load())
	}

	emittedMu.Lock()
	defer emittedMu.Unlock()
	if got, want := emitted, []string{
		"UpdateLastSelectedControl:captured-workspace-root",
		"Click:captured-new-button",
		"SetValue:dynamic-purpose",
		"ExecuteShortcuts:dynamic-dialog-root",
		"Click:dynamic-save",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted commands = %v, want %v", got, want)
	}
}

func validProfile(baseURL string) *capture.Profile {
	return &capture.Profile{
		Session: capture.SessionProfile{
			BaseURL:     baseURL,
			EndpointURL: baseURL + processMessagesPath + "?cmp=USMF&lng=en-us",
			RequestHeaders: http.Header{
				"Accept":           []string{"application/json"},
				"Content-Type":     []string{"application/json; charset=UTF-8"},
				"Origin":           []string{baseURL},
				"Referer":          []string{baseURL + "/?cmp=USMF"},
				"X-Requested-With": []string{"XMLHttpRequest"},
				"ms-dyn-aid":       []string{"unit-header-secret"},
				"ms-dyn-bsid":      []string{"unit-header-secret"},
				"ms-dyn-csrftoken": []string{"unit-header-secret"},
				"ms-dyn-sid":       []string{"unit-header-secret"},
			},
			Cookies: []*http.Cookie{
				{Name: "ms-dyn-csrftoken", Value: "unit-cookie-secret", Path: "/", Secure: true},
				{Name: "DynamicsOwinAuth", Value: "unit-cookie-secret", Path: "/", Secure: true, HttpOnly: true},
			},
			Company:            "USMF",
			Language:           "en-us",
			ChannelID:          7,
			LastServerSequence: 50,
			NextClientSequence: 80,
		},
		Draft: capture.DraftFlow{
			NewReport: capture.CommandTarget{CommandName: dynamics.CommandClick, RootID: "captured-workspace-root", TargetID: "captured-new-button", ControlName: dynamics.SelectedControlNewExpenseReportReportsTab},
			CreateDraft: capture.CreateDraftRequest{
				SetValue:            capture.CommandTarget{CommandName: dynamics.CommandSetValue, RootID: "captured-dialog", TargetID: "captured-purpose", ControlName: dynamics.ControlNamePurpose},
				InvokeDefaultButton: capture.CommandTarget{CommandName: dynamics.CommandExecuteShortcuts, RootID: "captured-dialog", TargetID: "captured-dialog", ControlName: dynamics.FormExpenseNewExpenseReport},
			},
			SaveAndClose: capture.CommandTarget{CommandName: dynamics.CommandClick, RootID: "captured-details", TargetID: "captured-save", ControlName: dynamics.ControlSaveAndClose},
		},
	}
}

func responseEnvelope(channelID int, acknowledged, sequence int64, interactions ...json.RawMessage) dynamics.Envelope {
	return dynamics.Envelope{
		ChannelID:                      channelID,
		LastAcknowledgedSequenceNumber: acknowledged,
		Messages: []dynamics.Message{{
			SequenceNumber: sequence,
			Interactions:   interactions,
		}},
	}
}

func viewModelInteraction(descriptor map[string]any) json.RawMessage {
	return mustRaw(map[string]any{"$type": "CreateViewModelInteraction", "Descriptor": descriptor})
}

func mustRaw(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func writeEnvelope(t *testing.T, w http.ResponseWriter, envelope dynamics.Envelope) {
	t.Helper()
	data, err := dynamics.MarshalEnvelope(envelope)
	if err != nil {
		t.Errorf("marshal response: %v", err)
		return
	}
	if _, err := w.Write(data); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func mustCommands(t *testing.T, message dynamics.Message) []dynamics.CommandInteraction {
	t.Helper()
	commands, err := message.CommandInteractions()
	if err != nil {
		t.Errorf("CommandInteractions() error = %v", err)
		return nil
	}
	return commands
}

func TestNewRejectsSequenceWithoutReportCreationHeadroom(t *testing.T) {
	profile := validProfile("https://example.test")
	profile.Session.NextClientSequence = math.MaxInt64 - 2
	if _, err := expense.New(profile); err == nil || !strings.Contains(err.Error(), "headroom") {
		t.Fatalf("New() error = %v, want sequence headroom error", err)
	}
}

func TestCreateAndSubmitUsesExactDiscoveredSubmitButton(t *testing.T) {
	t.Parallel()

	var requestNumber atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request dynamics.Envelope
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		last := request.Messages[len(request.Messages)-1].SequenceNumber
		w.Header().Set("Content-Type", "application/json")
		switch requestNumber.Add(1) {
		case 1:
			writeEnvelope(t, w, responseEnvelope(7, last, 51,
				viewModelInteraction(map[string]any{
					"Id": "submit-dialog", "Name": dynamics.FormExpenseNewExpenseReport, "TypeName": "Dialog",
					"ChildViewModels": []any{map[string]any{"Id": "submit-purpose", "Name": dynamics.ControlNamePurpose, "TypeName": "Input"}},
				}),
			))
		case 2:
			writeEnvelope(t, w, responseEnvelope(7, last, 52,
				viewModelInteraction(map[string]any{
					"Id": "unrelated-root", "Name": "Unrelated_form", "TypeName": "Form",
					"ChildViewModels": []any{submitButtonDescriptor("unrelated-submit")},
				}),
				viewModelInteraction(map[string]any{
					"Id": "submit-details", "Name": dynamics.FormExpenseReportDetails, "TypeName": "Form",
					"ChildModelCollections": map[string]any{"TrvExpTable_ds": map[string]any{"Items": []any{map[string]any{"Item": map[string]any{
						"Id": "submit-record", "Properties": map[string]any{"dataSourceName_internal": "TrvExpTable_ds", "ExpNumber_field": "ER-SUBMIT", "expenseReportStatus_dataMethod": "Draft"},
					}}}}},
					"ChildViewModels": []any{
						map[string]any{"Id": "submit-save", "Name": dynamics.ControlSaveAndClose, "TypeName": "CommandButton"},
						submitButtonDescriptor("submit-target"),
					},
				}),
			))
		case 3:
			commands := mustCommands(t, request.Messages[0])
			if len(commands) != 1 {
				t.Errorf("submit commands = %#v", commands)
				break
			}
			command := commands[0]
			if command.CommandName != dynamics.CommandClick || command.RootID != "submit-details" || command.TargetID != "submit-target" || command.PositionalParameters != nil {
				t.Errorf("submit command = %#v", command)
			}
			writeEnvelope(t, w, responseEnvelope(7, last, 53,
				mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "workspace", "Descriptor": map[string]any{
					"Id": "submitted-record", "Properties": map[string]any{"ExpNumber_field": "ER-SUBMIT", "ApprovalStatus_field": "2"},
				}}),
				viewModelInteraction(map[string]any{
					"Id": "workspace-after-submit", "Name": dynamics.FormExpenseWorkspace, "TypeName": "Form",
					"ChildViewModels": []any{map[string]any{
						"Id": "new-report-after-submit", "Name": dynamics.SelectedControlNewExpenseReportReportsTab, "TypeName": "MenuItemButton",
					}},
				}),
			))
		default:
			t.Errorf("unexpected request %d", requestNumber.Load())
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := expense.CreateReportRequest{Purpose: "Conference travel", FinalAction: expense.ReportFinalActionSubmit}
	plan, err := client.PlanCreateReport(request)
	if err != nil {
		t.Fatalf("PlanCreateReport() error = %v", err)
	}
	if plan.RequestCount != 3 || !strings.Contains(strings.Join(plan.Actions, " "), "SubmitButton") || requestNumber.Load() != 0 {
		t.Fatalf("PlanCreateReport() = %#v, requests=%d", plan, requestNumber.Load())
	}

	report, err := client.CreateReport(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateReport() error = %v", err)
	}
	want := expense.ReportResult{Purpose: "Conference travel", ReportNumber: "ER-SUBMIT", Status: "2", Submitted: true}
	if report != want {
		t.Fatalf("CreateReport() = %#v, want %#v", report, want)
	}
	if requestNumber.Load() != 3 {
		t.Fatalf("requests = %d, want 3", requestNumber.Load())
	}
}

func submitButtonDescriptor(id string) map[string]any {
	return map[string]any{
		"Id": id, "Name": dynamics.ControlSubmitButton, "TypeName": "Button",
		"ValueProperties": map[string]any{
			"Label": "Submit", "MenuItemType": "Action", "MenuItemName": dynamics.MenuItemSubmit,
			"PrimaryModelName": "TrvExpTable_ds", "ServiceBoundary": "TrvExpTable",
		},
		"SerializedValueProperties": map[string]any{
			"SaveRecord": "true", "Visible": "true", "Enabled": "true",
		},
		"Commands": map[string]any{
			"Click": map[string]any{
				"CommandName": "Click",
				"Properties": map[string]any{
					"ThrottleGroup": "TG", "Telemetry": "true", "ExecuteImmediate": true, "ShouldBlockOnExecution": true,
				},
				"ParameterBindings": map[string]any{},
				"ValueTypeName":     "Navigate",
			},
		},
		"ChildViewModels": []any{},
	}
}

func TestCreateReportRejectsInvalidFinalActionBeforeNetwork(t *testing.T) {
	t.Parallel()
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	request := expense.CreateReportRequest{Purpose: "Invalid action"}
	if _, err := client.PlanCreateReport(request); err == nil || !strings.Contains(err.Error(), "final action") {
		t.Fatalf("PlanCreateReport() error = %v", err)
	}
	if _, err := client.CreateReport(context.Background(), request); err == nil || !strings.Contains(err.Error(), "final action") {
		t.Fatalf("CreateReport() error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("network requests = %d, want 0", requests.Load())
	}
}
