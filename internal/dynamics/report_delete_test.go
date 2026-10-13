package dynamics_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func deleteTargets() dynamics.DeleteReportCommandTargets {
	return dynamics.DeleteReportCommandTargets{
		WorkspaceRootID: "3_0",
		ReportsGridID:   "100_0",
		DeleteButtonID:  "95_0",
		ConfirmRootID:   "115_0",
		ConfirmYesID:    "122_0",
	}
}

func TestValidateDeleteReportCommandsAllowsCapturedShapes(t *testing.T) {
	selectRow, err := dynamics.BuildSelectReportRowMessage(11, "3_0", "100_0", "19", "13")
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
			if err := dynamics.ValidateDeleteReportCommands(dynamics.Envelope{Messages: []dynamics.Message{message}}, deleteTargets()); err != nil {
				t.Fatalf("ValidateDeleteReportCommands() error = %v", err)
			}
		})
	}
}

func TestValidateDeleteReportCommandsRejectsOtherTargetsAndShapes(t *testing.T) {
	otherGrid, err := dynamics.BuildSelectReportRowMessage(11, "3_0", "200_0", "19", "13")
	if err != nil {
		t.Fatal(err)
	}
	markAll, err := dynamics.BuildSelectReportRowMessage(11, "3_0", "100_0", "19", "13")
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
			err := dynamics.ValidateDeleteReportCommands(dynamics.Envelope{Messages: []dynamics.Message{message}}, deleteTargets())
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
