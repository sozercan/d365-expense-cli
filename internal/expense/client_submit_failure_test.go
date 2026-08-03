package expense_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func TestCreateAndSubmitRequiresVerifiedStatusAndRestoredWorkspace(t *testing.T) {
	tests := []struct {
		name              string
		finalInteractions []json.RawMessage
		wantError         string
	}{
		{
			name: "missing target status",
			finalInteractions: []json.RawMessage{
				submitWorkspaceInteraction(),
			},
			wantError: "did not verify",
		},
		{
			name: "still draft",
			finalInteractions: []json.RawMessage{
				submitStatusInteraction("1"),
				submitWorkspaceInteraction(),
			},
			wantError: "still reports",
		},
		{
			name: "unmodeled status label",
			finalInteractions: []json.RawMessage{
				submitStatusInteraction("Approved"),
				submitWorkspaceInteraction(),
			},
			wantError: "did not affirmatively report",
		},
		{
			name: "unmodeled status code",
			finalInteractions: []json.RawMessage{
				submitStatusInteraction("3"),
				submitWorkspaceInteraction(),
			},
			wantError: "did not affirmatively report",
		},
		{
			name: "workspace not restored",
			finalInteractions: []json.RawMessage{
				submitStatusInteraction("2"),
			},
			wantError: "did not uniquely restore",
		},
		{
			name: "ambiguous restored workspaces",
			finalInteractions: []json.RawMessage{
				submitStatusInteraction("2"),
				submitWorkspaceInteraction(),
				viewModelInteraction(map[string]any{
					"Id": "stale-workspace", "Name": dynamics.FormExpenseWorkspace, "TypeName": "Form",
					"ChildViewModels": []any{map[string]any{
						"Id": "stale-new-report", "Name": dynamics.SelectedControlNewExpenseReportReportsTab, "TypeName": "MenuItemButton",
					}},
				}),
			},
			wantError: "did not uniquely restore",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stage := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				stage++
				var envelope dynamics.Envelope
				if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
					t.Fatal(err)
				}
				last := envelope.Messages[len(envelope.Messages)-1].SequenceNumber
				w.Header().Set("Content-Type", "application/json")
				switch stage {
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
							"Id": "submit-details", "Name": dynamics.FormExpenseReportDetails, "TypeName": "Form",
							"ChildModelCollections": map[string]any{"TrvExpTable_ds": map[string]any{"Items": []any{map[string]any{"Item": map[string]any{
								"Id": "record", "Properties": map[string]any{"dataSourceName_internal": "TrvExpTable_ds", "ExpNumber_field": "ER-FAIL", "expenseReportStatus_dataMethod": "Draft"},
							}}}}},
							"ChildViewModels": []any{
								map[string]any{"Id": "submit-save", "Name": dynamics.ControlSaveAndClose, "TypeName": "CommandButton"},
								submitButtonDescriptor("submit-target"),
							},
						}),
					))
				case 3:
					writeEnvelope(t, w, responseEnvelope(7, last, 53, test.finalInteractions...))
				default:
					t.Fatalf("unexpected stage %d", stage)
				}
			}))
			defer server.Close()

			client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
			if err != nil {
				t.Fatal(err)
			}
			request := expense.CreateReportRequest{Purpose: "Unverified submit", FinalAction: expense.ReportFinalActionSubmit}
			if _, err := client.CreateReport(context.Background(), request); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
			if stage != 3 {
				t.Fatalf("requests = %d, want 3", stage)
			}
		})
	}
}

func TestCreateAndSubmitMarksLateAuthenticationFailureUncertain(t *testing.T) {
	stage := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		stage++
		var envelope dynamics.Envelope
		if err := json.NewDecoder(request.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		last := envelope.Messages[len(envelope.Messages)-1].SequenceNumber
		w.Header().Set("Content-Type", "application/json")
		switch stage {
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
					"Id": "submit-details", "Name": dynamics.FormExpenseReportDetails, "TypeName": "Form",
					"ChildModelCollections": map[string]any{"TrvExpTable_ds": map[string]any{"Items": []any{map[string]any{"Item": map[string]any{
						"Id": "record", "Properties": map[string]any{"dataSourceName_internal": "TrvExpTable_ds", "ExpNumber_field": "ER-AUTH", "expenseReportStatus_dataMethod": "Draft"},
					}}}}},
					"ChildViewModels": []any{
						map[string]any{"Id": "submit-save", "Name": dynamics.ControlSaveAndClose, "TypeName": "CommandButton"},
						submitButtonDescriptor("submit-target"),
					},
				}),
			))
		case 3:
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Fatalf("unexpected stage %d", stage)
		}
	}))
	defer server.Close()

	client, err := expense.New(validProfile(server.URL), expense.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateReport(context.Background(), expense.CreateReportRequest{
		Purpose: "Late auth failure", FinalAction: expense.ReportFinalActionSubmit,
	})
	if !errors.Is(err, expense.ErrAuthenticationExpired) || !errors.Is(err, expense.ErrOperationUncertain) {
		t.Fatalf("error = %v, want authentication-expired and operation-uncertain markers", err)
	}
}

func submitStatusInteraction(status string) json.RawMessage {
	return mustRaw(map[string]any{"$type": "UpdateModelInteraction", "RootId": "workspace", "Descriptor": map[string]any{
		"Id": "submitted-record", "Properties": map[string]any{"ExpNumber_field": "ER-FAIL", "ApprovalStatus_field": status},
	}})
}

func submitWorkspaceInteraction() json.RawMessage {
	return viewModelInteraction(map[string]any{
		"Id": "workspace-after-submit", "Name": dynamics.FormExpenseWorkspace, "TypeName": "Form",
		"ChildViewModels": []any{map[string]any{
			"Id": "new-report-after-submit", "Name": dynamics.SelectedControlNewExpenseReportReportsTab, "TypeName": "MenuItemButton",
		}},
	})
}
