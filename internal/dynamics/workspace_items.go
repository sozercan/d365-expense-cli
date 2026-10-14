package dynamics

import (
	"encoding/json"
	"errors"
	"slices"
)

const (
	ControlExpensesGrid        = "ExpenseTabPageGrid"
	ControlDeleteExpenseButton = "DeleteButtonExpenseTab"
	ControlDeleteReceiptButton = "DeleteButtonReceiptsTab"

	// SelectedControlExpenseDate and SelectedControlReceiptName are the
	// captured focus targets sent with each tab's Delete click.
	SelectedControlExpenseDate = "ExpTransDate"
	SelectedControlReceiptName = "Filename"

	workspaceTabThrottle = "_WorkspaceTabControl"
)

// WorkspaceItemCommandTargets is the session-scoped allowlist used by
// ValidateWorkspaceItemCommands. Callers populate only the IDs admitted for
// the current stage.
type WorkspaceItemCommandTargets struct {
	WorkspaceRootID string
	TabIDs          []string
	GridID          string
	DeleteButtonID  string
	SelectedControl string
	ConfirmRootID   string
	ConfirmYesID    string
}

// BuildActivateWorkspaceTabMessage activates one Expense workspace tab.
// Dynamics serializes ResetThrottleTime and ThrottleFirst as strings for this
// command, so the captured shape is built directly.
func BuildActivateWorkspaceTabMessage(sequence int64, workspaceRootID, tabID string) (Message, error) {
	if sequence <= 0 || workspaceRootID == "" || tabID == "" {
		return Message{}, errors.New("workspace ActivateTab targets are incomplete")
	}
	interaction, err := json.Marshal(map[string]any{
		"$type":                InteractionTypeCommand,
		"CallbackId":           "",
		"CommandName":          CommandActivateTab,
		"FailureCallbackId":    "",
		"NamedParameters":      map[string]any{},
		"NoAsyncIncrement":     false,
		"PositionalParameters": []any{},
		"PriorityPosition":     false,
		"ResetThrottleTime":    "true",
		"RootId":               workspaceRootID,
		"TargetId":             tabID,
		"Throttle":             true,
		"ThrottleFirst":        "true",
		"ThrottleId":           workspaceRootID + workspaceTabThrottle,
		"ThrottleTimestamp":    int64(0),
		"ThrottleValue":        int64(300),
		"Telemetry":            true,
	})
	if err != nil {
		return Message{}, errors.New("build workspace ActivateTab command")
	}
	return Message{SequenceNumber: sequence, Interactions: []json.RawMessage{interaction}}, nil
}

// BuildDeleteWorkspaceItemMessage clicks a workspace tab's Delete button.
func BuildDeleteWorkspaceItemMessage(sequence int64, workspaceRootID, deleteButtonID, selectedControl string) Message {
	return buildSelectAndClickMessage(sequence, workspaceRootID, deleteButtonID, selectedControl, nil)
}

// ValidateWorkspaceItemCommands permits only the captured workspace tab
// shapes: activate an allowlisted tab, select a grid row, click the tab's
// Delete button, or confirm Yes.
func ValidateWorkspaceItemCommands(envelope Envelope, targets WorkspaceItemCommandTargets) error {
	if err := validateForbiddenCommandInvariant(envelope); err != nil {
		return err
	}
	if len(envelope.Messages) != 1 {
		return unsafe(-1, -1, "", "workspace item stages contain exactly one message")
	}
	message := envelope.Messages[0]
	if len(message.Interactions) == 2 {
		commands, err := message.CommandInteractions()
		if err != nil {
			return unsafe(0, -1, "", err.Error())
		}
		update, click := commands[0], commands[1]
		if targets.WorkspaceRootID == "" || targets.DeleteButtonID == "" || targets.SelectedControl == "" {
			return unsafe(0, 1, click.CommandName, "Delete targets are incomplete")
		}
		if err := validateCommon(update, CommandUpdateLastSelectedControl, targets.WorkspaceRootID, targets.WorkspaceRootID); err != nil {
			return unsafe(0, 0, update.CommandName, err.Error())
		}
		if !update.NoAsyncIncrement || update.Telemetry || update.Throttle || !shouldBlockOmitted(update) ||
			!oneString(update.PositionalParameters, targets.SelectedControl) || len(update.ExtraFields) != 0 {
			return unsafe(0, 0, update.CommandName, "UpdateLastSelectedControl shape does not match the captured workspace delete flow")
		}
		if err := validateClick(click, targets.WorkspaceRootID, targets.DeleteButtonID, true); err != nil {
			return unsafe(0, 1, click.CommandName, err.Error())
		}
		return nil
	}
	if len(message.Interactions) != 1 {
		return unsafe(0, -1, "", "workspace item stage has an unsupported interaction count")
	}
	names, err := topLevelStringFields(message.Interactions[0], "CommandName")
	if err != nil || len(names) != 1 {
		return unsafe(0, 0, "", "workspace item interaction must name one command")
	}
	switch names[0] {
	case CommandActivateTab:
		var actual map[string]any
		if err := json.Unmarshal(message.Interactions[0], &actual); err != nil {
			return unsafe(0, 0, names[0], "invalid ActivateTab JSON")
		}
		tabID, _ := actual["TargetId"].(string)
		if !slices.Contains(targets.TabIDs, tabID) {
			return unsafe(0, 0, names[0], "ActivateTab target is not an allowlisted workspace tab")
		}
		expected, err := BuildActivateWorkspaceTabMessage(message.SequenceNumber, targets.WorkspaceRootID, tabID)
		if err != nil || !sameJSON(message.Interactions[0], expected.Interactions[0]) {
			return unsafe(0, 0, names[0], "ActivateTab shape does not match the captured workspace flow")
		}
		return nil
	case CommandChangeSelectedIndex:
		if targets.WorkspaceRootID == "" || targets.GridID == "" {
			return unsafe(0, 0, names[0], "row selection targets are incomplete")
		}
		return validateChangeSelectedIndex(message, targets.WorkspaceRootID, targets.GridID, MarkModeActivate)
	case CommandClick:
		command, err := ParseCommandInteraction(message.Interactions[0])
		if err != nil {
			return unsafe(0, 0, names[0], err.Error())
		}
		if targets.ConfirmRootID == "" || targets.ConfirmYesID == "" {
			return unsafe(0, 0, command.CommandName, "confirmation targets are incomplete")
		}
		if err := validateClick(command, targets.ConfirmRootID, targets.ConfirmYesID, true); err != nil {
			return unsafe(0, 0, command.CommandName, err.Error())
		}
		return nil
	default:
		return unsafe(0, 0, names[0], "command is not part of the workspace item flow")
	}
}
