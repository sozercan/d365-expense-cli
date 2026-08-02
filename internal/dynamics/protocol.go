// Package dynamics implements the small, observed subset of the Dynamics 365
// Finance ReliableCommunicationManager JSON protocol needed to create an
// unsubmitted expense-report draft.
//
// The protocol is an internal Dynamics web-client protocol, not a supported
// public API. The types in this package deliberately retain unknown JSON fields
// so captures remain forward-compatible enough to inspect and clone.
package dynamics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	InteractionTypeCommand = "CommandInteraction"

	CommandUpdateLastSelectedControl = "UpdateLastSelectedControl"
	CommandClick                     = "Click"
	CommandSetValue                  = "SetValue"
	CommandExecuteShortcuts          = "ExecuteShortcuts"
	CommandActivateTab               = "ActivateTab"

	ShortcutInvokeDefaultButton = "InvokeDefaultButton"

	SelectedControlNewExpenseReportReportsTab = "NewExpenseReportReportsTab"

	FormExpenseNewExpenseReport = "ExpenseNewExpenseReport_form"
	FormExpenseReportDetails    = "ExpenseReportDetails_form"

	ControlNamePurpose  = "NamePurpose"
	ControlDescription  = "Description"
	ControlCreateButton = "CreateButton"
	ControlSaveAndClose = "SaveAndClose"
)

// Envelope is the request or response body sent through
// ReliableCommunicationManager.svc/ProcessMessages.
//
// In a request, LastAcknowledgedSequenceNumber acknowledges the latest server
// Message.SequenceNumber processed by the client. In a response, it
// acknowledges the latest client Message.SequenceNumber processed by the
// server.
type Envelope struct {
	ChannelID                      int                        `json:"ChannelId"`
	CompanyID                      string                     `json:"CompanyId,omitempty"`
	Language                       string                     `json:"Language,omitempty"`
	LastAcknowledgedSequenceNumber int64                      `json:"LastAcknowledgedSequenceNumber"`
	Messages                       []Message                  `json:"Messages"`
	ExtraFields                    map[string]json.RawMessage `json:"-"`
	reservedFieldConflicts         []string
}

// Message is one sequenced batch of interactions. Interactions are retained as
// raw JSON because server responses contain many heterogeneous interaction
// types. Use ParseCommandInteraction or CommandInteractions for outbound
// CommandInteraction values.
type Message struct {
	SequenceNumber         int64                      `json:"SequenceNumber"`
	Interactions           []json.RawMessage          `json:"Interactions"`
	ExtraFields            map[string]json.RawMessage `json:"-"`
	reservedFieldConflicts []string
}

// CommandInteraction is the observed wire representation of an outbound
// Dynamics command. ShouldBlockOnExecution is a pointer because the observed
// protocol distinguishes an omitted field from false.
type CommandInteraction struct {
	Type                   string                     `json:"$type"`
	CallbackID             string                     `json:"CallbackId"`
	CommandName            string                     `json:"CommandName"`
	FailureCallbackID      string                     `json:"FailureCallbackId"`
	NamedParameters        map[string]any             `json:"NamedParameters"`
	NoAsyncIncrement       bool                       `json:"NoAsyncIncrement"`
	PositionalParameters   []any                      `json:"PositionalParameters"`
	PriorityPosition       bool                       `json:"PriorityPosition"`
	ResetThrottleTime      bool                       `json:"ResetThrottleTime"`
	RootID                 string                     `json:"RootId"`
	ShouldBlockOnExecution *bool                      `json:"ShouldBlockOnExecution,omitempty"`
	TargetID               string                     `json:"TargetId"`
	Throttle               bool                       `json:"Throttle"`
	ThrottleID             string                     `json:"ThrottleId"`
	ThrottleTimestamp      int64                      `json:"ThrottleTimestamp"`
	ThrottleValue          int64                      `json:"ThrottleValue"`
	Telemetry              bool                       `json:"Telemetry"`
	ExtraFields            map[string]json.RawMessage `json:"-"`
	reservedFieldConflicts []string
	presentFields          map[string]bool
}

var (
	envelopeKnownFields = map[string]struct{}{
		"ChannelId": {}, "CompanyId": {}, "Language": {},
		"LastAcknowledgedSequenceNumber": {}, "Messages": {},
	}
	messageKnownFields = map[string]struct{}{
		"SequenceNumber": {}, "Interactions": {},
	}
	commandKnownFields = map[string]struct{}{
		"$type": {}, "CallbackId": {}, "CommandName": {},
		"FailureCallbackId": {}, "NamedParameters": {},
		"NoAsyncIncrement": {}, "PositionalParameters": {},
		"PriorityPosition": {}, "ResetThrottleTime": {}, "RootId": {},
		"ShouldBlockOnExecution": {}, "TargetId": {}, "Throttle": {},
		"ThrottleId": {}, "ThrottleTimestamp": {}, "ThrottleValue": {},
		"Telemetry": {},
	}
)

// ParseEnvelope parses one protocol envelope. Unknown fields are accepted and
// retained in ExtraFields at the envelope, message, and parsed-command levels.
func ParseEnvelope(data []byte) (Envelope, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Envelope{}, fmt.Errorf("parse Dynamics envelope: %w", err)
	}
	return envelope, nil
}

// MarshalEnvelope marshals one protocol envelope, including retained unknown
// fields. Known struct fields always take precedence over an ExtraFields entry
// with the same JSON name.
func MarshalEnvelope(envelope Envelope) ([]byte, error) {
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal Dynamics envelope: %w", err)
	}
	return data, nil
}

// ParseCommandInteraction parses a raw interaction as a CommandInteraction.
// A non-empty $type other than CommandInteraction is rejected.
func ParseCommandInteraction(data json.RawMessage) (CommandInteraction, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return CommandInteraction{}, errors.New("parse Dynamics command interaction: empty JSON")
	}
	var command CommandInteraction
	if err := json.Unmarshal(data, &command); err != nil {
		return CommandInteraction{}, fmt.Errorf("parse Dynamics command interaction: %w", err)
	}
	if command.Type != "" && command.Type != InteractionTypeCommand {
		return CommandInteraction{}, fmt.Errorf("parse Dynamics command interaction: interaction type %q is not %q", command.Type, InteractionTypeCommand)
	}
	return command, nil
}

// MarshalCommandInteraction marshals one command interaction, including
// retained unknown fields.
func MarshalCommandInteraction(command CommandInteraction) (json.RawMessage, error) {
	data, err := json.Marshal(command)
	if err != nil {
		return nil, fmt.Errorf("marshal Dynamics command interaction: %w", err)
	}
	return json.RawMessage(data), nil
}

// CommandInteractions parses every interaction in a message as an outbound
// CommandInteraction. It returns an error for a heterogeneous server-response
// message or malformed interaction.
func (message Message) CommandInteractions() ([]CommandInteraction, error) {
	commands := make([]CommandInteraction, len(message.Interactions))
	for i, raw := range message.Interactions {
		command, err := ParseCommandInteraction(raw)
		if err != nil {
			return nil, fmt.Errorf("interaction %d: %w", i, err)
		}
		if command.Type == "" {
			return nil, fmt.Errorf("interaction %d: missing $type", i)
		}
		commands[i] = command
	}
	return commands, nil
}

func (envelope *Envelope) UnmarshalJSON(data []byte) error {
	type envelopeAlias Envelope
	var value envelopeAlias
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	extra, err := collectExtraFields(data, envelopeKnownFields)
	if err != nil {
		return err
	}
	conflicts, err := collectReservedFieldConflicts(data, envelopeKnownFields)
	if err != nil {
		return err
	}
	*envelope = Envelope(value)
	envelope.ExtraFields = extra
	envelope.reservedFieldConflicts = conflicts
	return nil
}

func (envelope Envelope) MarshalJSON() ([]byte, error) {
	type envelopeAlias Envelope
	return marshalWithExtraFields(envelopeAlias(envelope), envelope.ExtraFields, envelopeKnownFields)
}

func (message *Message) UnmarshalJSON(data []byte) error {
	type messageAlias Message
	var value messageAlias
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	extra, err := collectExtraFields(data, messageKnownFields)
	if err != nil {
		return err
	}
	conflicts, err := collectReservedFieldConflicts(data, messageKnownFields)
	if err != nil {
		return err
	}
	*message = Message(value)
	message.ExtraFields = extra
	message.reservedFieldConflicts = conflicts
	return nil
}

func (message Message) MarshalJSON() ([]byte, error) {
	type messageAlias Message
	return marshalWithExtraFields(messageAlias(message), message.ExtraFields, messageKnownFields)
}

func (command *CommandInteraction) UnmarshalJSON(data []byte) error {
	type commandAlias CommandInteraction
	var value commandAlias
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	extra, err := collectExtraFields(data, commandKnownFields)
	if err != nil {
		return err
	}
	conflicts, err := collectReservedFieldConflicts(data, commandKnownFields)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	*command = CommandInteraction(value)
	command.ExtraFields = extra
	command.reservedFieldConflicts = conflicts
	command.presentFields = make(map[string]bool, len(object))
	for name := range object {
		command.presentFields[name] = true
	}
	return nil
}

func (command CommandInteraction) MarshalJSON() ([]byte, error) {
	type commandAlias CommandInteraction
	return marshalWithExtraFields(commandAlias(command), command.ExtraFields, commandKnownFields)
}

func commandFieldPresent(command CommandInteraction, name string) bool {
	if command.presentFields != nil {
		return command.presentFields[name]
	}
	switch name {
	case "ShouldBlockOnExecution":
		return command.ShouldBlockOnExecution != nil
	default:
		return true
	}
}

func collectExtraFields(data []byte, known map[string]struct{}) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	for name := range known {
		delete(object, name)
	}
	if len(object) == 0 {
		return nil, nil
	}
	return object, nil
}

func collectReservedFieldConflicts(data []byte, known map[string]struct{}) ([]string, error) {
	canonicalByFold := make(map[string]string, len(known))
	for name := range known {
		canonicalByFold[strings.ToLower(name)] = name
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("expected JSON object")
	}

	seen := make(map[string]bool, len(known))
	var conflicts []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("expected JSON object field name")
		}
		folded := strings.ToLower(name)
		if canonical, reserved := canonicalByFold[folded]; reserved {
			if name != canonical {
				conflicts = append(conflicts, fmt.Sprintf("field %q is a case variant of %q", name, canonical))
			}
			if seen[folded] {
				conflicts = append(conflicts, fmt.Sprintf("field %q appears more than once", canonical))
			}
			seen[folded] = true
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return conflicts, nil
}

func marshalWithExtraFields(value any, extra map[string]json.RawMessage, known map[string]struct{}) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return data, nil
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, err
	}
	for name, raw := range extra {
		if _, isKnown := known[name]; isKnown {
			continue
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("extra JSON field %q contains invalid JSON", name)
		}
		object[name] = cloneRawMessage(raw)
	}
	return json.Marshal(object)
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
