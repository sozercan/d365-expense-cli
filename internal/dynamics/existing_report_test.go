package dynamics_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func deleteTargets() dynamics.ReportCommandTargets {
	return dynamics.ReportCommandTargets{
		WorkspaceRootID: "3_0",
		ReportsGridID:   "100_0",
		MarkMode:        dynamics.MarkModeMark,
		DeleteButtonID:  "95_0",
		ConfirmRootID:   "115_0",
		ConfirmYesID:    "122_0",
	}
}

func TestValidateReportCommandsAllowsCapturedShapes(t *testing.T) {
	selectRow, err := dynamics.BuildChangeSelectedIndexMessage(11, "3_0", "100_0", "19", dynamics.MarkModeMark, "13")
	if err != nil {
		t.Fatal(err)
	}
	for name, message := range map[string]dynamics.Message{
		"refresh":     dynamics.BuildRefreshMessage(10, "3_0"),
		"select row":  selectRow,
		"delete":      dynamics.BuildDeleteReportClickMessage(12, "3_0", "95_0"),
		"confirm yes": dynamics.BuildSaveAndCloseClickMessage(13, "115_0", "122_0"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := dynamics.ValidateReportCommands(dynamics.Envelope{Messages: []dynamics.Message{message}}, deleteTargets()); err != nil {
				t.Fatalf("ValidateReportCommands() error = %v", err)
			}
		})
	}
}

func TestValidateReportCommandsRejectsOtherTargetsAndShapes(t *testing.T) {
	otherGrid, err := dynamics.BuildChangeSelectedIndexMessage(11, "3_0", "200_0", "19", dynamics.MarkModeMark, "13")
	if err != nil {
		t.Fatal(err)
	}
	markAll, err := dynamics.BuildChangeSelectedIndexMessage(11, "3_0", "100_0", "19", dynamics.MarkModeMark, "13")
	if err != nil {
		t.Fatal(err)
	}
	markAll.Interactions[0] = []byte(strings.Replace(string(markAll.Interactions[0]), `["19",1,"13",false,true]`, `["19",2,"13",false,true]`, 1))
	for name, message := range map[string]dynamics.Message{
		"other grid":         otherGrid,
		"other mark mode":    markAll,
		"confirm no":         dynamics.BuildSaveAndCloseClickMessage(13, "115_0", "123_0"),
		"other shortcut":     dynamics.BuildInvokeDefaultButtonMessage(10, "3_0"),
		"other delete click": dynamics.BuildDeleteReportClickMessage(12, "3_0", "96_0"),
		"submit button":      dynamics.BuildSubmitClickMessage(12, "3_0", "95_0"),
	} {
		t.Run(name, func(t *testing.T) {
			err := dynamics.ValidateReportCommands(dynamics.Envelope{Messages: []dynamics.Message{message}}, deleteTargets())
			if !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}

func TestDiscoverActiveTrackingIDRejectsConflicts(t *testing.T) {
	body := []byte(`{"Messages":[{"Interactions":[
		{"RootId":"3_0","Descriptor":{"Name":"TrvExpTable_ds","ActiveMasterTrackingId":"13"}},
		{"RootId":"3_0","Descriptor":{"Name":"TrvExpTable_ds","ActiveMasterTrackingId":"19"}}
	]}]}`)
	if _, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceReports, "3_0"); err == nil {
		t.Fatal("DiscoverActiveTrackingID() accepted conflicting active rows")
	}
}

func TestValidateReportCommandsAdmitsOnlyTheStageMarkModeAndLink(t *testing.T) {
	open := dynamics.ReportCommandTargets{WorkspaceRootID: "3_0", ReportsGridID: "95_0", MarkMode: dynamics.MarkModeActivate, ReportLinkID: "98_0"}
	activate, err := dynamics.BuildChangeSelectedIndexMessage(16, "3_0", "95_0", "429", dynamics.MarkModeActivate, "427")
	if err != nil {
		t.Fatal(err)
	}
	mark, err := dynamics.BuildChangeSelectedIndexMessage(16, "3_0", "95_0", "429", dynamics.MarkModeMark, "427")
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		message dynamics.Message
		allowed bool
	}{
		"activate row":    {activate, true},
		"open report":     {dynamics.BuildOpenReportMessage(17, "3_0", "98_0"), true},
		"mark row":        {mark, false},
		"other link":      {dynamics.BuildOpenReportMessage(17, "3_0", "99_0"), false},
		"delete for open": {dynamics.BuildDeleteReportClickMessage(17, "3_0", "90_0"), false},
	} {
		t.Run(name, func(t *testing.T) {
			err := dynamics.ValidateReportCommands(dynamics.Envelope{Messages: []dynamics.Message{test.message}}, open)
			if test.allowed && err != nil {
				t.Fatalf("ValidateReportCommands() error = %v", err)
			}
			if !test.allowed && !errors.Is(err, dynamics.ErrUnsafeCommand) {
				t.Fatalf("error = %v, want ErrUnsafeCommand", err)
			}
		})
	}
}

func TestValidateExpenseLineCommandsAllowsOnlyLineActivation(t *testing.T) {
	targets := dynamics.ExpenseLineCommandTargets{DetailsRootID: "108_0", LinesGridID: "367_0"}
	activate, err := dynamics.BuildChangeSelectedIndexMessage(13, "108_0", "367_0", "170", dynamics.MarkModeActivate, "160")
	if err != nil {
		t.Fatal(err)
	}
	if err := dynamics.ValidateExpenseLineCommands(dynamics.Envelope{Messages: []dynamics.Message{activate}}, targets); err != nil {
		t.Fatalf("ValidateExpenseLineCommands() error = %v", err)
	}
	mark, err := dynamics.BuildChangeSelectedIndexMessage(13, "108_0", "367_0", "170", dynamics.MarkModeMark, "160")
	if err != nil {
		t.Fatal(err)
	}
	if err := dynamics.ValidateExpenseLineCommands(dynamics.Envelope{Messages: []dynamics.Message{mark}}, targets); !errors.Is(err, dynamics.ErrUnsafeCommand) {
		t.Fatalf("error = %v, want ErrUnsafeCommand", err)
	}
}
