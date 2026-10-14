package dynamics_test

import (
	"errors"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func TestValidateWorkspaceItemCommandsAllowsOnlyCapturedShapes(t *testing.T) {
	targets := dynamics.WorkspaceItemCommandTargets{
		WorkspaceRootID: "3_0", TabIDs: []string{"174_0", "196_0", "197_0"}, GridID: "206_0",
		DeleteButtonID: "205_0", SelectedControl: dynamics.SelectedControlExpenseDate, ConfirmRootID: "217_0", ConfirmYesID: "224_0",
	}
	activate, err := dynamics.BuildActivateWorkspaceTabMessage(12, "3_0", "197_0")
	if err != nil {
		t.Fatal(err)
	}
	otherTab, err := dynamics.BuildActivateWorkspaceTabMessage(12, "3_0", "300_0")
	if err != nil {
		t.Fatal(err)
	}
	selectRow, err := dynamics.BuildChangeSelectedIndexMessage(13, "3_0", "206_0", "59", dynamics.MarkModeActivate, "57")
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		message dynamics.Message
		allowed bool
	}{
		"activate tab": {activate, true},
		"select row":   {selectRow, true},
		"delete":       {dynamics.BuildDeleteWorkspaceItemMessage(14, "3_0", "205_0", dynamics.SelectedControlExpenseDate), true},
		"confirm yes":  {dynamics.BuildSaveAndCloseClickMessage(15, "217_0", "224_0"), true},
		"other tab":    {otherTab, false},
		"other focus":  {dynamics.BuildDeleteWorkspaceItemMessage(14, "3_0", "205_0", dynamics.SelectedControlReceiptName), false},
		"other delete": {dynamics.BuildDeleteWorkspaceItemMessage(14, "3_0", "206_0", dynamics.SelectedControlExpenseDate), false},
		"confirm no":   {dynamics.BuildSaveAndCloseClickMessage(15, "217_0", "225_0"), false},
		"refresh":      {dynamics.BuildRefreshMessage(16, "3_0"), false},
	} {
		t.Run(name, func(t *testing.T) {
			err := dynamics.ValidateWorkspaceItemCommands(dynamics.Envelope{Messages: []dynamics.Message{test.message}}, targets)
			if test.allowed && err != nil {
				t.Fatalf("ValidateWorkspaceItemCommands() error = %v", err)
			}
			if !test.allowed && !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}
