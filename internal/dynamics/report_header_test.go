package dynamics_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

func headerTargets() dynamics.ReportHeaderCommandTargets {
	return dynamics.ReportHeaderCommandTargets{
		DetailsRootID: "110_0", MoreActionsID: "249_0", EditMenuItemID: "275_0",
		EditRootID: "284_0", PurposeID: "308_0", FinalApproverID: "315_0", FinalApproverNameID: "316_0",
		SelectInterimID: "313_0", EditOKID: "331_0",
		ApproversRootID: "342_0", ApproverGridID: "404_0", ApproverNewID: "399_0", ApproverDelete: "400_0",
		WorkerID: "407_0", WorkerNameID: "408_0", AliasID: "409_0", ApproversOKID: "411_0",
		ConfirmRootID: "713_0", ConfirmYesID: "720_0",
	}
}

// Captured browser interactions, with only the session-scoped control IDs
// shown above.
var capturedHeaderInteractions = map[string]string{
	"more actions": `{"$type":"CommandInteraction","CallbackId":"","CommandName":"Click","FailureCallbackId":"","NamedParameters":{},"NoAsyncIncrement":false,"PositionalParameters":null,"PriorityPosition":false,"ResetThrottleTime":false,"RootId":"110_0","ShouldBlockOnExecution":true,"TargetId":"249_0","Telemetry":true,"Throttle":true,"ThrottleId":"110_0_TG","ThrottleTimestamp":0,"ThrottleValue":0}`,
	"edit item":    `{"$type":"CommandInteraction","CallbackId":"","CommandName":"Click","FailureCallbackId":"","NamedParameters":{},"NoAsyncIncrement":false,"PositionalParameters":[],"PriorityPosition":false,"ResetThrottleTime":false,"RootId":"110_0","ShouldBlockOnExecution":true,"TargetId":"275_0","Telemetry":true,"Throttle":true,"ThrottleId":"110_0_TG","ThrottleTimestamp":0,"ThrottleValue":0}`,
	"approvers ok": `{"$type":"CommandInteraction","CallbackId":"","CommandName":"Click","FailureCallbackId":"","NamedParameters":{},"NoAsyncIncrement":false,"PositionalParameters":null,"PriorityPosition":false,"ResetThrottleTime":false,"RootId":"342_0","ShouldBlockOnExecution":true,"TargetId":"411_0","Telemetry":true,"Throttle":true,"ThrottleId":"342_0_TG","ThrottleTimestamp":0,"ThrottleValue":0}`,
}

func sameInteraction(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(got, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &right); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("built interaction\n%s\nwant captured\n%s", got, want)
	}
}

func TestReportHeaderBuildersMatchCapturedClicks(t *testing.T) {
	t.Parallel()
	sameInteraction(t, dynamics.BuildSaveAndCloseClickMessage(13, "110_0", "249_0").Interactions[0], capturedHeaderInteractions["more actions"])
	sameInteraction(t, dynamics.BuildMenuItemClickMessage(14, "110_0", "275_0").Interactions[0], capturedHeaderInteractions["edit item"])
	sameInteraction(t, dynamics.BuildSaveAndCloseClickMessage(33, "342_0", "411_0").Interactions[0], capturedHeaderInteractions["approvers ok"])
}

func TestValidateReportHeaderCommandsAllowsCapturedShapes(t *testing.T) {
	t.Parallel()
	selectRow, err := dynamics.BuildChangeSelectedIndexMessage(31, "342_0", "404_0", "733", dynamics.MarkModeActivate, "745")
	if err != nil {
		t.Fatal(err)
	}
	for name, messages := range map[string][]dynamics.Message{
		"more actions":   {dynamics.BuildSaveAndCloseClickMessage(13, "110_0", "249_0")},
		"edit item":      {dynamics.BuildMenuItemClickMessage(14, "110_0", "275_0")},
		"title":          {dynamics.BuildSetValueMessage(15, "284_0", "308_0", "Offsite 2030", "", "")},
		"final approver": dynamics.BuildReferenceNameMessages(16, "284_0", "315_0", "316_0", "Casey Example"),
		"select interim": {dynamics.BuildMenuItemClickMessage(19, "284_0", "313_0")},
		"new row":        {dynamics.BuildNewApproverMessage(20, "342_0", "399_0")},
		"worker":         dynamics.BuildReferenceNameMessages(21, "342_0", "407_0", "408_0", "Blake Example"),
		"alias":          {dynamics.BuildSetValueMessage(22, "342_0", "409_0", "bexample", "", "")},
		"select row":     {selectRow},
		"delete row":     {dynamics.BuildDeleteApproverMessage(25, "342_0", "400_0")},
		"confirm delete": {dynamics.BuildSaveAndCloseClickMessage(26, "713_0", "720_0")},
		"approvers ok":   {dynamics.BuildSaveAndCloseClickMessage(33, "342_0", "411_0")},
		"edit ok":        {dynamics.BuildSaveAndCloseClickMessage(35, "284_0", "331_0")},
	} {
		if err := dynamics.ValidateReportHeaderCommands(dynamics.Envelope{Messages: messages}, headerTargets()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidateReportHeaderCommandsRejectsOtherControls(t *testing.T) {
	t.Parallel()
	for name, messages := range map[string][]dynamics.Message{
		"set as default final approver": {dynamics.BuildSaveAndCloseClickMessage(15, "284_0", "317_0")},
		"set as default interim":        {dynamics.BuildMenuItemClickMessage(15, "342_0", "401_0")},
		"edit item as plain click":      {dynamics.BuildSaveAndCloseClickMessage(14, "110_0", "275_0")},
		"title in the approvers dialog": {dynamics.BuildSetValueMessage(15, "342_0", "308_0", "x", "", "")},
		"alias in the edit dialog":      {dynamics.BuildSetValueMessage(15, "284_0", "409_0", "x", "", "")},
		"worker resolved on final":      dynamics.BuildReferenceNameMessages(16, "284_0", "315_0", "408_0", "x"),
		"cancel":                        {dynamics.BuildSaveAndCloseClickMessage(15, "284_0", "332_0")},
		"submit":                        {dynamics.BuildSubmitClickMessage(15, "110_0", "SubmitButton")},
	} {
		err := dynamics.ValidateReportHeaderCommands(dynamics.Envelope{Messages: messages}, headerTargets())
		if !errors.Is(err, dynamics.ErrUnsafeCommand) {
			t.Errorf("%s: error = %v, want ErrUnsafeCommand", name, err)
		}
	}
}

func TestFindFormsAndPersonNames(t *testing.T) {
	t.Parallel()
	dialog := func(id string) map[string]any {
		return map[string]any{"$type": "CreateViewModelInteraction", "RootId": id, "Descriptor": map[string]any{
			"Id": id, "Name": dynamics.FormInterimApprovers, "TypeName": "Dialog",
			"ChildModelCollections": map[string]any{"Datasource1_ds": map[string]any{"Items": []any{
				map[string]any{"Item": map[string]any{"Properties": map[string]any{
					"dataSourceName_internal": "Datasource1_ds", "MasterTrackingId": "361", "Name_field": "Avery Example " + id,
				}}},
			}}},
		}}
	}
	body, err := json.Marshal(map[string]any{"Messages": []any{map[string]any{"Interactions": []any{dialog("342_0"), dialog("337_0")}}}})
	if err != nil {
		t.Fatal(err)
	}
	model, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		t.Fatal(err)
	}
	forms := model.FindForms(dynamics.FormInterimApprovers)
	if len(forms) != 2 || forms[0].ID != "337_0" || forms[1].ID != "342_0" {
		t.Fatalf("FindForms() = %+v, want 337_0 then 342_0", forms)
	}
	names, err := dynamics.DiscoverPersonNames(body, "342_0")
	if err != nil || !reflect.DeepEqual(names, map[string]string{"361": "Avery Example 342_0"}) {
		t.Fatalf("DiscoverPersonNames() = %v, %v", names, err)
	}
}
