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
