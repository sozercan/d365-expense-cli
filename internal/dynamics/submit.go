package dynamics

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// ControlSubmitButton is the details-form control that invokes the captured
	// TrvSubmit action menu item.
	ControlSubmitButton = "SubmitButton"

	// MenuItemSubmit is the captured Dynamics action menu item behind
	// ControlSubmitButton.
	MenuItemSubmit = "TrvSubmit"
)

const (
	submitButtonType       = "Button"
	submitButtonLabel      = "Submit"
	submitMenuItemType     = "Action"
	submitPrimaryModelName = "TrvExpTable_ds"
	submitServiceBoundary  = "TrvExpTable"
	submitClickValueType   = "Navigate"
	submitThrottleGroup    = "TG"
)

// SubmitCommandTargets is the session-scoped allowlist used by
// ValidateSubmitCommands.
type SubmitCommandTargets struct {
	DetailsRootID  string
	SubmitButtonID string
}

// BuildSubmitClickMessage builds the inferred one-click Submit request. The
// null PositionalParameters value matches captured blocking, throttled details
// form clicks; no successful Submit request was present in the captures.
func BuildSubmitClickMessage(sequence int64, rootID, targetID string) Message {
	command := baseCommand(CommandClick, rootID, targetID)
	command.PositionalParameters = nil
	command.ShouldBlockOnExecution = boolPointer(true)
	command.Throttle = true
	command.ThrottleID = rootID + "_TG"
	command.Telemetry = true
	return commandMessage(sequence, command)
}

// ValidateSubmitCommands permits exactly one blocking, throttled Click against
// the discovered details-form SubmitButton. It does not permit generic submit,
// workflow, approval, post, or recall command names.
func ValidateSubmitCommands(envelope Envelope, targets SubmitCommandTargets) error {
	if err := validateForbiddenCommandInvariant(envelope); err != nil {
		return err
	}
	if len(envelope.Messages) != 1 {
		return unsafe(-1, -1, "", "submit envelope must contain exactly one message")
	}
	commands, err := envelope.Messages[0].CommandInteractions()
	if err != nil {
		return unsafe(0, -1, "", err.Error())
	}
	if len(commands) != 1 {
		return unsafe(0, -1, "", "submit message must contain exactly one Click command")
	}
	command := commands[0]
	if targets.DetailsRootID == "" || targets.SubmitButtonID == "" {
		return unsafe(0, 0, command.CommandName, "submit command targets are incomplete")
	}
	if err := validateCommon(command, CommandClick, targets.DetailsRootID, targets.SubmitButtonID); err != nil {
		return unsafe(0, 0, command.CommandName, err.Error())
	}
	if command.NoAsyncIncrement || !command.Telemetry || !command.Throttle ||
		command.ThrottleID != targets.DetailsRootID+"_TG" || !truePointer(command.ShouldBlockOnExecution) ||
		command.PositionalParameters != nil || len(command.ExtraFields) != 0 {
		return unsafe(0, 0, command.CommandName, "Click shape does not match the inferred Submit flow")
	}
	return nil
}

// ValidateSubmitButton verifies the captured identity, availability, and Click
// command metadata for the details-form SubmitButton before its dynamic ID is
// admitted to SubmitCommandTargets.
func ValidateSubmitButton(node ModelNode, detailsRootID string) error {
	if detailsRootID == "" {
		return errors.New("submit button details root is empty")
	}
	if node.ID == "" {
		return errors.New("submit button ID is empty")
	}
	if node.Name != ControlSubmitButton {
		return fmt.Errorf("submit button name is %q, want %q", node.Name, ControlSubmitButton)
	}
	if node.TypeName != submitButtonType {
		return fmt.Errorf("submit button type is %q, want %q", node.TypeName, submitButtonType)
	}
	if node.RootID != detailsRootID {
		return fmt.Errorf("submit button root is %q, want details root %q", node.RootID, detailsRootID)
	}

	for _, expectation := range []struct {
		properties map[string]json.RawMessage
		name       string
		want       string
	}{
		{node.ValueProperties, "Label", submitButtonLabel},
		{node.ValueProperties, "MenuItemName", MenuItemSubmit},
		{node.ValueProperties, "MenuItemType", submitMenuItemType},
		{node.ValueProperties, "PrimaryModelName", submitPrimaryModelName},
		{node.ValueProperties, "ServiceBoundary", submitServiceBoundary},
		{node.SerializedValueProperties, "Enabled", "true"},
		{node.SerializedValueProperties, "Visible", "true"},
		{node.SerializedValueProperties, "SaveRecord", "true"},
	} {
		if err := requireModelString(expectation.properties, expectation.name, expectation.want); err != nil {
			return fmt.Errorf("submit button metadata: %w", err)
		}
	}

	click, ok := node.Commands[CommandClick]
	if !ok {
		return errors.New("submit button metadata: Click command descriptor is missing")
	}
	if err := validateSubmitClickDescriptor(click); err != nil {
		return fmt.Errorf("submit button metadata: %w", err)
	}
	return nil
}

func validateSubmitClickDescriptor(raw json.RawMessage) error {
	object, err := rawJSONObject(raw)
	if err != nil {
		return fmt.Errorf("Click command descriptor is invalid: %w", err)
	}
	if err := requireExactJSONFields(object, "CommandName", "ParameterBindings", "Properties", "ValueTypeName"); err != nil {
		return fmt.Errorf("Click command descriptor: %w", err)
	}
	if err := requireModelString(object, "CommandName", CommandClick); err != nil {
		return fmt.Errorf("Click command descriptor: %w", err)
	}
	if err := requireModelString(object, "ValueTypeName", submitClickValueType); err != nil {
		return fmt.Errorf("Click command descriptor: %w", err)
	}

	bindings, err := rawJSONObject(object["ParameterBindings"])
	if err != nil {
		return fmt.Errorf("Click ParameterBindings is invalid: %w", err)
	}
	if len(bindings) != 0 {
		return errors.New("Click ParameterBindings must be empty")
	}

	properties, err := rawJSONObject(object["Properties"])
	if err != nil {
		return fmt.Errorf("Click Properties is invalid: %w", err)
	}
	if err := requireExactJSONFields(properties, "ExecuteImmediate", "ShouldBlockOnExecution", "Telemetry", "ThrottleGroup"); err != nil {
		return fmt.Errorf("Click Properties: %w", err)
	}
	for _, name := range []string{"ExecuteImmediate", "ShouldBlockOnExecution"} {
		if err := requireModelBool(properties, name, true); err != nil {
			return fmt.Errorf("Click Properties: %w", err)
		}
	}
	if err := requireModelString(properties, "Telemetry", "true"); err != nil {
		return fmt.Errorf("Click Properties: %w", err)
	}
	if err := requireModelString(properties, "ThrottleGroup", submitThrottleGroup); err != nil {
		return fmt.Errorf("Click Properties: %w", err)
	}
	return nil
}

func rawJSONObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, errors.New("missing object")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errors.New("value is not an object")
	}
	return object, nil
}

func requireExactJSONFields(object map[string]json.RawMessage, names ...string) error {
	if len(object) != len(names) {
		return errors.New("field set differs from captured metadata")
	}
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return fmt.Errorf("required field %q is missing", name)
		}
	}
	return nil
}

func requireModelString(properties map[string]json.RawMessage, name, want string) error {
	raw, ok := properties[name]
	if !ok {
		return fmt.Errorf("required field %q is missing", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("field %q is not a string", name)
	}
	if value != want {
		return fmt.Errorf("field %q is %q, want %q", name, value, want)
	}
	return nil
}

func requireModelBool(properties map[string]json.RawMessage, name string, want bool) error {
	raw, ok := properties[name]
	if !ok {
		return fmt.Errorf("required field %q is missing", name)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("field %q is not a boolean", name)
	}
	if value != want {
		return fmt.Errorf("field %q is %t, want %t", name, value, want)
	}
	return nil
}
