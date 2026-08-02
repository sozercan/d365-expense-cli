package dynamics

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// MaxServerSequence returns the largest Message.SequenceNumber in the supplied
// server response envelopes.
func MaxServerSequence(envelopes ...Envelope) int64 {
	return MaxMessageSequence(envelopes...)
}

// MaxClientSequence returns the largest client sequence acknowledged by the
// supplied server response envelopes.
func MaxClientSequence(envelopes ...Envelope) int64 {
	return MaxAcknowledgedSequence(envelopes...)
}

// MaxMessageSequence returns the largest Message.SequenceNumber in the
// supplied envelopes. It is useful for server responses and for captured
// client requests when the direction is already known by the caller.
func MaxMessageSequence(envelopes ...Envelope) int64 {
	var maximum int64
	for _, envelope := range envelopes {
		for _, message := range envelope.Messages {
			if message.SequenceNumber > maximum {
				maximum = message.SequenceNumber
			}
		}
	}
	return maximum
}

// MaxAcknowledgedSequence returns the largest
// LastAcknowledgedSequenceNumber in the supplied envelopes.
func MaxAcknowledgedSequence(envelopes ...Envelope) int64 {
	var maximum int64
	for _, envelope := range envelopes {
		if envelope.LastAcknowledgedSequenceNumber > maximum {
			maximum = envelope.LastAcknowledgedSequenceNumber
		}
	}
	return maximum
}

// CloneEnvelope returns a deep clone of an envelope.
func CloneEnvelope(envelope Envelope) Envelope {
	clone := envelope
	clone.ExtraFields = cloneRawMap(envelope.ExtraFields)
	if envelope.Messages != nil {
		clone.Messages = make([]Message, len(envelope.Messages))
		for i, message := range envelope.Messages {
			clone.Messages[i] = CloneMessage(message)
		}
	}
	return clone
}

// CloneMessage returns a deep clone of a message.
func CloneMessage(message Message) Message {
	clone := message
	clone.ExtraFields = cloneRawMap(message.ExtraFields)
	if message.Interactions != nil {
		clone.Interactions = make([]json.RawMessage, len(message.Interactions))
		for i, interaction := range message.Interactions {
			clone.Interactions[i] = cloneRawMessage(interaction)
		}
	}
	return clone
}

// CloneCommandInteraction returns a deep clone of a command interaction.
func CloneCommandInteraction(command CommandInteraction) CommandInteraction {
	clone := command
	if command.ShouldBlockOnExecution != nil {
		value := *command.ShouldBlockOnExecution
		clone.ShouldBlockOnExecution = &value
	}
	clone.NamedParameters = cloneStringAnyMap(command.NamedParameters)
	clone.PositionalParameters = cloneAnySlice(command.PositionalParameters)
	clone.ExtraFields = cloneRawMap(command.ExtraFields)
	if command.presentFields != nil {
		clone.presentFields = make(map[string]bool, len(command.presentFields))
		for name, present := range command.presentFields {
			clone.presentFields[name] = present
		}
	}
	return clone
}

// CloneWithMessages deep-clones base, replaces its acknowledgement and
// messages, and leaves the caller's base and message values untouched.
func CloneWithMessages(base Envelope, lastServerSequence int64, messages ...Message) Envelope {
	clone := CloneEnvelope(base)
	clone.LastAcknowledgedSequenceNumber = lastServerSequence
	if messages == nil {
		clone.Messages = nil
		return clone
	}
	clone.Messages = make([]Message, len(messages))
	for i, message := range messages {
		clone.Messages[i] = CloneMessage(message)
	}
	return clone
}

// BuildOpenNewExpenseReportMessage creates the observed two-command message
// that selects the expense-report tab and clicks the workspace's new-report
// button.
func BuildOpenNewExpenseReportMessage(sequence int64, rootID, createButtonID string) Message {
	return BuildUpdateLastSelectedControlClickMessage(
		sequence,
		rootID,
		createButtonID,
		SelectedControlNewExpenseReportReportsTab,
	)
}

// BuildUpdateLastSelectedControlClickMessage creates the observed paired
// UpdateLastSelectedControl + Click command message.
func BuildUpdateLastSelectedControlClickMessage(sequence int64, rootID, targetID, selectedControl string) Message {
	update := baseCommand(CommandUpdateLastSelectedControl, rootID, rootID)
	update.NoAsyncIncrement = true
	update.PositionalParameters = []any{selectedControl}
	update.Telemetry = false

	click := baseCommand(CommandClick, rootID, targetID)
	click.PositionalParameters = []any{}
	click.ShouldBlockOnExecution = boolPointer(true)
	click.Throttle = true
	click.ThrottleID = rootID + "_TG"
	click.Telemetry = true

	return commandMessage(sequence, update, click)
}

// BuildSetValueMessage creates the observed NamePurpose SetValue command. The
// callback IDs are session-scoped and therefore supplied by the caller.
func BuildSetValueMessage(sequence int64, rootID, targetID, value, callbackID, failureCallbackID string) Message {
	command := baseCommand(CommandSetValue, rootID, targetID)
	command.CallbackID = callbackID
	command.FailureCallbackID = failureCallbackID
	command.PositionalParameters = []any{value}
	command.Telemetry = true
	command.ExtraFields = map[string]json.RawMessage{
		"complete": json.RawMessage("null"),
	}
	return commandMessage(sequence, command)
}

// BuildInvokeDefaultButtonMessage creates the observed ExecuteShortcuts
// InvokeDefaultButton command against the new-expense dialog root.
func BuildInvokeDefaultButtonMessage(sequence int64, rootID string) Message {
	command := baseCommand(CommandExecuteShortcuts, rootID, rootID)
	command.PositionalParameters = []any{ShortcutInvokeDefaultButton}
	command.ShouldBlockOnExecution = boolPointer(true)
	command.Telemetry = true
	return commandMessage(sequence, command)
}

// BuildSaveAndCloseClickMessage creates the observed Click command for the
// details form's SaveAndClose control. Its null PositionalParameters value is
// intentional and matches the observed wire shape.
func BuildSaveAndCloseClickMessage(sequence int64, rootID, targetID string) Message {
	command := baseCommand(CommandClick, rootID, targetID)
	command.PositionalParameters = nil
	command.ShouldBlockOnExecution = boolPointer(true)
	command.Throttle = true
	command.ThrottleID = rootID + "_TG"
	command.Telemetry = true
	return commandMessage(sequence, command)
}

func baseCommand(name, rootID, targetID string) CommandInteraction {
	return CommandInteraction{
		Type:                 InteractionTypeCommand,
		CallbackID:           "",
		CommandName:          name,
		FailureCallbackID:    "",
		NamedParameters:      map[string]any{},
		NoAsyncIncrement:     false,
		PositionalParameters: nil,
		PriorityPosition:     false,
		ResetThrottleTime:    false,
		RootID:               rootID,
		TargetID:             targetID,
		Throttle:             false,
		ThrottleID:           "0",
		ThrottleTimestamp:    0,
		ThrottleValue:        0,
		Telemetry:            false,
	}
}

func commandMessage(sequence int64, commands ...CommandInteraction) Message {
	message := Message{
		SequenceNumber: sequence,
		Interactions:   make([]json.RawMessage, len(commands)),
	}
	for i, command := range commands {
		raw, err := MarshalCommandInteraction(command)
		if err != nil {
			// All builder-owned values are JSON-marshalable. A panic here means a
			// programming error in this package rather than capture input.
			panic(fmt.Sprintf("dynamics: marshal built command: %v", err))
		}
		message.Interactions[i] = raw
	}
	return message
}

func boolPointer(value bool) *bool {
	return &value
}

func cloneRawMap(source map[string]json.RawMessage) map[string]json.RawMessage {
	if source == nil {
		return nil
	}
	clone := make(map[string]json.RawMessage, len(source))
	for key, value := range source {
		clone[key] = cloneRawMessage(value)
	}
	return clone
}

func cloneStringAnyMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = cloneAny(value)
	}
	return clone
}

func cloneAnySlice(source []any) []any {
	if source == nil {
		return nil
	}
	clone := make([]any, len(source))
	for i, value := range source {
		clone[i] = cloneAny(value)
	}
	return clone
}

func cloneAny(value any) any {
	switch typed := value.(type) {
	case nil, bool, string, float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		json.Number:
		return typed
	case json.RawMessage:
		return cloneRawMessage(typed)
	case []any:
		return cloneAnySlice(typed)
	case map[string]any:
		return cloneStringAnyMap(typed)
	case map[string]json.RawMessage:
		return cloneRawMap(typed)
	default:
		// Named parameters in this observed protocol are JSON values. The
		// marshal/unmarshal fallback handles uncommon JSON-compatible named
		// slices and maps without sharing their backing storage.
		data, err := json.Marshal(typed)
		if err != nil {
			return typed
		}
		var cloned any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&cloned); err != nil {
			return typed
		}
		return cloned
	}
}
