package dynamics

import (
	"errors"
	"strconv"
)

const (
	CommandRequestPopup = "RequestPopup"
	CommandRequestClose = "RequestClose"

	FormReceiptMatch           = "ExpenseReceiptCardAddDialog_form"
	ControlReceiptsGrid        = "ReceiptsTabPageGrid"
	ControlMatchExpensesButton = "MatchExpensesButton"
	ControlAttachButton        = "AttachButton"

	DataSourceReceipts       = "DocuValue_ds"
	DataSourceReceiptMatches = "DocuRef_MatchedToExpense_ds"
)

// ReceiptMatchCommandTargets is the session-scoped allowlist used by
// ValidateReceiptMatchCommands. Callers populate only the IDs admitted for the
// current stage.
type ReceiptMatchCommandTargets struct {
	DetailsRootID  string
	ReceiptsGridID string
	MatchButtonID  string
	PopupRootID    string
	PopupGridID    string
	AttachButtonID string
}

// BuildOpenReceiptMatchMessage opens the selected receipt's Attach to expense
// popup. The browser numbers popups with Arg1; the message sequence keeps the
// number unique within the session.
func BuildOpenReceiptMatchMessage(sequence int64, detailsRootID, matchButtonID string) Message {
	message := BuildUpdateLastSelectedControlClickMessage(sequence, detailsRootID, matchButtonID, SelectedControlImagePreviewReceipts)
	popup := baseCommand(CommandRequestPopup, detailsRootID, matchButtonID)
	popup.NamedParameters = map[string]any{"Arg1": strconv.FormatInt(sequence, 10)}
	popup.PositionalParameters = []any{}
	popup.ShouldBlockOnExecution = boolPointer(true)
	popup.Throttle = true
	popup.ThrottleID = detailsRootID + "_TG"
	popup.Telemetry = true
	raw, err := MarshalCommandInteraction(popup)
	if err != nil {
		panic("dynamics: marshal built RequestPopup: " + err.Error())
	}
	message.Interactions[1] = raw
	return message
}

// BuildAttachReceiptMessage clicks the popup's attach button for its active
// expense line.
func BuildAttachReceiptMessage(sequence int64, popupRootID, attachButtonID string) Message {
	command := baseCommand(CommandClick, popupRootID, attachButtonID)
	command.Telemetry = true
	return commandMessage(sequence, command)
}

// BuildClosePopupMessage closes a popup form.
func BuildClosePopupMessage(sequence int64, popupRootID string) Message {
	command := baseCommand(CommandRequestClose, popupRootID, popupRootID)
	command.PositionalParameters = []any{nil}
	command.ShouldBlockOnExecution = boolPointer(true)
	command.Telemetry = true
	return commandMessage(sequence, command)
}

// ValidateReceiptMatchCommands permits only the captured receipt-to-line
// shapes: select a receipt, open its Attach to expense popup, select a popup
// line, attach, or close the popup.
func ValidateReceiptMatchCommands(envelope Envelope, targets ReceiptMatchCommandTargets) error {
	if err := validateForbiddenCommandInvariant(envelope); err != nil {
		return err
	}
	if len(envelope.Messages) != 1 {
		return unsafe(-1, -1, "", "receipt matching stages contain exactly one message")
	}
	message := envelope.Messages[0]
	if len(message.Interactions) == 2 {
		commands, err := message.CommandInteractions()
		if err != nil {
			return unsafe(0, -1, "", err.Error())
		}
		update, popup := commands[0], commands[1]
		if targets.DetailsRootID == "" || targets.MatchButtonID == "" {
			return unsafe(0, 1, popup.CommandName, "Attach to expense targets are incomplete")
		}
		if err := validateCommon(update, CommandUpdateLastSelectedControl, targets.DetailsRootID, targets.DetailsRootID); err != nil {
			return unsafe(0, 0, update.CommandName, err.Error())
		}
		if !update.NoAsyncIncrement || update.Telemetry || update.Throttle || !shouldBlockOmitted(update) ||
			!oneString(update.PositionalParameters, SelectedControlImagePreviewReceipts) || len(update.ExtraFields) != 0 {
			return unsafe(0, 0, update.CommandName, "UpdateLastSelectedControl shape does not match the captured receipt matching flow")
		}
		if err := validateRequestPopup(popup, targets.DetailsRootID, targets.MatchButtonID, message.SequenceNumber); err != nil {
			return unsafe(0, 1, popup.CommandName, err.Error())
		}
		return nil
	}
	if len(message.Interactions) != 1 {
		return unsafe(0, -1, "", "receipt matching stage has an unsupported interaction count")
	}
	names, err := topLevelStringFields(message.Interactions[0], "CommandName")
	if err != nil || len(names) != 1 {
		return unsafe(0, 0, "", "receipt matching interaction must name one command")
	}
	if names[0] == CommandChangeSelectedIndex {
		switch {
		case targets.DetailsRootID != "" && targets.ReceiptsGridID != "":
			return validateChangeSelectedIndex(message, targets.DetailsRootID, targets.ReceiptsGridID, MarkModeActivate)
		case targets.PopupRootID != "" && targets.PopupGridID != "":
			return validateChangeSelectedIndex(message, targets.PopupRootID, targets.PopupGridID, MarkModeActivate)
		}
		return unsafe(0, 0, names[0], "row selection targets are incomplete")
	}
	command, err := ParseCommandInteraction(message.Interactions[0])
	if err != nil {
		return unsafe(0, 0, names[0], err.Error())
	}
	switch command.CommandName {
	case CommandClick:
		if targets.PopupRootID == "" || targets.AttachButtonID == "" {
			return unsafe(0, 0, command.CommandName, "attach targets are incomplete")
		}
		if err := validateCommon(command, CommandClick, targets.PopupRootID, targets.AttachButtonID); err != nil {
			return unsafe(0, 0, command.CommandName, err.Error())
		}
		if command.NoAsyncIncrement || !command.Telemetry || command.Throttle || !shouldBlockOmitted(command) ||
			command.PositionalParameters != nil || len(command.ExtraFields) != 0 {
			return unsafe(0, 0, command.CommandName, "attach Click shape does not match the captured receipt matching flow")
		}
		return nil
	case CommandRequestClose:
		if targets.PopupRootID == "" {
			return unsafe(0, 0, command.CommandName, "popup target is incomplete")
		}
		if err := validateCommon(command, CommandRequestClose, targets.PopupRootID, targets.PopupRootID); err != nil {
			return unsafe(0, 0, command.CommandName, err.Error())
		}
		if command.NoAsyncIncrement || !command.Telemetry || command.Throttle || !truePointer(command.ShouldBlockOnExecution) ||
			len(command.PositionalParameters) != 1 || command.PositionalParameters[0] != nil || len(command.ExtraFields) != 0 {
			return unsafe(0, 0, command.CommandName, "RequestClose shape does not match the captured receipt matching flow")
		}
		return nil
	default:
		return unsafe(0, 0, command.CommandName, "command is not part of the receipt matching flow")
	}
}

func validateRequestPopup(command CommandInteraction, rootID, targetID string, sequence int64) error {
	// validateCommon rejects named parameters, which RequestPopup requires,
	// so check the captured shape field by field.
	if len(command.reservedFieldConflicts) != 0 || command.Type != InteractionTypeCommand || command.CommandName != CommandRequestPopup ||
		command.RootID != rootID || command.TargetID != targetID || command.CallbackID != "" || command.FailureCallbackID != "" {
		return errors.New("RequestPopup target or callbacks are not allowlisted")
	}
	arg, ok := command.NamedParameters["Arg1"].(string)
	if len(command.NamedParameters) != 1 || !ok || arg != strconv.FormatInt(sequence, 10) {
		return errors.New("RequestPopup named parameters are not allowlisted")
	}
	if command.NoAsyncIncrement || !command.Telemetry || !command.Throttle || command.ThrottleID != rootID+"_TG" ||
		!truePointer(command.ShouldBlockOnExecution) || command.PositionalParameters == nil || len(command.PositionalParameters) != 0 ||
		command.PriorityPosition || command.ResetThrottleTime || command.ThrottleTimestamp != 0 || command.ThrottleValue != 0 ||
		len(command.ExtraFields) != 0 {
		return errors.New("RequestPopup shape does not match the captured receipt matching flow")
	}
	return nil
}
