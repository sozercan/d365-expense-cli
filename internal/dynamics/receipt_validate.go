package dynamics

import (
	"encoding/json"
	"errors"
	"strings"
)

// ReceiptCommandTargets is the session-scoped allowlist used by
// ValidateReceiptCommands.
type ReceiptCommandTargets struct {
	DetailsRootID      string
	NewReceiptButtonID string
	DialogRootID       string
	UploadControlID    string
	OKButtonID         string
	CloseButtonID      string
	SaveAndCloseID     string
}

// ValidateReceiptCommands permits only one captured receipt request stage:
// open the dialog; DocuName then CheckFile; the second CheckFile;
// UploadedFileId then CloseDialog; click OK; or SaveAndClose.
func ValidateReceiptCommands(envelope Envelope, targets ReceiptCommandTargets) error {
	if err := validateForbiddenCommandInvariant(envelope); err != nil {
		return err
	}

	switch len(envelope.Messages) {
	case 2:
		return validateReceiptDocuNameCheckFile(envelope.Messages, targets)
	case 1:
		message := envelope.Messages[0]
		switch len(message.Interactions) {
		case 1:
			command, err := ParseCommandInteraction(message.Interactions[0])
			if err != nil || command.Type != InteractionTypeCommand {
				return unsafe(0, 0, "", "single-interaction receipt stage must contain one command")
			}
			switch command.CommandName {
			case CommandCheckFile:
				return validateReceiptCheckFile(command, targets, 0, 0, "", "")
			case CommandClick:
				if targets.DialogRootID != "" && targets.OKButtonID != "" &&
					command.RootID == targets.DialogRootID && command.TargetID == targets.OKButtonID {
					if err := validateClick(command, targets.DialogRootID, targets.OKButtonID, true); err != nil {
						return unsafe(0, 0, command.CommandName, err.Error())
					}
					return nil
				}
				if targets.DialogRootID != "" && targets.CloseButtonID != "" &&
					command.RootID == targets.DialogRootID && command.TargetID == targets.CloseButtonID {
					if err := validateClick(command, targets.DialogRootID, targets.CloseButtonID, true); err != nil {
						return unsafe(0, 0, command.CommandName, err.Error())
					}
					return nil
				}
				if targets.DetailsRootID != "" && targets.SaveAndCloseID != "" &&
					command.RootID == targets.DetailsRootID && command.TargetID == targets.SaveAndCloseID {
					if err := validateClick(command, targets.DetailsRootID, targets.SaveAndCloseID, true); err != nil {
						return unsafe(0, 0, command.CommandName, err.Error())
					}
					return nil
				}
				return unsafe(0, 0, command.CommandName, "Click root/target is not the allowlisted OK, Close, or SaveAndClose control")
			default:
				return unsafe(0, 0, command.CommandName, "command is not part of the captured receipt flow")
			}
		case 2:
			var firstHeader struct {
				Type string `json:"$type"`
			}
			if err := json.Unmarshal(message.Interactions[0], &firstHeader); err != nil {
				return unsafe(0, 0, "", "invalid interaction JSON")
			}
			switch firstHeader.Type {
			case InteractionTypeCommand:
				commands, err := message.CommandInteractions()
				if err != nil {
					return unsafe(0, -1, "", err.Error())
				}
				return validateReceiptOpen(commands, targets)
			case InteractionTypePropertyChange:
				return validateReceiptUploadedFileCloseDialog(message, targets)
			default:
				return unsafe(0, 0, "", "unsupported receipt interaction order")
			}
		default:
			return unsafe(0, -1, "", "receipt stage has an unsupported interaction count")
		}
	default:
		return unsafe(-1, -1, "", "envelope message shape is not part of the captured receipt flow")
	}
}

func validateReceiptOpen(commands []CommandInteraction, targets ReceiptCommandTargets) error {
	if len(commands) != 2 || targets.DetailsRootID == "" || targets.NewReceiptButtonID == "" {
		return unsafe(0, -1, "", "open-receipt command targets or interaction count are invalid")
	}
	update, click := commands[0], commands[1]
	if err := validateCommon(update, CommandUpdateLastSelectedControl, targets.DetailsRootID, targets.DetailsRootID); err != nil {
		return unsafe(0, 0, update.CommandName, err.Error())
	}
	if !update.NoAsyncIncrement || update.Telemetry || update.Throttle || !shouldBlockOmitted(update) ||
		!oneString(update.PositionalParameters, SelectedControlImagePreviewReceipts) || len(update.ExtraFields) != 0 {
		return unsafe(0, 0, update.CommandName, "UpdateLastSelectedControl shape does not match the captured receipt flow")
	}
	if err := validateClick(click, targets.DetailsRootID, targets.NewReceiptButtonID, false); err != nil {
		return unsafe(0, 1, click.CommandName, err.Error())
	}
	return nil
}

func validateReceiptDocuNameCheckFile(messages []Message, targets ReceiptCommandTargets) error {
	if messages[1].SequenceNumber != messages[0].SequenceNumber+1 {
		return unsafe(1, -1, "", "DocuName and CheckFile sequences must be consecutive")
	}
	if len(messages[0].Interactions) != 1 || len(messages[1].Interactions) != 1 {
		return unsafe(-1, -1, "", "DocuName and CheckFile stages must each contain one interaction")
	}
	property, err := ParsePropertyChangeInteraction(messages[0].Interactions[0])
	if err != nil || property.Type != InteractionTypePropertyChange {
		return unsafe(0, 0, "", "first receipt message must contain DocuName property change")
	}
	if err := validateReceiptProperty(property, PropertyDocuName, targets, false); err != nil {
		return unsafe(0, 0, "", err.Error())
	}
	command, err := ParseCommandInteraction(messages[1].Interactions[0])
	if err != nil || command.Type != InteractionTypeCommand {
		return unsafe(1, 0, "", "second receipt message must contain CheckFile")
	}
	return validateReceiptCheckFile(command, targets, 1, 0, property.NewValue, "")
}

func validateReceiptCheckFile(command CommandInteraction, targets ReceiptCommandTargets, messageIndex, interactionIndex int, expectedDocumentName, expectedFileName string) error {
	if targets.DialogRootID == "" || targets.UploadControlID == "" {
		return unsafe(messageIndex, interactionIndex, command.CommandName, "CheckFile targets are incomplete")
	}
	if err := validateCommon(command, CommandCheckFile, targets.DialogRootID, targets.UploadControlID); err != nil {
		return unsafe(messageIndex, interactionIndex, command.CommandName, err.Error())
	}
	if command.NoAsyncIncrement || command.Telemetry || command.Throttle || !shouldBlockOmitted(command) || len(command.ExtraFields) != 0 || len(command.PositionalParameters) != 2 {
		return unsafe(messageIndex, interactionIndex, command.CommandName, "CheckFile shape does not match the captured receipt flow")
	}
	documentName, documentOK := command.PositionalParameters[0].(string)
	fileName, fileOK := command.PositionalParameters[1].(string)
	if !documentOK || !fileOK || strings.TrimSpace(documentName) == "" || strings.TrimSpace(fileName) == "" {
		return unsafe(messageIndex, interactionIndex, command.CommandName, "CheckFile requires non-empty document and file names")
	}
	if expectedDocumentName != "" && documentName != expectedDocumentName {
		return unsafe(messageIndex, interactionIndex, command.CommandName, "CheckFile document name does not match DocuName")
	}
	if expectedFileName != "" && fileName != expectedFileName {
		return unsafe(messageIndex, interactionIndex, command.CommandName, "CheckFile file name does not match")
	}
	return nil
}

func validateReceiptUploadedFileCloseDialog(message Message, targets ReceiptCommandTargets) error {
	property, err := ParsePropertyChangeInteraction(message.Interactions[0])
	if err != nil || property.Type != InteractionTypePropertyChange {
		return unsafe(0, 0, "", "first upload-completion interaction must be UploadedFileId")
	}
	if err := validateReceiptProperty(property, PropertyUploadedFileID, targets, true); err != nil {
		return unsafe(0, 0, "", err.Error())
	}
	command, err := ParseCommandInteraction(message.Interactions[1])
	if err != nil || command.Type != InteractionTypeCommand {
		return unsafe(0, 1, "", "second upload-completion interaction must be CloseDialog")
	}
	if err := validateCommon(command, CommandCloseDialog, targets.DialogRootID, targets.UploadControlID); err != nil {
		return unsafe(0, 1, command.CommandName, err.Error())
	}
	if command.NoAsyncIncrement || command.Telemetry || command.Throttle || !shouldBlockOmitted(command) ||
		command.PositionalParameters != nil || len(command.ExtraFields) != 0 {
		return unsafe(0, 1, command.CommandName, "CloseDialog shape does not match the captured receipt flow")
	}
	return nil
}

func validateReceiptProperty(property PropertyChangeInteraction, propertyName string, targets ReceiptCommandTargets, requireNoAsync bool) error {
	if targets.DialogRootID == "" || targets.UploadControlID == "" {
		return errors.New("receipt property targets are incomplete")
	}
	if len(property.reservedFieldConflicts) != 0 || len(property.ExtraFields) != 0 {
		return errors.New("property change contains unapproved, duplicate, or non-canonical fields")
	}
	if property.Type != InteractionTypePropertyChange || property.PropertyName != propertyName ||
		property.RootID != targets.DialogRootID || property.TargetID != targets.UploadControlID || strings.TrimSpace(property.NewValue) == "" {
		return errors.New("property change shape or target does not match the captured receipt flow")
	}
	present := propertyNoAsyncIncrementPresent(property)
	if requireNoAsync {
		if !present || property.NoAsyncIncrement == nil || !*property.NoAsyncIncrement {
			return errors.New("UploadedFileId must set NoAsyncIncrement to true")
		}
	} else if present {
		return errors.New("DocuName must omit NoAsyncIncrement")
	}
	return nil
}
