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
	CommandExecuteHyperlink    = "ExecuteHyperlink"
	ShortcutRefresh            = "Refresh"

	// MarkModeActivate makes a grid row active, as a row click does;
	// MarkModeMark also checks its selection box.
	MarkModeActivate = 0
	MarkModeMark     = 1

	ControlReportsGrid        = "ReportsTabPageGrid"
	ControlReportLink         = "ExpenseReportNumber"
	ControlLinesGrid          = "MainGrid"
	ControlDeleteReportButton = "DeleteButtonReportsTab"
	FormConfirmation          = "SysBoxForm_form"
	ControlConfirmYes         = "Yes"
	ControlConfirmNo          = "No"

	DataSourceReports = "TrvExpTable_ds"
)

// ReportCommandTargets is the session-scoped allowlist used by
// ValidateReportCommands. Callers populate only the IDs admitted for the
// current stage; MarkMode is the only row-selection mode admitted.
type ReportCommandTargets struct {
	WorkspaceRootID string
	ReportsGridID   string
	MarkMode        int
	ReportLinkID    string
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

// BuildChangeSelectedIndexMessage selects one grid row by MasterTrackingId.
// pivotTrackingID is the currently active row. Dynamics serializes
// ResetThrottleTime and ThrottleFirst as strings for this command, so the
// captured shape is built directly.
func BuildChangeSelectedIndexMessage(sequence int64, rootID, gridID, trackingID string, markMode int, pivotTrackingID string) (Message, error) {
	if sequence <= 0 || rootID == "" || gridID == "" || !trackingIDValid(trackingID) || !trackingIDValid(pivotTrackingID) ||
		(markMode != MarkModeActivate && markMode != MarkModeMark) {
		return Message{}, errors.New("ChangeSelectedIndex targets are incomplete")
	}
	interaction, err := json.Marshal(map[string]any{
		"$type":                InteractionTypeCommand,
		"CallbackId":           "",
		"CommandName":          CommandChangeSelectedIndex,
		"FailureCallbackId":    "",
		"NamedParameters":      map[string]any{},
		"NoAsyncIncrement":     false,
		"PositionalParameters": []any{trackingID, markMode, pivotTrackingID, false, true},
		"PriorityPosition":     false,
		"ResetThrottleTime":    "true",
		"RootId":               rootID,
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

// BuildOpenReportMessage opens the active reports-grid row through its
// report-number hyperlink.
func BuildOpenReportMessage(sequence int64, workspaceRootID, reportLinkID string) Message {
	command := baseCommand(CommandExecuteHyperlink, workspaceRootID, reportLinkID)
	command.PositionalParameters = nil
	command.ShouldBlockOnExecution = boolPointer(true)
	command.Telemetry = true
	return commandMessage(sequence, command)
}

// BuildDeleteReportClickMessage clicks the reports grid's Delete button.
func BuildDeleteReportClickMessage(sequence int64, workspaceRootID, deleteButtonID string) Message {
	return buildSelectAndClickMessage(sequence, workspaceRootID, deleteButtonID, ControlReportsGrid, nil)
}

// ValidateReportCommands permits only the captured existing-report request
// shapes: Refresh, select one grid row, open its report, click Delete, or
// confirm Yes.
func ValidateReportCommands(envelope Envelope, targets ReportCommandTargets) error {
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
		return validateChangeSelectedIndex(message, targets.WorkspaceRootID, targets.ReportsGridID, targets.MarkMode)
	case CommandExecuteHyperlink:
		command, err := ParseCommandInteraction(message.Interactions[0])
		if err != nil {
			return unsafe(0, 0, names[0], err.Error())
		}
		if targets.WorkspaceRootID == "" || targets.ReportLinkID == "" {
			return unsafe(0, 0, command.CommandName, "report link targets are incomplete")
		}
		if err := validateCommon(command, CommandExecuteHyperlink, targets.WorkspaceRootID, targets.ReportLinkID); err != nil {
			return unsafe(0, 0, command.CommandName, err.Error())
		}
		if command.NoAsyncIncrement || !command.Telemetry || command.Throttle || !truePointer(command.ShouldBlockOnExecution) ||
			command.PositionalParameters != nil || len(command.ExtraFields) != 0 {
			return unsafe(0, 0, command.CommandName, "ExecuteHyperlink shape does not match the captured open-report flow")
		}
		return nil
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

func validateChangeSelectedIndex(message Message, rootID, gridID string, markMode int) error {
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
	expected, err := BuildChangeSelectedIndexMessage(message.SequenceNumber, rootID, gridID, trackingID, markMode, pivotTrackingID)
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
