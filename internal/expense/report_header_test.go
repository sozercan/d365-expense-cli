package expense_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func headerRecord(rootID string, fields map[string]any) json.RawMessage {
	fields["dataSourceName_internal"] = dynamics.DataSourceReports
	fields["MasterTrackingId"] = "290"
	return recordUpdate(rootID, fields)
}

func personName(rootID, tracking, name string) json.RawMessage {
	return recordUpdate(rootID, map[string]any{"dataSourceName_internal": "Datasource1_ds", "MasterTrackingId": tracking, "Name_field": name})
}

func approverRow(rootID, tracking, alias string) json.RawMessage {
	return recordUpdate(rootID, map[string]any{"dataSourceName_internal": dynamics.DataSourceInterimApprovers, "MasterTrackingId": tracking, "UserAlias_field": alias})
}

func approverCollection(rootID string, descriptor map[string]any) json.RawMessage {
	descriptor["Name"] = dynamics.DataSourceInterimApprovers
	return mustRaw(map[string]any{"$type": "UpdateModelCollectionInteraction", "RootId": rootID, "Descriptor": descriptor})
}

// approversDialog is one copy of the interim approvers dialog listing Avery
// Example and Drew Example.
func approversDialog(id, prefix string) []json.RawMessage {
	row := func(tracking string) map[string]any {
		return map[string]any{"Item": map[string]any{"Properties": map[string]any{
			"dataSourceName_internal": dynamics.DataSourceInterimApprovers, "MasterTrackingId": tracking,
		}}}
	}
	return []json.RawMessage{
		rootedViewModel(id, map[string]any{
			"Id": id, "Name": dynamics.FormInterimApprovers, "TypeName": "Dialog",
			"ChildModelCollections": map[string]any{dynamics.DataSourceInterimApprovers: map[string]any{
				"Name": dynamics.DataSourceInterimApprovers, "IsLastPage": true, "ActiveMasterTrackingId": "361",
				"Items": []any{row("361"), row("362")},
			}},
			"ChildViewModels": []any{
				control(prefix+"grid", dynamics.ControlApproverGrid), control(prefix+"new", dynamics.ControlApproverNewButton),
				control(prefix+"delete", dynamics.ControlApproverDeleteButton), control(prefix+"default", "SetAsDefaultInterimApprovers"),
				control(prefix+"worker", dynamics.ControlApproverWorker), control(prefix+"worker-name", dynamics.ControlApproverWorkerName),
				control(prefix+"alias", dynamics.ControlApproverAlias),
				control(prefix+"ok", dynamics.ControlApproversOK),
			},
		}),
		approverRow(id, "361", "aexample"), approverRow(id, "362", "dexample"),
		personName(id, "361", "Avery Example"), personName(id, "362", "Drew Example"),
	}
}

// headerSteps edit ER-0002 from interim approvers Avery Example and Drew
// Example with final approver Blake Example. finalName is the name Dynamics
// resolves for the requested final approver.
func headerSteps(finalName string) []scriptedStep {
	steps := openSteps(draftLine("160", "Hotel", "2026-10-03", "120.00", "EXAMPLE HOTEL"))
	open := steps[2]
	steps[2] = func(request dynamics.Envelope) []json.RawMessage {
		return append(open(request), controlUpdate("details", control("more", dynamics.ControlMoreActions)))
	}
	return append(steps,
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{controlUpdate("details", control("edit-item", dynamics.ControlEditExpenseReport))}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				rootedViewModel("edit", map[string]any{
					"Id": "edit", "Name": dynamics.FormEditExpenseReport, "TypeName": "Dialog",
					"ChildViewModels": []any{
						control("purpose", dynamics.ControlNamePurpose), control("final", dynamics.ControlFinalApprover),
						control("final-name", dynamics.ControlFinalApproverName), control("select-interim", dynamics.ControlSelectInterimApprovers),
						control("default-final", "Expense_SetAsDefaultFinalApprover"), control("edit-ok", dynamics.ControlEditReportOK),
					},
				}),
				headerRecord("edit", map[string]any{"Txt2_field": "offsite draft", "interimApprovers_dataMethod": "Avery Example, Drew Example"}),
				personName("edit", "290", "Blake Example"),
			}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{headerRecord("edit", map[string]any{"Txt2_field": "Offsite 2030"})}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{personName("edit", "290", finalName)}
		},
		func(dynamics.Envelope) []json.RawMessage {
			// Dynamics opens the dialog twice; the newer copy is used.
			return append(approversDialog("337_0", "a-"), approversDialog("342_0", "b-")...)
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{approverCollection("342_0", map[string]any{"ActiveMasterTrackingId": "362"})}
		},
		// Deleting a saved row asks for confirmation.
		confirmStep,
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				deleteViewModel("confirm"),
				approverCollection("342_0", map[string]any{"IsLastPage": true, "ActiveMasterTrackingId": "733", "SpliceOperations": []any{
					map[string]any{"Index": 0, "Delete": 0, "Insert": []any{}, "ClearCache": true},
					map[string]any{"Index": 0, "Delete": 2, "Insert": []any{map[string]any{"Item": map[string]any{"Properties": map[string]any{"MasterTrackingId": "733"}}}}},
				}}),
				approverRow("342_0", "733", "aexample"), personName("342_0", "733", "Avery Example"),
			}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				approverCollection("342_0", map[string]any{"IsLastPage": true, "ActiveMasterTrackingId": "745", "SpliceOperations": []any{
					map[string]any{"Index": 1, "Delete": 0, "Insert": []any{map[string]any{"Item": map[string]any{"Properties": map[string]any{"MasterTrackingId": "745"}}}}},
				}}),
				approverRow("342_0", "745", ""), personName("342_0", "745", ""),
			}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{approverRow("342_0", "745", "bexample"), personName("342_0", "745", "Blake Example")}
		},
		func(dynamics.Envelope) []json.RawMessage {
			// Dynamics re-reads the report under a new tracking ID.
			return []json.RawMessage{
				deleteViewModel("342_0"),
				recordUpdate("edit", map[string]any{"dataSourceName_internal": dynamics.DataSourceReports, "MasterTrackingId": "492",
					"interimApprovers_dataMethod": "Avery Example, Blake Example"}),
				personName("edit", "492", finalName),
			}
		},
		func(dynamics.Envelope) []json.RawMessage { return []json.RawMessage{deleteViewModel("337_0")} },
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{deleteViewModel("edit"), recordUpdate("details", map[string]any{
				"dataSourceName_internal": dynamics.DataSourceReports, "MasterTrackingId": "149", "interimApprovers_dataMethod": "Avery Example, Blake Example",
			})}
		},
		workspaceStep,
	)
}

func offsiteHeader() expense.ReportHeaderInput {
	return expense.ReportHeaderInput{
		Title:            "Offsite 2030",
		InterimApprovers: []string{"Avery Example", "bexample"},
		FinalApprover:    "Casey Example",
	}
}

func TestEditDraftReportHeaderKeepsMatchingApproversAndReplacesTheRest(t *testing.T) {
	t.Parallel()
	server, emitted := scriptedServer(t, headerSteps("Casey Example"))
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	header, err := client.EditDraftReportHeader(context.Background(), "ER-0002", offsiteHeader())
	if err != nil {
		t.Fatalf("EditDraftReportHeader() error = %v", err)
	}
	if want := (expense.ReportHeader{Title: "Offsite 2030", InterimApprovers: []string{"Avery Example", "Blake Example"}, FinalApprover: "Casey Example"}); !reflect.DeepEqual(header, want) {
		t.Fatalf("EditDraftReportHeader() = %#v, want %#v", header, want)
	}
	if got, want := (*emitted)[3:], []string{
		"Click:more", "Click:edit-item", "SetValue:purpose", "SetValue:final-name", "ResolveChanges:final",
		"Click:select-interim", "ChangeSelectedIndex:b-grid",
		"UpdateLastSelectedControl:342_0", "Click:b-delete", "Click:yes",
		"UpdateLastSelectedControl:342_0", "Click:b-new", "SetValue:b-alias",
		"Click:b-ok", "Click:a-ok", "Click:edit-ok", "Click:save-and-close",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted commands = %v, want %v", got, want)
	}
}

func TestEditDraftReportHeaderStopsWhenTheFinalApproverDoesNotResolve(t *testing.T) {
	t.Parallel()
	server, emitted := scriptedServer(t, headerSteps("Blake Example"))
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	_, err := client.EditDraftReportHeader(context.Background(), "ER-0002", offsiteHeader())
	if err == nil || !strings.Contains(err.Error(), `did not resolve final approver "Casey Example"`) {
		t.Fatalf("EditDraftReportHeader() error = %v", err)
	}
	if got := *emitted; got[len(got)-1] != "ResolveChanges:final" {
		t.Fatalf("continued after an unresolved final approver: %v", got)
	}
}

func TestPlanReportHeaderRejectsInvalidNames(t *testing.T) {
	t.Parallel()
	for name, input := range map[string]expense.ReportHeaderInput{
		"empty":             {},
		"comma":             {FinalApprover: "Example, Casey"},
		"duplicate":         {InterimApprovers: []string{"Avery Example", "avery example"}},
		"empty list":        {InterimApprovers: []string{}},
		"surrounding space": {InterimApprovers: []string{" Avery Example"}},
	} {
		if _, err := expense.PlanReportHeader(input); err == nil {
			t.Errorf("%s: PlanReportHeader() accepted %#v", name, input)
		}
	}
	if _, err := expense.PlanReportHeader(offsiteHeader()); err != nil {
		t.Fatalf("PlanReportHeader() error = %v", err)
	}
}

// TestEditDraftReportHeaderReplacesTheOnlyApprover deletes the only saved row,
// which Dynamics reports as a delete-only splice instead of a reload.
func TestEditDraftReportHeaderReplacesTheOnlyApprover(t *testing.T) {
	t.Parallel()
	steps := headerSteps("Blake Example")[:5] // open the report and the edit dialog
	steps[4] = func(dynamics.Envelope) []json.RawMessage {
		return []json.RawMessage{
			rootedViewModel("edit", map[string]any{
				"Id": "edit", "Name": dynamics.FormEditExpenseReport, "TypeName": "Dialog",
				"ChildViewModels": []any{
					control("purpose", dynamics.ControlNamePurpose), control("final", dynamics.ControlFinalApprover),
					control("final-name", dynamics.ControlFinalApproverName), control("select-interim", dynamics.ControlSelectInterimApprovers),
					control("edit-ok", dynamics.ControlEditReportOK),
				},
			}),
			headerRecord("edit", map[string]any{"Txt2_field": "offsite draft", "interimApprovers_dataMethod": "Avery Example"}),
			personName("edit", "290", "Blake Example"),
		}
	}
	steps = append(steps,
		func(dynamics.Envelope) []json.RawMessage {
			dialog := approversDialog("342_0", "b-")
			// Keep only Avery Example's row.
			return []json.RawMessage{
				rootedViewModel("342_0", map[string]any{
					"Id": "342_0", "Name": dynamics.FormInterimApprovers, "TypeName": "Dialog",
					"ChildModelCollections": map[string]any{dynamics.DataSourceInterimApprovers: map[string]any{
						"Name": dynamics.DataSourceInterimApprovers, "IsLastPage": true, "ActiveMasterTrackingId": "361",
						"Items": []any{map[string]any{"Item": map[string]any{"Properties": map[string]any{
							"dataSourceName_internal": dynamics.DataSourceInterimApprovers, "MasterTrackingId": "361",
						}}}},
					}},
					"ChildViewModels": []any{
						control("b-grid", dynamics.ControlApproverGrid), control("b-new", dynamics.ControlApproverNewButton),
						control("b-delete", dynamics.ControlApproverDeleteButton), control("b-worker", dynamics.ControlApproverWorker),
						control("b-worker-name", dynamics.ControlApproverWorkerName), control("b-alias", dynamics.ControlApproverAlias),
						control("b-ok", dynamics.ControlApproversOK),
					},
				}),
				dialog[1], dialog[3],
			}
		},
		confirmStep,
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				deleteViewModel("confirm"),
				approverCollection("342_0", map[string]any{"IsLastPage": false, "ActiveMasterTrackingId": "0", "SpliceOperations": []any{
					map[string]any{"Index": 0, "Delete": 1, "Insert": []any{}},
					map[string]any{"Index": 0, "Delete": 1, "Insert": []any{}, "MasterTrackingIndex": "361"},
				}}),
			}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				approverCollection("342_0", map[string]any{"IsLastPage": true, "ActiveMasterTrackingId": "745", "SpliceOperations": []any{
					map[string]any{"Index": 0, "Delete": 0, "Insert": []any{map[string]any{"Item": map[string]any{"Properties": map[string]any{"MasterTrackingId": "745"}}}}},
				}}),
				approverRow("342_0", "745", ""),
			}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{approverRow("342_0", "745", "cexample"), personName("342_0", "745", "Casey Example")}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{deleteViewModel("342_0"), recordUpdate("edit", map[string]any{
				"dataSourceName_internal": dynamics.DataSourceReports, "MasterTrackingId": "492", "interimApprovers_dataMethod": "Casey Example",
			})}
		},
		func(dynamics.Envelope) []json.RawMessage { return []json.RawMessage{deleteViewModel("edit")} },
		workspaceStep,
	)
	server, emitted := scriptedServer(t, steps)
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	header, err := client.EditDraftReportHeader(context.Background(), "ER-0002", expense.ReportHeaderInput{InterimApprovers: []string{"Casey Example"}})
	if err != nil {
		t.Fatalf("EditDraftReportHeader() error = %v", err)
	}
	if !reflect.DeepEqual(header.InterimApprovers, []string{"Casey Example"}) {
		t.Fatalf("interim approvers = %q", header.InterimApprovers)
	}
	if got, want := (*emitted)[3:], []string{
		"Click:more", "Click:edit-item", "Click:select-interim",
		"UpdateLastSelectedControl:342_0", "Click:b-delete", "Click:yes",
		"UpdateLastSelectedControl:342_0", "Click:b-new", "SetValue:b-worker-name", "ResolveChanges:b-worker",
		"Click:b-ok", "Click:edit-ok", "Click:save-and-close",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted commands = %v, want %v", got, want)
	}
}

// TestEditDraftReportHeaderFillsThePlaceholderRow sets the first approver of
// a report that has none: Dynamics opens the dialog with one blank row.
func TestEditDraftReportHeaderFillsThePlaceholderRow(t *testing.T) {
	t.Parallel()
	steps := headerSteps("Blake Example")[:5]
	steps[4] = func(dynamics.Envelope) []json.RawMessage {
		return []json.RawMessage{
			rootedViewModel("edit", map[string]any{
				"Id": "edit", "Name": dynamics.FormEditExpenseReport, "TypeName": "Dialog",
				"ChildViewModels": []any{
					control("purpose", dynamics.ControlNamePurpose), control("final", dynamics.ControlFinalApprover),
					control("final-name", dynamics.ControlFinalApproverName), control("select-interim", dynamics.ControlSelectInterimApprovers),
					control("edit-ok", dynamics.ControlEditReportOK),
				},
			}),
			headerRecord("edit", map[string]any{"Txt2_field": "offsite draft", "interimApprovers_dataMethod": ""}),
		}
	}
	steps = append(steps,
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{
				rootedViewModel("342_0", map[string]any{
					"Id": "342_0", "Name": dynamics.FormInterimApprovers, "TypeName": "Dialog",
					"ChildModelCollections": map[string]any{dynamics.DataSourceInterimApprovers: map[string]any{
						"Name": dynamics.DataSourceInterimApprovers, "IsLastPage": true, "ActiveMasterTrackingId": "645",
						"Items": []any{map[string]any{"Item": map[string]any{"Properties": map[string]any{
							"dataSourceName_internal": dynamics.DataSourceInterimApprovers, "MasterTrackingId": "645", "UserAlias_field": "",
						}}}},
					}},
					"ChildViewModels": []any{
						control("b-grid", dynamics.ControlApproverGrid), control("b-new", dynamics.ControlApproverNewButton),
						control("b-delete", dynamics.ControlApproverDeleteButton), control("b-worker", dynamics.ControlApproverWorker),
						control("b-worker-name", dynamics.ControlApproverWorkerName), control("b-alias", dynamics.ControlApproverAlias),
						control("b-ok", dynamics.ControlApproversOK),
					},
				}),
				personName("342_0", "645", ""),
			}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{approverRow("342_0", "645", "bexample"), personName("342_0", "645", "Blake Example")}
		},
		func(dynamics.Envelope) []json.RawMessage {
			return []json.RawMessage{deleteViewModel("342_0"), headerRecord("edit", map[string]any{"interimApprovers_dataMethod": "Blake Example"})}
		},
		func(dynamics.Envelope) []json.RawMessage { return []json.RawMessage{deleteViewModel("edit")} },
		workspaceStep,
	)
	server, emitted := scriptedServer(t, steps)
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	if _, err := client.EditDraftReportHeader(context.Background(), "ER-0002", expense.ReportHeaderInput{InterimApprovers: []string{"Blake Example"}}); err != nil {
		t.Fatalf("EditDraftReportHeader() error = %v", err)
	}
	if got, want := (*emitted)[3:], []string{
		"Click:more", "Click:edit-item", "Click:select-interim", "SetValue:b-worker-name", "ResolveChanges:b-worker",
		"Click:b-ok", "Click:edit-ok", "Click:save-and-close",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted commands = %v, want %v", got, want)
	}
}

func TestEditDraftReportHeaderAsksForAnAliasWhenANameIsAmbiguous(t *testing.T) {
	t.Parallel()
	steps := headerSteps("Casey Example")[:12] // through New on the approvers dialog
	steps = append(steps, func(dynamics.Envelope) []json.RawMessage {
		return []json.RawMessage{rootedViewModel("lookup", map[string]any{"Id": "lookup", "Name": dynamics.FormWorkerLookup, "TypeName": "Popup"})}
	})
	server, emitted := scriptedServer(t, steps)
	client := deleteClient(t, server.URL, true, expense.WithHTTPClient(server.Client()))

	input := offsiteHeader()
	input.InterimApprovers = []string{"Avery Example", "Blake Example"}
	_, err := client.EditDraftReportHeader(context.Background(), "ER-0002", input)
	if err == nil || !strings.Contains(err.Error(), `more than one worker is named "Blake Example"; give their user alias instead`) {
		t.Fatalf("EditDraftReportHeader() error = %v; emitted %v", err, *emitted)
	}
	if got := *emitted; got[len(got)-1] != "ResolveChanges:b-worker" {
		t.Fatalf("continued after an ambiguous name: %v", got)
	}
}
