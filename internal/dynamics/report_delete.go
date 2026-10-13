package dynamics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const (
	CommandChangeSelectedIndex = "ChangeSelectedIndex"
	ShortcutRefresh            = "Refresh"

	ControlReportsGrid        = "ReportsTabPageGrid"
	ControlDeleteReportButton = "DeleteButtonReportsTab"
	FormConfirmation          = "SysBoxForm_form"
	ControlConfirmYes         = "Yes"
	ControlConfirmNo          = "No"

	DataSourceReports = "TrvExpTable_ds"

	// selectRowMarkMode is the captured MarkMode for a single checkbox mark.
	selectRowMarkMode = 1
)

// DeleteReportCommandTargets is the session-scoped allowlist used by
// ValidateDeleteReportCommands. Callers populate only the IDs admitted for the
// current stage.
type DeleteReportCommandTargets struct {
	WorkspaceRootID string
	ReportsGridID   string
	DeleteButtonID  string
	ConfirmRootID   string
	ConfirmYesID    string
}

// BuildRefreshMessage builds the captured workspace Refresh shortcut, which
// reloads the reports grid.
func BuildRefreshMessage(sequence int64, workspaceRootID string) Message {
	command := baseCommand(CommandExecuteShortcuts, workspaceRootID, workspaceRootID)
	command.PositionalParameters = []any{ShortcutRefresh}
	command.ShouldBlockOnExecution = boolPointer(true)
	command.Telemetry = true
	return commandMessage(sequence, command)
}

// BuildSelectReportRowMessage marks exactly one reports-grid row. Dynamics
// serializes ResetThrottleTime and ThrottleFirst as strings for this command,
// so the captured shape is built directly.
func BuildSelectReportRowMessage(sequence int64, workspaceRootID, gridID, trackingID, pivotTrackingID string) (Message, error) {
	if sequence <= 0 || workspaceRootID == "" || gridID == "" || !trackingIDValid(trackingID) || !trackingIDValid(pivotTrackingID) {
		return Message{}, errors.New("ChangeSelectedIndex targets are incomplete")
	}
	interaction, err := json.Marshal(map[string]any{
		"$type":                InteractionTypeCommand,
		"CallbackId":           "",
		"CommandName":          CommandChangeSelectedIndex,
		"FailureCallbackId":    "",
		"NamedParameters":      map[string]any{},
		"NoAsyncIncrement":     false,
		"PositionalParameters": []any{trackingID, selectRowMarkMode, pivotTrackingID, false, true},
		"PriorityPosition":     false,
		"ResetThrottleTime":    "true",
		"RootId":               workspaceRootID,
		"TargetId":             gridID,
		"Throttle":             false,
		"ThrottleFirst":        "false",
		"ThrottleId":           "0",
		"ThrottleTimestamp":    int64(0),
		"ThrottleValue":        int64(0),
		"Telemetry":            false,
	})
	if err != nil {
		return Message{}, errors.New("build ChangeSelectedIndex command")
	}
	return Message{SequenceNumber: sequence, Interactions: []json.RawMessage{interaction}}, nil
}

// BuildDeleteReportClickMessage clicks the reports grid's Delete button.
func BuildDeleteReportClickMessage(sequence int64, workspaceRootID, deleteButtonID string) Message {
	return buildSelectAndClickMessage(sequence, workspaceRootID, deleteButtonID, ControlReportsGrid, nil)
}

// ValidateDeleteReportCommands permits only the captured report-deletion
// request shapes: Refresh, mark one grid row, click Delete, or confirm Yes.
func ValidateDeleteReportCommands(envelope Envelope, targets DeleteReportCommandTargets) error {
	if err := validateForbiddenCommandInvariant(envelope); err != nil {
		return err
	}
	if len(envelope.Messages) != 1 {
		return unsafe(-1, -1, "", "report deletion stages contain exactly one message")
	}
	message := envelope.Messages[0]
	if len(message.Interactions) == 2 {
		commands, err := message.CommandInteractions()
		if err != nil {
			return unsafe(0, -1, "", err.Error())
		}
		update, click := commands[0], commands[1]
		if targets.WorkspaceRootID == "" || targets.DeleteButtonID == "" {
			return unsafe(0, 1, click.CommandName, "Delete targets are incomplete")
		}
		if err := validateCommon(update, CommandUpdateLastSelectedControl, targets.WorkspaceRootID, targets.WorkspaceRootID); err != nil {
			return unsafe(0, 0, update.CommandName, err.Error())
		}
		if !update.NoAsyncIncrement || update.Telemetry || update.Throttle || !shouldBlockOmitted(update) ||
			!oneString(update.PositionalParameters, ControlReportsGrid) || len(update.ExtraFields) != 0 {
			return unsafe(0, 0, update.CommandName, "UpdateLastSelectedControl shape does not match the captured delete flow")
		}
		if err := validateClick(click, targets.WorkspaceRootID, targets.DeleteButtonID, true); err != nil {
			return unsafe(0, 1, click.CommandName, err.Error())
		}
		return nil
	}
	if len(message.Interactions) != 1 {
		return unsafe(0, -1, "", "report deletion stage has an unsupported interaction count")
	}
	names, err := topLevelStringFields(message.Interactions[0], "CommandName")
	if err != nil || len(names) != 1 {
		return unsafe(0, 0, "", "report deletion interaction must name one command")
	}
	switch names[0] {
	case CommandChangeSelectedIndex:
		return validateSelectReportRow(message, targets)
	case CommandExecuteShortcuts:
		command, err := ParseCommandInteraction(message.Interactions[0])
		if err != nil {
			return unsafe(0, 0, names[0], err.Error())
		}
		if targets.WorkspaceRootID == "" {
			return unsafe(0, 0, command.CommandName, "Refresh target is incomplete")
		}
		if err := validateCommon(command, CommandExecuteShortcuts, targets.WorkspaceRootID, targets.WorkspaceRootID); err != nil {
			return unsafe(0, 0, command.CommandName, err.Error())
		}
		if command.NoAsyncIncrement || !command.Telemetry || command.Throttle || !truePointer(command.ShouldBlockOnExecution) ||
			!oneString(command.PositionalParameters, ShortcutRefresh) || len(command.ExtraFields) != 0 {
			return unsafe(0, 0, command.CommandName, "Refresh shape does not match the captured delete flow")
		}
		return nil
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
		return unsafe(0, 0, names[0], "command is not part of the report deletion flow")
	}
}

func validateSelectReportRow(message Message, targets DeleteReportCommandTargets) error {
	var actual map[string]any
	decoder := json.NewDecoder(bytes.NewReader(message.Interactions[0]))
	decoder.UseNumber()
	if err := decoder.Decode(&actual); err != nil {
		return unsafe(0, 0, CommandChangeSelectedIndex, "invalid ChangeSelectedIndex JSON")
	}
	parameters, _ := actual["PositionalParameters"].([]any)
	if len(parameters) != 5 {
		return unsafe(0, 0, CommandChangeSelectedIndex, "ChangeSelectedIndex parameters do not match the captured delete flow")
	}
	trackingID, _ := parameters[0].(string)
	pivotTrackingID, _ := parameters[2].(string)
	expected, err := BuildSelectReportRowMessage(message.SequenceNumber, targets.WorkspaceRootID, targets.ReportsGridID, trackingID, pivotTrackingID)
	if err != nil {
		return unsafe(0, 0, CommandChangeSelectedIndex, err.Error())
	}
	if !sameJSON(message.Interactions[0], expected.Interactions[0]) {
		return unsafe(0, 0, CommandChangeSelectedIndex, "ChangeSelectedIndex shape or target is not allowlisted")
	}
	return nil
}

func sameJSON(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func trackingIDValid(value string) bool {
	number, err := strconv.ParseInt(value, 10, 64)
	return err == nil && number > 0 && strconv.FormatInt(number, 10) == value
}

// DiscoverActiveTrackingID returns the ActiveMasterTrackingId reported for a
// data source in rootID. It fails when one response reports conflicting rows.
func DiscoverActiveTrackingID(data []byte, dataSource, rootID string) (string, error) {
	value, err := decodeResponseValue(data)
	if err != nil {
		return "", fmt.Errorf("discover active %s record: %w", dataSource, err)
	}
	active := ""
	conflict := false
	var walk func(any, string)
	walk = func(value any, currentRoot string) {
		switch typed := value.(type) {
		case map[string]any:
			if root := stringValue(typed["RootId"]); root != "" {
				currentRoot = root
			}
			if currentRoot == rootID && stringValue(typed["Name"]) == dataSource {
				if tracking := stringValue(typed["ActiveMasterTrackingId"]); tracking != "" {
					conflict = conflict || (active != "" && active != tracking)
					active = tracking
				}
			}
			for _, child := range typed {
				walk(child, currentRoot)
			}
		case []any:
			for _, child := range typed {
				walk(child, currentRoot)
			}
		}
	}
	walk(value, "")
	if conflict {
		return "", fmt.Errorf("discover active %s record: response reports conflicting active rows", dataSource)
	}
	return active, nil
}
