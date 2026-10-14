package dynamics_test

import (
	"errors"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func TestValidateReceiptMatchCommandsAllowsCapturedShapes(t *testing.T) {
	selectReceipt, err := dynamics.BuildChangeSelectedIndexMessage(17, "113_0", "326_0", "214", dynamics.MarkModeActivate, "212")
	if err != nil {
		t.Fatal(err)
	}
	selectLine, err := dynamics.BuildChangeSelectedIndexMessage(22, "350_0", "365_0", "356", dynamics.MarkModeActivate, "354")
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		message dynamics.Message
		targets dynamics.ReceiptMatchCommandTargets
	}{
		"select receipt": {selectReceipt, dynamics.ReceiptMatchCommandTargets{DetailsRootID: "113_0", ReceiptsGridID: "326_0"}},
		"open popup":     {dynamics.BuildOpenReceiptMatchMessage(21, "113_0", "346_0"), dynamics.ReceiptMatchCommandTargets{DetailsRootID: "113_0", MatchButtonID: "346_0"}},
		"select line":    {selectLine, dynamics.ReceiptMatchCommandTargets{PopupRootID: "350_0", PopupGridID: "365_0"}},
		"attach":         {dynamics.BuildAttachReceiptMessage(24, "350_0", "371_0"), dynamics.ReceiptMatchCommandTargets{PopupRootID: "350_0", AttachButtonID: "371_0"}},
		"close popup":    {dynamics.BuildClosePopupMessage(25, "350_0"), dynamics.ReceiptMatchCommandTargets{PopupRootID: "350_0"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := dynamics.ValidateReceiptMatchCommands(dynamics.Envelope{Messages: []dynamics.Message{test.message}}, test.targets); err != nil {
				t.Fatalf("ValidateReceiptMatchCommands() error = %v", err)
			}
		})
	}
}

func TestValidateReceiptMatchCommandsRejectsOtherTargets(t *testing.T) {
	popup := dynamics.BuildOpenReceiptMatchMessage(21, "113_0", "346_0")
	popup.SequenceNumber = 22 // Arg1 must follow the message sequence.
	for name, test := range map[string]struct {
		message dynamics.Message
		targets dynamics.ReceiptMatchCommandTargets
	}{
		"popup counter":     {popup, dynamics.ReceiptMatchCommandTargets{DetailsRootID: "113_0", MatchButtonID: "346_0"}},
		"other popup":       {dynamics.BuildOpenReceiptMatchMessage(21, "113_0", "347_0"), dynamics.ReceiptMatchCommandTargets{DetailsRootID: "113_0", MatchButtonID: "346_0"}},
		"other attach":      {dynamics.BuildAttachReceiptMessage(24, "350_0", "372_0"), dynamics.ReceiptMatchCommandTargets{PopupRootID: "350_0", AttachButtonID: "371_0"}},
		"close report form": {dynamics.BuildClosePopupMessage(25, "113_0"), dynamics.ReceiptMatchCommandTargets{PopupRootID: "350_0"}},
		"submit":            {dynamics.BuildSubmitClickMessage(25, "113_0", "354_0"), dynamics.ReceiptMatchCommandTargets{DetailsRootID: "113_0"}},
	} {
		t.Run(name, func(t *testing.T) {
			err := dynamics.ValidateReceiptMatchCommands(dynamics.Envelope{Messages: []dynamics.Message{test.message}}, test.targets)
			if !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}

func TestValidateReceiptRemoveCommandsAllowsOnlyCapturedShapes(t *testing.T) {
	targets := dynamics.ReceiptRemoveCommandTargets{DetailsRootID: "115_0", ReceiptsGridID: "326_0", RemoveButtonID: "241_0", ConfirmRootID: "266_0", ConfirmYesID: "273_0"}
	selectReceipt, err := dynamics.BuildChangeSelectedIndexMessage(16, "115_0", "326_0", "169", dynamics.MarkModeActivate, "167")
	if err != nil {
		t.Fatal(err)
	}
	mark, err := dynamics.BuildChangeSelectedIndexMessage(16, "115_0", "326_0", "169", dynamics.MarkModeMark, "167")
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		message dynamics.Message
		allowed bool
	}{
		"select receipt": {selectReceipt, true},
		"remove":         {dynamics.BuildRemoveReceiptClickMessage(17, "115_0", "241_0"), true},
		"confirm yes":    {dynamics.BuildSaveAndCloseClickMessage(18, "266_0", "273_0"), true},
		"mark receipt":   {mark, false},
		"confirm no":     {dynamics.BuildSaveAndCloseClickMessage(18, "266_0", "274_0"), false},
		"other button":   {dynamics.BuildRemoveReceiptClickMessage(17, "115_0", "242_0"), false},
		"report delete":  {dynamics.BuildDeleteReportClickMessage(17, "115_0", "241_0"), false},
	} {
		t.Run(name, func(t *testing.T) {
			err := dynamics.ValidateReceiptRemoveCommands(dynamics.Envelope{Messages: []dynamics.Message{test.message}}, targets)
			if test.allowed && err != nil {
				t.Fatalf("ValidateReceiptRemoveCommands() error = %v", err)
			}
			if !test.allowed && !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}
