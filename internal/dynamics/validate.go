package dynamics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrUnsafeCommand identifies an envelope outside the narrowly allowed draft
// creation flow.
var ErrUnsafeCommand = errors.New("unsafe Dynamics command")

// DraftCommandTargets is the session-scoped allowlist used by
// ValidateDraftCommands.
type DraftCommandTargets struct {
	WorkspaceRootID string
	CreateButtonID  string
	DialogRootID    string
	NamePurposeID   string
	DetailsRootID   string
	SaveAndCloseID  string
}

// ValidateDraftCommands permits only one of the three observed request shapes:
// open-new-report, SetValue+InvokeDefaultButton, or SaveAndClose. Target IDs
// must match the supplied session-scoped allowlist.
func ValidateDraftCommands(envelope Envelope, targets DraftCommandTargets) error {
	if err := validateForbiddenCommandInvariant(envelope); err != nil {
		return err
	}

	switch len(envelope.Messages) {
	case 1:
		message := envelope.Messages[0]
		commands, err := message.CommandInteractions()
		if err != nil {
			return unsafe(0, -1, "", err.Error())
		}
		switch len(commands) {
		case 2:
			if err := validateOpen(commands, targets); err != nil {
				return err
			}
			return nil
		case 1:
			if err := validateSave(commands[0], targets); err != nil {
				return err
			}
			return nil
		default:
			return unsafe(0, -1, "", "single-message envelope has an unsupported interaction count")
		}
	case 2:
		if envelope.Messages[1].SequenceNumber != envelope.Messages[0].SequenceNumber+1 {
			return unsafe(1, -1, "", "SetValue and InvokeDefaultButton sequences must be consecutive")
		}
		setCommands, err := envelope.Messages[0].CommandInteractions()
		if err != nil || len(setCommands) != 1 {
			return unsafe(0, -1, "", "first create-draft message must contain exactly one SetValue command")
		}
		invokeCommands, err := envelope.Messages[1].CommandInteractions()
		if err != nil || len(invokeCommands) != 1 {
			return unsafe(1, -1, "", "second create-draft message must contain exactly one InvokeDefaultButton command")
		}
		if err := validateSetValue(setCommands[0], targets); err != nil {
			return err
		}
		if err := validateInvoke(invokeCommands[0], targets); err != nil {
			return err
		}
		return nil
	default:
		return unsafe(-1, -1, "", "envelope message shape is not part of draft creation")
	}
}

func validateOpen(commands []CommandInteraction, targets DraftCommandTargets) error {
	if targets.WorkspaceRootID == "" || targets.CreateButtonID == "" {
		return unsafe(0, -1, "", "workspace command targets are incomplete")
	}
	update, click := commands[0], commands[1]
	if err := validateCommon(update, CommandUpdateLastSelectedControl, targets.WorkspaceRootID, targets.WorkspaceRootID); err != nil {
		return unsafe(0, 0, update.CommandName, err.Error())
	}
	if !update.NoAsyncIncrement || update.Telemetry || update.Throttle || !shouldBlockOmitted(update) || !oneString(update.PositionalParameters, SelectedControlNewExpenseReportReportsTab) || len(update.ExtraFields) != 0 {
		return unsafe(0, 0, update.CommandName, "UpdateLastSelectedControl shape does not match the observed draft flow")
	}
	if err := validateClick(click, targets.WorkspaceRootID, targets.CreateButtonID, false); err != nil {
		return unsafe(0, 1, click.CommandName, err.Error())
	}
	return nil
}

func validateSetValue(command CommandInteraction, targets DraftCommandTargets) error {
	if targets.DialogRootID == "" || targets.NamePurposeID == "" {
		return unsafe(0, 0, command.CommandName, "SetValue command targets are incomplete")
	}
	if err := validateCommon(command, CommandSetValue, targets.DialogRootID, targets.NamePurposeID); err != nil {
		return unsafe(0, 0, command.CommandName, err.Error())
	}
	if command.NoAsyncIncrement || !command.Telemetry || command.Throttle || !shouldBlockOmitted(command) || !oneArbitraryString(command.PositionalParameters) {
		return unsafe(0, 0, command.CommandName, "SetValue shape does not match the observed draft flow")
	}
	if len(command.ExtraFields) > 1 {
		return unsafe(0, 0, command.CommandName, "SetValue contains unapproved fields")
	}
	if complete, ok := command.ExtraFields["complete"]; ok && !bytes.Equal(bytes.TrimSpace(complete), []byte("null")) {
		return unsafe(0, 0, command.CommandName, "SetValue complete field must be null")
	}
	for name := range command.ExtraFields {
		if name != "complete" {
			return unsafe(0, 0, command.CommandName, "SetValue contains unapproved field "+name)
		}
	}
	return nil
}

func validateInvoke(command CommandInteraction, targets DraftCommandTargets) error {
	if targets.DialogRootID == "" {
		return unsafe(1, 0, command.CommandName, "InvokeDefaultButton root is incomplete")
	}
	if err := validateCommon(command, CommandExecuteShortcuts, targets.DialogRootID, targets.DialogRootID); err != nil {
		return unsafe(1, 0, command.CommandName, err.Error())
	}
	if command.NoAsyncIncrement || !command.Telemetry || command.Throttle || !truePointer(command.ShouldBlockOnExecution) || !oneString(command.PositionalParameters, ShortcutInvokeDefaultButton) || len(command.ExtraFields) != 0 {
		return unsafe(1, 0, command.CommandName, "InvokeDefaultButton shape does not match the observed draft flow")
	}
	return nil
}

func validateSave(command CommandInteraction, targets DraftCommandTargets) error {
	if targets.DetailsRootID == "" || targets.SaveAndCloseID == "" {
		return unsafe(0, 0, command.CommandName, "SaveAndClose command targets are incomplete")
	}
	if err := validateClick(command, targets.DetailsRootID, targets.SaveAndCloseID, true); err != nil {
		return unsafe(0, 0, command.CommandName, err.Error())
	}
	return nil
}

func validateClick(command CommandInteraction, rootID, targetID string, requireNullParameters bool) error {
	if err := validateCommon(command, CommandClick, rootID, targetID); err != nil {
		return err
	}
	parametersOK := command.PositionalParameters == nil
	if !requireNullParameters {
		parametersOK = command.PositionalParameters != nil && len(command.PositionalParameters) == 0
	}
	if command.NoAsyncIncrement || !command.Telemetry || !command.Throttle || command.ThrottleID != rootID+"_TG" || !truePointer(command.ShouldBlockOnExecution) || !parametersOK || len(command.ExtraFields) != 0 {
		return errors.New("Click shape or target does not match the observed draft flow")
	}
	return nil
}

func validateCommon(command CommandInteraction, name, rootID, targetID string) error {
	if len(command.reservedFieldConflicts) != 0 {
		return errors.New("command contains duplicate or non-canonical reserved fields")
	}
	for _, field := range []string{
		"$type", "CallbackId", "CommandName", "FailureCallbackId", "NamedParameters",
		"NoAsyncIncrement", "PositionalParameters", "PriorityPosition", "ResetThrottleTime",
		"RootId", "TargetId", "Throttle", "ThrottleId", "ThrottleTimestamp", "ThrottleValue", "Telemetry",
	} {
		if !commandFieldPresent(command, field) {
			return fmt.Errorf("command is missing required field %s", field)
		}
	}
	if command.Type != InteractionTypeCommand || command.CommandName != name || command.RootID != rootID || command.TargetID != targetID {
		return fmt.Errorf("expected %s against allowed root/target", name)
	}
	if command.NamedParameters == nil || len(command.NamedParameters) != 0 || command.PriorityPosition || command.ResetThrottleTime || command.ThrottleTimestamp != 0 || command.ThrottleValue != 0 {
		return errors.New("command contains unapproved parameters or scheduling fields")
	}
	if name != CommandSetValue && (command.CallbackID != "" || command.FailureCallbackID != "") {
		return errors.New("command contains unapproved callbacks")
	}
	return nil
}

func validateForbiddenCommandInvariant(envelope Envelope) error {
	if len(envelope.ExtraFields) != 0 {
		return unsafe(-1, -1, "", "envelope contains unapproved extra fields")
	}
	if len(envelope.reservedFieldConflicts) != 0 {
		return unsafe(-1, -1, "", "envelope contains duplicate or non-canonical reserved fields")
	}
	if len(envelope.Messages) == 0 {
		return unsafe(-1, -1, "", "envelope has no messages")
	}
	for messageIndex, message := range envelope.Messages {
		if len(message.ExtraFields) != 0 {
			return unsafe(messageIndex, -1, "", "message contains unapproved extra fields")
		}
		if len(message.reservedFieldConflicts) != 0 {
			return unsafe(messageIndex, -1, "", "message contains duplicate or non-canonical reserved fields")
		}
		if message.SequenceNumber <= 0 {
			return unsafe(messageIndex, -1, "", "message sequence must be positive")
		}
		if len(message.Interactions) == 0 {
			return unsafe(messageIndex, -1, "", "message has no interactions")
		}
		for interactionIndex, raw := range message.Interactions {
			commandNames, err := topLevelStringFields(raw, "CommandName")
			if err != nil {
				return unsafe(messageIndex, interactionIndex, "", "invalid interaction JSON")
			}
			for _, commandName := range commandNames {
				if forbiddenCommandName(commandName) {
					return unsafe(messageIndex, interactionIndex, commandName, "submit/workflow/post/approve/recall commands are forbidden")
				}
			}
		}
	}
	return nil
}

func topLevelStringFields(raw json.RawMessage, wanted string) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("interaction is not an object")
	}
	var values []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("interaction field name is not a string")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if strings.EqualFold(name, wanted) {
			var text string
			if err := json.Unmarshal(value, &text); err == nil {
				values = append(values, text)
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	return values, nil
}

func forbiddenCommandName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, forbidden := range []string{"submit", "workflow", "post", "approve", "approval", "recall"} {
		if strings.Contains(normalized, forbidden) {
			return true
		}
	}
	return false
}

func oneString(parameters []any, expected string) bool {
	return len(parameters) == 1 && parameters[0] == expected
}

func oneArbitraryString(parameters []any) bool {
	if len(parameters) != 1 {
		return false
	}
	_, ok := parameters[0].(string)
	return ok
}

func shouldBlockOmitted(command CommandInteraction) bool {
	return command.ShouldBlockOnExecution == nil && !commandFieldPresent(command, "ShouldBlockOnExecution")
}

func truePointer(value *bool) bool {
	return value != nil && *value
}

func unsafe(messageIndex, interactionIndex int, commandName, reason string) error {
	location := ""
	if messageIndex >= 0 {
		location = fmt.Sprintf(" message %d", messageIndex)
	}
	if interactionIndex >= 0 {
		location += fmt.Sprintf(" interaction %d", interactionIndex)
	}
	if commandName != "" {
		location += fmt.Sprintf(" command %q", commandName)
	}
	return fmt.Errorf("%w:%s: %s", ErrUnsafeCommand, location, reason)
}
