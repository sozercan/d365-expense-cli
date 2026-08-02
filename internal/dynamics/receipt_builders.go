package dynamics

import (
	"encoding/json"
	"fmt"
)

const (
	CommandCheckFile   = "CheckFile"
	CommandCloseDialog = "CloseDialog"

	SelectedControlImagePreviewReceipts = "ImagePreview_Receipts"

	FormExpenseAddNewReceipt = "ExpenseAddNewReceipt_form"

	ControlUploadControl            = "UploadControl"
	ControlOKButtonAddNewTabPage    = "OkButtonAddNewTabPage"
	ControlCloseButtonAddNewTabPage = "CloseButtonAddNewTabPage"
	ControlAddReceipts              = "AddReceipts"
	ControlNewReceiptButton         = "NewReceiptButton"
	ControlReceiptsTabPage          = "ReceiptsTabPage"
	ControlReceiptCount             = "ReceiptCount"
)

// BuildOpenNewReceiptMessage selects the receipt preview and clicks the
// session-scoped NewReceiptButton.
func BuildOpenNewReceiptMessage(sequence int64, detailsRootID, newReceiptButtonID string) Message {
	return BuildUpdateLastSelectedControlClickMessage(
		sequence,
		detailsRootID,
		newReceiptButtonID,
		SelectedControlImagePreviewReceipts,
	)
}

// BuildReceiptDocuNameCheckFileMessages builds the captured two-message stage:
// a DocuName property change followed by CheckFile.
func BuildReceiptDocuNameCheckFileMessages(sequence int64, dialogRootID, uploadControlID, documentName, fileName string) []Message {
	property := PropertyChangeInteraction{
		Type:         InteractionTypePropertyChange,
		PropertyName: PropertyDocuName,
		RootID:       dialogRootID,
		TargetID:     uploadControlID,
		NewValue:     documentName,
	}
	return []Message{
		receiptInteractionMessage(sequence, property),
		BuildReceiptCheckFileMessage(sequence+1, dialogRootID, uploadControlID, documentName, fileName),
	}
}

// BuildReceiptCheckFileMessage builds either captured CheckFile request. The
// first follows DocuName in the same request envelope; the second is sent in a
// later one-message envelope after the server acknowledges the first.
func BuildReceiptCheckFileMessage(sequence int64, dialogRootID, uploadControlID, documentName, fileName string) Message {
	command := baseCommand(CommandCheckFile, dialogRootID, uploadControlID)
	command.PositionalParameters = []any{documentName, fileName}
	return commandMessage(sequence, command)
}

// BuildReceiptUploadedFileCloseDialogMessage builds the captured upload
// completion stage: UploadedFileId with NoAsyncIncrement=true, then
// CloseDialog with null PositionalParameters.
func BuildReceiptUploadedFileCloseDialogMessage(sequence int64, dialogRootID, uploadControlID, uploadedFileID string) Message {
	property := PropertyChangeInteraction{
		Type:                    InteractionTypePropertyChange,
		PropertyName:            PropertyUploadedFileID,
		RootID:                  dialogRootID,
		TargetID:                uploadControlID,
		NoAsyncIncrement:        boolPointer(true),
		NewValue:                uploadedFileID,
		noAsyncIncrementPresent: true,
	}
	closeDialog := baseCommand(CommandCloseDialog, dialogRootID, uploadControlID)
	closeDialog.PositionalParameters = nil
	return receiptInteractionMessage(sequence, property, closeDialog)
}

// BuildReceiptOKClickMessage clicks the add-receipt dialog's OK button. Its
// null PositionalParameters value is intentional and capture-compatible.
func BuildReceiptOKClickMessage(sequence int64, dialogRootID, okButtonID string) Message {
	return BuildSaveAndCloseClickMessage(sequence, dialogRootID, okButtonID)
}

// BuildReceiptCloseButtonMessage closes the add-receipt dialog without saving
// or uploading. It is used for the captured Draft-status preflight.
func BuildReceiptCloseButtonMessage(sequence int64, dialogRootID, closeButtonID string) Message {
	return BuildSaveAndCloseClickMessage(sequence, dialogRootID, closeButtonID)
}

func receiptInteractionMessage(sequence int64, interactions ...any) Message {
	message := Message{SequenceNumber: sequence, Interactions: make([]json.RawMessage, len(interactions))}
	for index, interaction := range interactions {
		var (
			raw json.RawMessage
			err error
		)
		switch typed := interaction.(type) {
		case CommandInteraction:
			raw, err = MarshalCommandInteraction(typed)
		case PropertyChangeInteraction:
			raw, err = MarshalPropertyChangeInteraction(typed)
		default:
			err = fmt.Errorf("unsupported built interaction %T", interaction)
		}
		if err != nil {
			panic(fmt.Sprintf("dynamics: marshal built receipt interaction: %v", err))
		}
		message.Interactions[index] = raw
	}
	return message
}
