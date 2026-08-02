package expense

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func TestValidateActivateReceiptsTabEnvelopeAllowsOnlyCapturedShapeAndTargets(t *testing.T) {
	t.Parallel()

	message, err := buildActivateReceiptsTabMessage(42, "details-root", "receipts-tab")
	if err != nil {
		t.Fatal(err)
	}
	valid := dynamics.Envelope{Messages: []dynamics.Message{message}}
	if err := validateActivateReceiptsTabEnvelope(valid, "details-root", "receipts-tab"); err != nil {
		t.Fatalf("valid ActivateTab rejected: %v", err)
	}

	var command map[string]any
	if err := json.Unmarshal(message.Interactions[0], &command); err != nil {
		t.Fatal(err)
	}
	if command["CommandName"] != dynamics.CommandActivateTab || command["RootId"] != "details-root" || command["TargetId"] != "receipts-tab" {
		t.Fatalf("ActivateTab command = %#v", command)
	}
	if command["ResetThrottleTime"] != "true" || command["ThrottleFirst"] != "true" || command["ThrottleId"] != "details-root_TopTabs" {
		t.Fatalf("ActivateTab throttle shape = %#v", command)
	}
	if _, present := command["ShouldBlockOnExecution"]; present {
		t.Fatalf("ActivateTab unexpectedly included ShouldBlockOnExecution: %#v", command)
	}

	mutations := map[string]func(*dynamics.Envelope){
		"wrong root": func(envelope *dynamics.Envelope) {
			envelope.Messages[0].Interactions[0] = mutateActivateField(t, envelope.Messages[0].Interactions[0], "RootId", "other-root")
		},
		"wrong tab": func(envelope *dynamics.Envelope) {
			envelope.Messages[0].Interactions[0] = mutateActivateField(t, envelope.Messages[0].Interactions[0], "TargetId", "other-tab")
		},
		"submit command": func(envelope *dynamics.Envelope) {
			envelope.Messages[0].Interactions[0] = mutateActivateField(t, envelope.Messages[0].Interactions[0], "CommandName", "Submit")
		},
		"extra interaction": func(envelope *dynamics.Envelope) {
			envelope.Messages[0].Interactions = append(envelope.Messages[0].Interactions, json.RawMessage(`{"$type":"CommandInteraction","CommandName":"Submit"}`))
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			envelope := dynamics.CloneEnvelope(valid)
			mutate(&envelope)
			err := validateActivateReceiptsTabEnvelope(envelope, "details-root", "receipts-tab")
			if !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}

func mutateActivateField(t *testing.T, raw json.RawMessage, name string, value any) json.RawMessage {
	t.Helper()
	var command map[string]any
	if err := json.Unmarshal(raw, &command); err != nil {
		t.Fatal(err)
	}
	command[name] = value
	updated, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}
