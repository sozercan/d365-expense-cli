package dynamics_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func TestParseMarshalEnvelopeToleratesAndPreservesUnknownFields(t *testing.T) {
	fixture := []byte(`{
		"ChannelId":7,
		"CompanyId":"USMF",
		"Language":"en-us",
		"LastAcknowledgedSequenceNumber":41,
		"FutureEnvelope":{"enabled":true},
		"Messages":[{
			"SequenceNumber":99,
			"FutureMessage":"kept",
			"Interactions":[{
				"$type":"CommandInteraction",
				"CallbackId":"17",
				"CommandName":"SetValue",
				"complete":null,
				"FutureCommand":{"version":2},
				"FailureCallbackId":"18",
				"NamedParameters":{},
				"NoAsyncIncrement":false,
				"PositionalParameters":["Conference travel"],
				"PriorityPosition":false,
				"ResetThrottleTime":false,
				"RootId":"dialog-root",
				"TargetId":"purpose-control",
				"Throttle":false,
				"ThrottleId":"0",
				"ThrottleTimestamp":0,
				"ThrottleValue":0,
				"Telemetry":true
			}]
		}]
	}`)

	envelope, err := dynamics.ParseEnvelope(fixture)
	if err != nil {
		t.Fatalf("ParseEnvelope() error = %v", err)
	}
	if got, want := envelope.ChannelID, 7; got != want {
		t.Errorf("ChannelID = %d, want %d", got, want)
	}
	if string(envelope.ExtraFields["FutureEnvelope"]) != `{"enabled":true}` {
		t.Errorf("FutureEnvelope = %s", envelope.ExtraFields["FutureEnvelope"])
	}
	if string(envelope.Messages[0].ExtraFields["FutureMessage"]) != `"kept"` {
		t.Errorf("FutureMessage = %s", envelope.Messages[0].ExtraFields["FutureMessage"])
	}

	command, err := dynamics.ParseCommandInteraction(envelope.Messages[0].Interactions[0])
	if err != nil {
		t.Fatalf("ParseCommandInteraction() error = %v", err)
	}
	for _, name := range []string{"complete", "FutureCommand"} {
		if _, ok := command.ExtraFields[name]; !ok {
			t.Errorf("command ExtraFields missing %q", name)
		}
	}
	commandJSON, err := dynamics.MarshalCommandInteraction(command)
	if err != nil {
		t.Fatalf("MarshalCommandInteraction() error = %v", err)
	}
	var commandObject map[string]json.RawMessage
	if err := json.Unmarshal(commandJSON, &commandObject); err != nil {
		t.Fatal(err)
	}
	if string(commandObject["FutureCommand"]) != `{"version":2}` || string(commandObject["complete"]) != "null" {
		t.Errorf("unknown command fields did not round trip: %s", commandJSON)
	}

	marshaled, err := dynamics.MarshalEnvelope(envelope)
	if err != nil {
		t.Fatalf("MarshalEnvelope() error = %v", err)
	}
	var before, after any
	if err := json.Unmarshal(fixture, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(marshaled, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Errorf("round trip mismatch\n got: %s\nwant: %s", marshaled, fixture)
	}
}

func TestParseEnvelopeLeavesHeterogeneousServerInteractionsRaw(t *testing.T) {
	fixture := []byte(`{"ChannelId":0,"LastAcknowledgedSequenceNumber":28,"Messages":[{"SequenceNumber":16,"Interactions":[{"$type":"CreateViewModelInteraction","Descriptor":{"Id":"root","Name":"Example_form","Future":true}}]}]}`)
	envelope, err := dynamics.ParseEnvelope(fixture)
	if err != nil {
		t.Fatalf("ParseEnvelope() error = %v", err)
	}
	if got := len(envelope.Messages[0].Interactions); got != 1 {
		t.Fatalf("interaction count = %d, want 1", got)
	}
	if _, err := envelope.Messages[0].CommandInteractions(); err == nil {
		t.Fatal("CommandInteractions() unexpectedly accepted server interaction")
	}
}

func TestSequenceHelpers(t *testing.T) {
	first := dynamics.Envelope{LastAcknowledgedSequenceNumber: 27, Messages: []dynamics.Message{{SequenceNumber: 15}, {SequenceNumber: 17}}}
	second := dynamics.Envelope{LastAcknowledgedSequenceNumber: 29, Messages: []dynamics.Message{{SequenceNumber: 16}}}
	if got, want := dynamics.MaxServerSequence(first, second), int64(17); got != want {
		t.Errorf("MaxServerSequence() = %d, want %d", got, want)
	}
	if got, want := dynamics.MaxClientSequence(first, second), int64(29); got != want {
		t.Errorf("MaxClientSequence() = %d, want %d", got, want)
	}
	if got, want := dynamics.MaxMessageSequence(first, second), int64(17); got != want {
		t.Errorf("MaxMessageSequence() = %d, want %d", got, want)
	}
	if got, want := dynamics.MaxAcknowledgedSequence(first, second), int64(29); got != want {
		t.Errorf("MaxAcknowledgedSequence() = %d, want %d", got, want)
	}
}

func TestCloneHelpersAreDeep(t *testing.T) {
	original := dynamics.Envelope{
		ChannelID:   7,
		ExtraFields: map[string]json.RawMessage{"future": json.RawMessage(`{"x":1}`)},
		Messages: []dynamics.Message{{
			SequenceNumber: 9,
			Interactions:   []json.RawMessage{json.RawMessage(`{"$type":"Unknown"}`)},
			ExtraFields:    map[string]json.RawMessage{"messageFuture": json.RawMessage(`true`)},
		}},
	}
	clone := dynamics.CloneEnvelope(original)
	clone.ExtraFields["future"][2] = 'z'
	clone.Messages[0].Interactions[0][2] = 'z'
	clone.Messages[0].ExtraFields["messageFuture"][0] = 'f'
	if string(original.ExtraFields["future"]) != `{"x":1}` || string(original.Messages[0].Interactions[0]) != `{"$type":"Unknown"}` || string(original.Messages[0].ExtraFields["messageFuture"]) != "true" {
		t.Fatal("CloneEnvelope() shares mutable storage with original")
	}

	command := dynamics.CommandInteraction{
		ShouldBlockOnExecution: boolPtr(true),
		NamedParameters: map[string]any{
			"nested": map[string]any{"value": "original"},
		},
		PositionalParameters: []any{[]any{"original"}},
		ExtraFields:          map[string]json.RawMessage{"future": json.RawMessage(`{"x":1}`)},
	}
	commandClone := dynamics.CloneCommandInteraction(command)
	commandClone.NamedParameters["nested"].(map[string]any)["value"] = "changed"
	commandClone.PositionalParameters[0].([]any)[0] = "changed"
	*commandClone.ShouldBlockOnExecution = false
	commandClone.ExtraFields["future"][2] = 'z'
	if command.NamedParameters["nested"].(map[string]any)["value"] != "original" || command.PositionalParameters[0].([]any)[0] != "original" || !*command.ShouldBlockOnExecution || string(command.ExtraFields["future"]) != `{"x":1}` {
		t.Fatal("CloneCommandInteraction() shares mutable storage with original")
	}

	replaced := dynamics.CloneWithMessages(original, 44, dynamics.Message{SequenceNumber: 10})
	if replaced.LastAcknowledgedSequenceNumber != 44 || len(replaced.Messages) != 1 || replaced.Messages[0].SequenceNumber != 10 {
		t.Errorf("CloneWithMessages() = %#v", replaced)
	}
	if original.LastAcknowledgedSequenceNumber != 0 || original.Messages[0].SequenceNumber != 9 {
		t.Fatal("CloneWithMessages() mutated base")
	}
}

func TestBuildersMatchObservedCommandShapes(t *testing.T) {
	open := dynamics.BuildOpenNewExpenseReportMessage(26, "workspace", "new-button")
	openCommands := mustCommands(t, open)
	if len(openCommands) != 2 {
		t.Fatalf("open command count = %d, want 2", len(openCommands))
	}
	if got := openCommands[0]; got.CommandName != dynamics.CommandUpdateLastSelectedControl || got.RootID != "workspace" || got.TargetID != "workspace" || !got.NoAsyncIncrement || got.Telemetry || !reflect.DeepEqual(got.PositionalParameters, []any{dynamics.SelectedControlNewExpenseReportReportsTab}) {
		t.Errorf("UpdateLastSelectedControl = %#v", got)
	}
	if got := openCommands[1]; got.CommandName != dynamics.CommandClick || got.TargetID != "new-button" || !got.Throttle || got.ThrottleID != "workspace_TG" || got.PositionalParameters == nil || len(got.PositionalParameters) != 0 || got.ShouldBlockOnExecution == nil || !*got.ShouldBlockOnExecution {
		t.Errorf("open Click = %#v", got)
	}

	set := mustCommands(t, dynamics.BuildSetValueMessage(27, "dialog", "purpose", "Conference", "17", "18"))[0]
	if set.CommandName != dynamics.CommandSetValue || set.CallbackID != "17" || set.FailureCallbackID != "18" || !reflect.DeepEqual(set.PositionalParameters, []any{"Conference"}) || string(set.ExtraFields["complete"]) != "null" {
		t.Errorf("SetValue = %#v", set)
	}

	invoke := mustCommands(t, dynamics.BuildInvokeDefaultButtonMessage(28, "dialog"))[0]
	if invoke.CommandName != dynamics.CommandExecuteShortcuts || invoke.RootID != "dialog" || invoke.TargetID != "dialog" || !reflect.DeepEqual(invoke.PositionalParameters, []any{dynamics.ShortcutInvokeDefaultButton}) {
		t.Errorf("InvokeDefaultButton = %#v", invoke)
	}

	save := mustCommands(t, dynamics.BuildSaveAndCloseClickMessage(29, "details", "save"))[0]
	if save.CommandName != dynamics.CommandClick || save.RootID != "details" || save.TargetID != "save" || save.PositionalParameters != nil || save.ThrottleID != "details_TG" {
		t.Errorf("SaveAndClose Click = %#v", save)
	}
}

func mustCommands(t *testing.T, message dynamics.Message) []dynamics.CommandInteraction {
	t.Helper()
	commands, err := message.CommandInteractions()
	if err != nil {
		t.Fatalf("CommandInteractions() error = %v", err)
	}
	return commands
}

func boolPtr(value bool) *bool { return &value }

func TestParseEnvelopeAcceptsUTF8BOM(t *testing.T) {
	data := append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"ChannelId":7,"LastAcknowledgedSequenceNumber":1,"Messages":[]}`)...)
	envelope, err := dynamics.ParseEnvelope(data)
	if err != nil {
		t.Fatalf("ParseEnvelope() error = %v", err)
	}
	if envelope.ChannelID != 7 {
		t.Fatalf("ChannelID = %d", envelope.ChannelID)
	}
}
