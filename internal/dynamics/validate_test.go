package dynamics_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func TestValidateDraftCommandsAllowsOnlyObservedDraftRequests(t *testing.T) {
	targets := draftTargets()
	tests := map[string]dynamics.Envelope{
		"open": {
			Messages: []dynamics.Message{dynamics.BuildOpenNewExpenseReportMessage(26, targets.WorkspaceRootID, targets.CreateButtonID)},
		},
		"set and invoke": {
			Messages: []dynamics.Message{
				dynamics.BuildSetValueMessage(27, targets.DialogRootID, targets.NamePurposeID, "Conference travel", "17", "18"),
				dynamics.BuildInvokeDefaultButtonMessage(28, targets.DialogRootID),
			},
		},
		"save and close": {
			Messages: []dynamics.Message{dynamics.BuildSaveAndCloseClickMessage(29, targets.DetailsRootID, targets.SaveAndCloseID)},
		},
	}
	for name, envelope := range tests {
		t.Run(name, func(t *testing.T) {
			if err := dynamics.ValidateDraftCommands(envelope, targets); err != nil {
				t.Fatalf("ValidateDraftCommands() error = %v", err)
			}
		})
	}
}

func TestValidateDraftCommandsRejectsForbiddenCommandNames(t *testing.T) {
	for _, name := range []string{"Submit", "SubmitToWorkflow", "WorkflowAction", "PostExpense", "Approve", "ApprovalComplete", "Recall"} {
		t.Run(name, func(t *testing.T) {
			targets := draftTargets()
			envelope := dynamics.Envelope{Messages: []dynamics.Message{dynamics.BuildSaveAndCloseClickMessage(29, targets.DetailsRootID, targets.SaveAndCloseID)}}
			command := mustCommands(t, envelope.Messages[0])[0]
			command.CommandName = name
			raw, err := dynamics.MarshalCommandInteraction(command)
			if err != nil {
				t.Fatal(err)
			}
			envelope.Messages[0].Interactions[0] = raw
			err = dynamics.ValidateDraftCommands(envelope, targets)
			if !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}

func TestValidateDraftCommandsRejectsShapeAndTargetChanges(t *testing.T) {
	targets := draftTargets()
	validCreate := func() dynamics.Envelope {
		return dynamics.Envelope{Messages: []dynamics.Message{
			dynamics.BuildSetValueMessage(27, targets.DialogRootID, targets.NamePurposeID, "Conference", "17", "18"),
			dynamics.BuildInvokeDefaultButtonMessage(28, targets.DialogRootID),
		}}
	}
	tests := map[string]func() dynamics.Envelope{
		"wrong save target": func() dynamics.Envelope {
			return dynamics.Envelope{Messages: []dynamics.Message{dynamics.BuildSaveAndCloseClickMessage(29, targets.DetailsRootID, "submit-button")}}
		},
		"wrong SetValue target": func() dynamics.Envelope {
			envelope := validCreate()
			command := mustCommands(t, envelope.Messages[0])[0]
			command.TargetID = "description-control"
			envelope.Messages[0].Interactions[0] = mustRawCommand(t, command)
			return envelope
		},
		"wrong shortcut": func() dynamics.Envelope {
			envelope := validCreate()
			command := mustCommands(t, envelope.Messages[1])[0]
			command.PositionalParameters = []any{"InvokeSubmitButton"}
			envelope.Messages[1].Interactions[0] = mustRawCommand(t, command)
			return envelope
		},
		"nonconsecutive sequences": func() dynamics.Envelope {
			envelope := validCreate()
			envelope.Messages[1].SequenceNumber = 30
			return envelope
		},
		"isolated SetValue": func() dynamics.Envelope {
			envelope := validCreate()
			envelope.Messages = envelope.Messages[:1]
			return envelope
		},
		"extra command field": func() dynamics.Envelope {
			envelope := dynamics.Envelope{Messages: []dynamics.Message{dynamics.BuildSaveAndCloseClickMessage(29, targets.DetailsRootID, targets.SaveAndCloseID)}}
			command := mustCommands(t, envelope.Messages[0])[0]
			command.ExtraFields = map[string]json.RawMessage{"ExecuteImmediateDangerous": json.RawMessage(`true`)}
			envelope.Messages[0].Interactions[0] = mustRawCommand(t, command)
			return envelope
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			err := dynamics.ValidateDraftCommands(build(), targets)
			if !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}

func draftTargets() dynamics.DraftCommandTargets {
	return dynamics.DraftCommandTargets{
		WorkspaceRootID: "workspace-root",
		CreateButtonID:  "new-report-button",
		DialogRootID:    "dialog-root",
		NamePurposeID:   "purpose-control",
		DetailsRootID:   "details-root",
		SaveAndCloseID:  "save-control",
	}
}

func mustRawCommand(t *testing.T, command dynamics.CommandInteraction) json.RawMessage {
	t.Helper()
	raw, err := dynamics.MarshalCommandInteraction(command)
	if err != nil {
		t.Fatalf("MarshalCommandInteraction() error = %v", err)
	}
	return raw
}

func TestValidateDraftCommandsRejectsEnvelopeAndMessageExtraFields(t *testing.T) {
	targets := draftTargets()
	valid := func() dynamics.Envelope {
		return dynamics.Envelope{Messages: []dynamics.Message{
			dynamics.BuildSaveAndCloseClickMessage(29, targets.DetailsRootID, targets.SaveAndCloseID),
		}}
	}

	tests := map[string]func() dynamics.Envelope{
		"envelope": func() dynamics.Envelope {
			envelope := valid()
			envelope.ExtraFields = map[string]json.RawMessage{"Future": json.RawMessage(`true`)}
			return envelope
		},
		"message": func() dynamics.Envelope {
			envelope := valid()
			envelope.Messages[0].ExtraFields = map[string]json.RawMessage{"Future": json.RawMessage(`true`)}
			return envelope
		},
	}

	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			if err := dynamics.ValidateDraftCommands(build(), targets); !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}

func TestValidateDraftCommandsRejectsReservedFieldSmuggling(t *testing.T) {
	targets := draftTargets()
	safeEnvelope := dynamics.Envelope{Messages: []dynamics.Message{
		dynamics.BuildSaveAndCloseClickMessage(29, targets.DetailsRootID, targets.SaveAndCloseID),
	}}
	safeMessage, err := json.Marshal(safeEnvelope.Messages[0])
	if err != nil {
		t.Fatal(err)
	}
	safeInteraction := safeEnvelope.Messages[0].Interactions[0]
	maliciousMessage := `{"SequenceNumber":28,"Interactions":[{"$type":"CommandInteraction","CommandName":"Submit"}]}`
	maliciousInteraction := `{"$type":"CommandInteraction","CommandName":"Submit"}`

	tests := map[string]string{
		"lowercase messages":     `{"messages":[` + maliciousMessage + `],"Messages":[` + string(safeMessage) + `]}`,
		"lowercase interactions": `{"Messages":[{"SequenceNumber":29,"interactions":[` + maliciousInteraction + `],"Interactions":[` + string(safeInteraction) + `]}]}`,
		"duplicate Messages":     `{"Messages":[` + maliciousMessage + `],"Messages":[` + string(safeMessage) + `]}`,
		"duplicate Interactions": `{"Messages":[{"SequenceNumber":29,"Interactions":[` + maliciousInteraction + `],"Interactions":[` + string(safeInteraction) + `]}]}`,
		"duplicate CommandName":  `{"Messages":[{"SequenceNumber":29,"Interactions":[{"$type":"CommandInteraction","CommandName":"Submit","CommandName":"Click","CallbackId":"","FailureCallbackId":"","NamedParameters":{},"NoAsyncIncrement":false,"PositionalParameters":null,"PriorityPosition":false,"ResetThrottleTime":false,"RootId":"details-root","ShouldBlockOnExecution":true,"TargetId":"save-control","Throttle":true,"ThrottleId":"details-root_TG","ThrottleTimestamp":0,"ThrottleValue":0,"Telemetry":true}]}]}`,
	}

	for name, fixture := range tests {
		t.Run(name, func(t *testing.T) {
			envelope, err := dynamics.ParseEnvelope([]byte(fixture))
			if err != nil {
				t.Fatalf("ParseEnvelope() error = %v", err)
			}
			if err := dynamics.ValidateDraftCommands(envelope, targets); !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}
