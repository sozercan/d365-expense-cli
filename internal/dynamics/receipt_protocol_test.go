package dynamics_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func TestPropertyChangeInteractionRoundTripPreservesUnknownFields(t *testing.T) {
	raw := json.RawMessage(`{"$type":"PropertyChangeInteraction","PropertyName":"DocuName","RootId":"dialog","TargetId":"upload","NewValue":"receipt","Future":{"v":2}}`)
	property, err := dynamics.ParsePropertyChangeInteraction(raw)
	if err != nil {
		t.Fatal(err)
	}
	if property.PropertyName != dynamics.PropertyDocuName || property.NewValue != "receipt" {
		t.Fatalf("property = %#v", property)
	}
	if string(property.ExtraFields["Future"]) != `{"v":2}` {
		t.Fatalf("Future = %s", property.ExtraFields["Future"])
	}
	marshaled, err := dynamics.MarshalPropertyChangeInteraction(property)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(marshaled, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !jsonEqual(got, want) {
		t.Fatalf("round trip = %s, want %s", marshaled, raw)
	}
}

func TestReceiptBuildersMatchCapturedNullAndEmptyShapes(t *testing.T) {
	open := dynamics.BuildOpenNewReceiptMessage(10, "details", "new-receipt")
	openCommands := receiptCommands(t, open)
	if !oneParameter(openCommands[0].PositionalParameters, dynamics.SelectedControlImagePreviewReceipts) {
		t.Fatalf("open selection = %#v", openCommands[0].PositionalParameters)
	}
	if openCommands[1].PositionalParameters == nil || len(openCommands[1].PositionalParameters) != 0 {
		t.Fatalf("open Click parameters = %#v, want empty array", openCommands[1].PositionalParameters)
	}

	documents := dynamics.BuildReceiptDocuNameCheckFileMessages(11, "dialog", "upload", "receipt", "receipt.png")
	if len(documents) != 2 || documents[1].SequenceNumber != documents[0].SequenceNumber+1 {
		t.Fatalf("document messages = %#v", documents)
	}
	property, err := dynamics.ParsePropertyChangeInteraction(documents[0].Interactions[0])
	if err != nil {
		t.Fatal(err)
	}
	if property.NoAsyncIncrement != nil || property.PropertyName != dynamics.PropertyDocuName {
		t.Fatalf("DocuName property = %#v", property)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(documents[0].Interactions[0], &object); err != nil {
		t.Fatal(err)
	}
	if _, present := object["NoAsyncIncrement"]; present {
		t.Fatal("DocuName unexpectedly serialized NoAsyncIncrement")
	}
	check := receiptCommands(t, documents[1])[0]
	if len(check.PositionalParameters) != 2 || check.PositionalParameters[0] != "receipt" || check.PositionalParameters[1] != "receipt.png" {
		t.Fatalf("CheckFile parameters = %#v", check.PositionalParameters)
	}

	complete := dynamics.BuildReceiptUploadedFileCloseDialogMessage(13, "dialog", "upload", "file-id")
	uploaded, err := dynamics.ParsePropertyChangeInteraction(complete.Interactions[0])
	if err != nil {
		t.Fatal(err)
	}
	if uploaded.NoAsyncIncrement == nil || !*uploaded.NoAsyncIncrement || uploaded.PropertyName != dynamics.PropertyUploadedFileID {
		t.Fatalf("UploadedFileId property = %#v", uploaded)
	}
	closeDialog, err := dynamics.ParseCommandInteraction(complete.Interactions[1])
	if err != nil {
		t.Fatal(err)
	}
	if closeDialog.PositionalParameters != nil {
		t.Fatalf("CloseDialog parameters = %#v, want null", closeDialog.PositionalParameters)
	}

	ok := receiptCommands(t, dynamics.BuildReceiptOKClickMessage(14, "dialog", "ok"))[0]
	if ok.PositionalParameters != nil {
		t.Fatalf("OK parameters = %#v, want null", ok.PositionalParameters)
	}
	close := receiptCommands(t, dynamics.BuildReceiptCloseButtonMessage(15, "dialog", "close"))[0]
	if close.PositionalParameters != nil {
		t.Fatalf("Close parameters = %#v, want null", close.PositionalParameters)
	}
}

func TestValidateReceiptCommandsAcceptsEveryCapturedStage(t *testing.T) {
	targets := receiptTargets()
	documents := dynamics.BuildReceiptDocuNameCheckFileMessages(11, targets.DialogRootID, targets.UploadControlID, "receipt", "receipt.png")
	stages := map[string]dynamics.Envelope{
		"open":       {Messages: []dynamics.Message{dynamics.BuildOpenNewReceiptMessage(10, targets.DetailsRootID, targets.NewReceiptButtonID)}},
		"name-check": {Messages: documents},
		"check":      {Messages: []dynamics.Message{dynamics.BuildReceiptCheckFileMessage(13, targets.DialogRootID, targets.UploadControlID, "receipt", "receipt.png")}},
		"complete":   {Messages: []dynamics.Message{dynamics.BuildReceiptUploadedFileCloseDialogMessage(14, targets.DialogRootID, targets.UploadControlID, "file-id")}},
		"ok":         {Messages: []dynamics.Message{dynamics.BuildReceiptOKClickMessage(15, targets.DialogRootID, targets.OKButtonID)}},
		"close":      {Messages: []dynamics.Message{dynamics.BuildReceiptCloseButtonMessage(16, targets.DialogRootID, targets.CloseButtonID)}},
		"save":       {Messages: []dynamics.Message{dynamics.BuildSaveAndCloseClickMessage(16, targets.DetailsRootID, targets.SaveAndCloseID)}},
	}
	for name, envelope := range stages {
		t.Run(name, func(t *testing.T) {
			if err := dynamics.ValidateReceiptCommands(envelope, targets); err != nil {
				t.Fatalf("ValidateReceiptCommands() error = %v", err)
			}
		})
	}
}

func TestValidateReceiptCommandsRejectsForbiddenCommandAtEveryStage(t *testing.T) {
	targets := receiptTargets()
	documents := dynamics.BuildReceiptDocuNameCheckFileMessages(11, targets.DialogRootID, targets.UploadControlID, "receipt", "receipt.png")
	stages := map[string]dynamics.Envelope{
		"open":       {Messages: []dynamics.Message{dynamics.BuildOpenNewReceiptMessage(10, targets.DetailsRootID, targets.NewReceiptButtonID)}},
		"name-check": {Messages: documents},
		"check":      {Messages: []dynamics.Message{dynamics.BuildReceiptCheckFileMessage(13, targets.DialogRootID, targets.UploadControlID, "receipt", "receipt.png")}},
		"complete":   {Messages: []dynamics.Message{dynamics.BuildReceiptUploadedFileCloseDialogMessage(14, targets.DialogRootID, targets.UploadControlID, "file-id")}},
		"ok":         {Messages: []dynamics.Message{dynamics.BuildReceiptOKClickMessage(15, targets.DialogRootID, targets.OKButtonID)}},
		"close":      {Messages: []dynamics.Message{dynamics.BuildReceiptCloseButtonMessage(16, targets.DialogRootID, targets.CloseButtonID)}},
		"save":       {Messages: []dynamics.Message{dynamics.BuildSaveAndCloseClickMessage(16, targets.DetailsRootID, targets.SaveAndCloseID)}},
	}
	for name, envelope := range stages {
		t.Run(name, func(t *testing.T) {
			mutated := false
			for messageIndex := range envelope.Messages {
				for interactionIndex, raw := range envelope.Messages[messageIndex].Interactions {
					command, err := dynamics.ParseCommandInteraction(raw)
					if err == nil && command.Type == dynamics.InteractionTypeCommand {
						command.CommandName = "SubmitWorkflowPostApproveRecall"
						envelope.Messages[messageIndex].Interactions[interactionIndex] = receiptRawCommand(t, command)
						mutated = true
						break
					}
				}
				if mutated {
					break
				}
			}
			if !mutated {
				t.Fatal("stage had no command to mutate")
			}
			if err := dynamics.ValidateReceiptCommands(envelope, targets); !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}

func TestValidateReceiptCommandsRejectsWrongOrderTargetsExtrasAndWireShapes(t *testing.T) {
	targets := receiptTargets()
	validDocuments := func() dynamics.Envelope {
		return dynamics.Envelope{Messages: dynamics.BuildReceiptDocuNameCheckFileMessages(20, targets.DialogRootID, targets.UploadControlID, "receipt", "receipt.png")}
	}
	tests := map[string]func() dynamics.Envelope{
		"open reversed": func() dynamics.Envelope {
			message := dynamics.BuildOpenNewReceiptMessage(20, targets.DetailsRootID, targets.NewReceiptButtonID)
			message.Interactions[0], message.Interactions[1] = message.Interactions[1], message.Interactions[0]
			return dynamics.Envelope{Messages: []dynamics.Message{message}}
		},
		"document messages reversed": func() dynamics.Envelope {
			envelope := validDocuments()
			envelope.Messages[0], envelope.Messages[1] = envelope.Messages[1], envelope.Messages[0]
			return envelope
		},
		"upload interactions reversed": func() dynamics.Envelope {
			message := dynamics.BuildReceiptUploadedFileCloseDialogMessage(20, targets.DialogRootID, targets.UploadControlID, "file-id")
			message.Interactions[0], message.Interactions[1] = message.Interactions[1], message.Interactions[0]
			return dynamics.Envelope{Messages: []dynamics.Message{message}}
		},
		"wrong root": func() dynamics.Envelope {
			message := dynamics.BuildReceiptCheckFileMessage(20, "wrong", targets.UploadControlID, "receipt", "receipt.png")
			return dynamics.Envelope{Messages: []dynamics.Message{message}}
		},
		"envelope extra": func() dynamics.Envelope {
			envelope := validDocuments()
			envelope.ExtraFields = map[string]json.RawMessage{"Future": json.RawMessage(`true`)}
			return envelope
		},
		"message extra": func() dynamics.Envelope {
			envelope := validDocuments()
			envelope.Messages[0].ExtraFields = map[string]json.RawMessage{"Future": json.RawMessage(`true`)}
			return envelope
		},
		"property extra": func() dynamics.Envelope {
			envelope := validDocuments()
			property, _ := dynamics.ParsePropertyChangeInteraction(envelope.Messages[0].Interactions[0])
			property.ExtraFields = map[string]json.RawMessage{"Future": json.RawMessage(`true`)}
			envelope.Messages[0].Interactions[0], _ = dynamics.MarshalPropertyChangeInteraction(property)
			return envelope
		},
		"command extra": func() dynamics.Envelope {
			envelope := validDocuments()
			command := receiptCommands(t, envelope.Messages[1])[0]
			command.ExtraFields = map[string]json.RawMessage{"Future": json.RawMessage(`true`)}
			envelope.Messages[1].Interactions[0] = receiptRawCommand(t, command)
			return envelope
		},
		"open click null": func() dynamics.Envelope {
			message := dynamics.BuildOpenNewReceiptMessage(20, targets.DetailsRootID, targets.NewReceiptButtonID)
			command, _ := dynamics.ParseCommandInteraction(message.Interactions[1])
			command.PositionalParameters = nil
			message.Interactions[1] = receiptRawCommand(t, command)
			return dynamics.Envelope{Messages: []dynamics.Message{message}}
		},
		"close dialog empty": func() dynamics.Envelope {
			message := dynamics.BuildReceiptUploadedFileCloseDialogMessage(20, targets.DialogRootID, targets.UploadControlID, "file-id")
			command, _ := dynamics.ParseCommandInteraction(message.Interactions[1])
			command.PositionalParameters = []any{}
			message.Interactions[1] = receiptRawCommand(t, command)
			return dynamics.Envelope{Messages: []dynamics.Message{message}}
		},
		"ok click empty": func() dynamics.Envelope {
			message := dynamics.BuildReceiptOKClickMessage(20, targets.DialogRootID, targets.OKButtonID)
			command := receiptCommands(t, message)[0]
			command.PositionalParameters = []any{}
			message.Interactions[0] = receiptRawCommand(t, command)
			return dynamics.Envelope{Messages: []dynamics.Message{message}}
		},
		"save click empty": func() dynamics.Envelope {
			message := dynamics.BuildSaveAndCloseClickMessage(20, targets.DetailsRootID, targets.SaveAndCloseID)
			command := receiptCommands(t, message)[0]
			command.PositionalParameters = []any{}
			message.Interactions[0] = receiptRawCommand(t, command)
			return dynamics.Envelope{Messages: []dynamics.Message{message}}
		},
		"DocuName NoAsync false": func() dynamics.Envelope {
			envelope := validDocuments()
			envelope.Messages[0].Interactions[0] = json.RawMessage(`{"$type":"PropertyChangeInteraction","PropertyName":"DocuName","RootId":"dialog-root","TargetId":"upload-control","NoAsyncIncrement":false,"NewValue":"receipt"}`)
			return envelope
		},
		"UploadedFileId omits NoAsync": func() dynamics.Envelope {
			message := dynamics.BuildReceiptUploadedFileCloseDialogMessage(20, targets.DialogRootID, targets.UploadControlID, "file-id")
			message.Interactions[0] = json.RawMessage(`{"$type":"PropertyChangeInteraction","PropertyName":"UploadedFileId","RootId":"dialog-root","TargetId":"upload-control","NewValue":"file-id"}`)
			return dynamics.Envelope{Messages: []dynamics.Message{message}}
		},
		"property reserved smuggling": func() dynamics.Envelope {
			envelope := validDocuments()
			envelope.Messages[0].Interactions[0] = json.RawMessage(`{"$type":"PropertyChangeInteraction","PropertyName":"UploadedFileId","propertyname":"DocuName","RootId":"dialog-root","TargetId":"upload-control","NewValue":"receipt"}`)
			return envelope
		},
		"duplicate forbidden command smuggling": func() dynamics.Envelope {
			message := dynamics.BuildReceiptOKClickMessage(20, targets.DialogRootID, targets.OKButtonID)
			message.Interactions[0] = json.RawMessage(`{"$type":"CommandInteraction","CommandName":"Submit","CommandName":"Click","CallbackId":"","FailureCallbackId":"","NamedParameters":{},"NoAsyncIncrement":false,"PositionalParameters":null,"PriorityPosition":false,"ResetThrottleTime":false,"RootId":"dialog-root","ShouldBlockOnExecution":true,"TargetId":"ok-button","Throttle":true,"ThrottleId":"dialog-root_TG","ThrottleTimestamp":0,"ThrottleValue":0,"Telemetry":true}`)
			return dynamics.Envelope{Messages: []dynamics.Message{message}}
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			if err := dynamics.ValidateReceiptCommands(build(), targets); !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}

func receiptTargets() dynamics.ReceiptCommandTargets {
	return dynamics.ReceiptCommandTargets{
		DetailsRootID: "details-root", NewReceiptButtonID: "new-receipt-button",
		DialogRootID: "dialog-root", UploadControlID: "upload-control",
		OKButtonID: "ok-button", CloseButtonID: "close-button", SaveAndCloseID: "save-button",
	}
}

func receiptCommands(t *testing.T, message dynamics.Message) []dynamics.CommandInteraction {
	t.Helper()
	commands, err := message.CommandInteractions()
	if err != nil {
		t.Fatal(err)
	}
	return commands
}

func receiptRawCommand(t *testing.T, command dynamics.CommandInteraction) json.RawMessage {
	t.Helper()
	raw, err := dynamics.MarshalCommandInteraction(command)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func oneParameter(parameters []any, expected string) bool {
	return len(parameters) == 1 && parameters[0] == expected
}

func jsonEqual(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return strings.TrimSpace(string(leftJSON)) == strings.TrimSpace(string(rightJSON))
}
