package dynamics

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	// Details-form controls that open the report header editor.
	ControlMoreActions       = "MoreActions"
	ControlEditExpenseReport = "EditExpenseReport"

	// Edit expense report dialog controls.
	FormEditExpenseReport         = "ExpenseEditExpenseReport_form"
	ControlSelectInterimApprovers = "Expense_SelectInterimApprovers"
	ControlFinalApprover          = "Expense_FinalApprover"
	ControlFinalApproverName      = "Expense_FinalApprover_DirPerson_FK_Name"
	ControlEditReportOK           = "OK"

	// Interim approvers dialog controls.
	FormInterimApprovers        = "TrvInterimApproversDialog_form"
	ControlApproverGrid         = "ApproverGrid"
	ControlApproverNewButton    = "NewButton"
	ControlApproverDeleteButton = "DeleteButton"
	ControlApproverWorker       = "TrvInterimApprover_ApproverWorker"
	ControlApproverWorkerName   = "TrvInterimApprover_ApproverWorker_DirPerson_FK_Name"
	ControlApproverAlias        = "UserAlias"
	ControlApproversOK          = "OKButton"

	// SelectedControlApproverAlias is the captured focus target sent with the
	// approvers dialog's Delete click.
	SelectedControlApproverAlias = ControlApproverAlias

	// FormWorkerLookup is the worker lookup Dynamics opens when a name matches
	// more than one worker.
	FormWorkerLookup = "HcmWorkerLookUp_form"

	DataSourceInterimApprovers = "TrvInterimApprover_ds"
)

// generatedNameDataSource matches the DatasourceN_ds data sources Dynamics
// generates for a person reference's Name field. N differs between forms.
var generatedNameDataSource = regexp.MustCompile(`^Datasource[0-9]+_ds$`)

// ReportHeaderCommandTargets is the session-scoped allowlist used by
// ValidateReportHeaderCommands. Callers populate only the IDs admitted for the
// current stage.
type ReportHeaderCommandTargets struct {
	DetailsRootID  string
	MoreActionsID  string
	EditMenuItemID string

	EditRootID          string
	PurposeID           string
	FinalApproverID     string
	FinalApproverNameID string
	SelectInterimID     string
	EditOKID            string

	ApproversRootID string
	ApproverGridID  string
	ApproverNewID   string
	ApproverDelete  string
	WorkerID        string
	WorkerNameID    string
	AliasID         string
	ApproversOKID   string

	ConfirmRootID string
	ConfirmYesID  string
}

// BuildMenuItemClickMessage clicks a menu item or plain button. The captured
// clicks use empty, not null, PositionalParameters.
func BuildMenuItemClickMessage(sequence int64, rootID, targetID string) Message {
	command := baseCommand(CommandClick, rootID, targetID)
	command.PositionalParameters = []any{}
	command.ShouldBlockOnExecution = boolPointer(true)
	command.Throttle = true
	command.ThrottleID = rootID + "_TG"
	command.Telemetry = true
	return commandMessage(sequence, command)
}

// BuildNewApproverMessage adds a row to the interim approvers grid.
func BuildNewApproverMessage(sequence int64, approversRootID, newButtonID string) Message {
	return buildSelectAndClickMessage(sequence, approversRootID, newButtonID, ControlApproverNewButton, nil)
}

// BuildDeleteApproverMessage deletes the active interim approvers grid row.
// Dynamics asks for Yes/No confirmation only for a saved row.
func BuildDeleteApproverMessage(sequence int64, approversRootID, deleteButtonID string) Message {
	return buildSelectAndClickMessage(sequence, approversRootID, deleteButtonID, SelectedControlApproverAlias, nil)
}

// ValidateReportHeaderCommands permits only the captured header-editing
// shapes: open the Actions menu and its Edit expense report item, set the
// title, resolve the final approver or an approver row's worker by name, open
// the interim approvers dialog, add, select, or delete its rows, set a row's
// user alias, confirm a
// deletion with Yes, and click a dialog's OK. Set-as-default buttons are never
// admitted.
func ValidateReportHeaderCommands(envelope Envelope, targets ReportHeaderCommandTargets) error {
	if err := validateForbiddenCommandInvariant(envelope); err != nil {
		return err
	}
	switch len(envelope.Messages) {
	case 1:
	case 2:
		return validateReferenceNameMessages(envelope.Messages, targets)
	default:
		return unsafe(-1, -1, "", "envelope message shape is not part of the header flow")
	}
	message := envelope.Messages[0]
	if len(message.Interactions) == 1 {
		names, err := topLevelStringFields(message.Interactions[0], "CommandName")
		if err == nil && len(names) == 1 && names[0] == CommandChangeSelectedIndex {
			if targets.ApproversRootID == "" || targets.ApproverGridID == "" {
				return unsafe(0, 0, names[0], "approver selection targets are incomplete")
			}
			return validateChangeSelectedIndex(message, targets.ApproversRootID, targets.ApproverGridID, MarkModeActivate)
		}
	}
	commands, err := message.CommandInteractions()
	if err != nil {
		return unsafe(0, -1, "", err.Error())
	}
	switch len(commands) {
	case 1:
		return validateHeaderSingleCommand(commands[0], targets)
	case 2:
		return validateApproverSelectAndClick(commands, targets)
	default:
		return unsafe(0, -1, "", "header message has an unsupported interaction count")
	}
}

func validateHeaderSingleCommand(command CommandInteraction, targets ReportHeaderCommandTargets) error {
	switch command.CommandName {
	case CommandSetValue:
		for _, allowed := range []struct{ rootID, targetID string }{
			{targets.EditRootID, targets.PurposeID},
			{targets.ApproversRootID, targets.AliasID},
		} {
			if allowed.rootID == "" || allowed.targetID == "" || command.RootID != allowed.rootID || command.TargetID != allowed.targetID {
				continue
			}
			if err := validateSetValueShape(command, allowed.rootID, allowed.targetID); err != nil {
				return unsafe(0, 0, command.CommandName, err.Error())
			}
			return nil
		}
		return unsafe(0, 0, command.CommandName, "SetValue target is not the title or an approver alias")
	case CommandClick:
		for _, allowed := range []struct {
			rootID, targetID string
			nullParameters   bool
		}{
			{targets.DetailsRootID, targets.MoreActionsID, true},
			{targets.DetailsRootID, targets.EditMenuItemID, false},
			{targets.EditRootID, targets.SelectInterimID, false},
			{targets.EditRootID, targets.EditOKID, true},
			{targets.ApproversRootID, targets.ApproversOKID, true},
			{targets.ConfirmRootID, targets.ConfirmYesID, true},
		} {
			if allowed.rootID == "" || allowed.targetID == "" || command.RootID != allowed.rootID || command.TargetID != allowed.targetID {
				continue
			}
			if err := validateClick(command, allowed.rootID, allowed.targetID, allowed.nullParameters); err != nil {
				return unsafe(0, 0, command.CommandName, err.Error())
			}
			return nil
		}
		return unsafe(0, 0, command.CommandName, "Click target is not an allowlisted header control")
	default:
		return unsafe(0, 0, command.CommandName, "command is not part of the header flow")
	}
}

func validateApproverSelectAndClick(commands []CommandInteraction, targets ReportHeaderCommandTargets) error {
	update, click := commands[0], commands[1]
	for _, allowed := range []struct{ targetID, selected string }{
		{targets.ApproverNewID, ControlApproverNewButton},
		{targets.ApproverDelete, SelectedControlApproverAlias},
	} {
		root := targets.ApproversRootID
		if root == "" || allowed.targetID == "" || click.RootID != root || click.TargetID != allowed.targetID {
			continue
		}
		if err := validateCommon(update, CommandUpdateLastSelectedControl, root, root); err != nil {
			return unsafe(0, 0, update.CommandName, err.Error())
		}
		if !update.NoAsyncIncrement || update.Telemetry || update.Throttle || !shouldBlockOmitted(update) ||
			!oneString(update.PositionalParameters, allowed.selected) || len(update.ExtraFields) != 0 {
			return unsafe(0, 0, update.CommandName, "UpdateLastSelectedControl shape does not match the captured approvers flow")
		}
		if err := validateClick(click, root, allowed.targetID, true); err != nil {
			return unsafe(0, 1, click.CommandName, err.Error())
		}
		return nil
	}
	return unsafe(0, 1, click.CommandName, "Click target is not the allowlisted approver New or Delete button")
}

func validateReferenceNameMessages(messages []Message, targets ReportHeaderCommandTargets) error {
	if messages[1].SequenceNumber != messages[0].SequenceNumber+1 {
		return unsafe(1, -1, "", "SetValue and ResolveChanges sequences must be consecutive")
	}
	set, err := messages[0].CommandInteractions()
	if err != nil || len(set) != 1 {
		return unsafe(0, -1, "", "first reference message must contain exactly one SetValue")
	}
	resolve, err := messages[1].CommandInteractions()
	if err != nil || len(resolve) != 1 {
		return unsafe(1, -1, "", "second reference message must contain exactly one ResolveChanges")
	}
	for _, allowed := range []struct{ rootID, groupID, nameID string }{
		{targets.EditRootID, targets.FinalApproverID, targets.FinalApproverNameID},
		{targets.ApproversRootID, targets.WorkerID, targets.WorkerNameID},
	} {
		if allowed.rootID == "" || allowed.groupID == "" || allowed.nameID == "" ||
			set[0].RootID != allowed.rootID || set[0].TargetID != allowed.nameID {
			continue
		}
		if err := validateSetValueShape(set[0], allowed.rootID, allowed.nameID); err != nil {
			return unsafe(0, 0, set[0].CommandName, err.Error())
		}
		if err := validateResolveChanges(resolve[0], allowed.rootID, allowed.groupID); err != nil {
			return unsafe(1, 0, resolve[0].CommandName, err.Error())
		}
		return nil
	}
	return unsafe(0, 0, set[0].CommandName, "SetValue target is not an allowlisted approver field")
}

// FindForms returns every distinct form with the exact Name, oldest first by
// Dynamics' numeric view-model ID.
func (model ResponseModel) FindForms(name string) []ModelNode {
	forms := make([]ModelNode, 0, len(model.formsByName[name]))
	for _, form := range model.formsByName[name] {
		forms = append(forms, form)
	}
	if len(forms) == 0 {
		if form, ok := model.FindForm(name); ok {
			forms = append(forms, form)
		}
	}
	slices.SortFunc(forms, func(a, b ModelNode) int { return viewModelOrder(a.ID) - viewModelOrder(b.ID) })
	return forms
}

func viewModelOrder(id string) int {
	prefix, _, _ := strings.Cut(id, "_")
	order, err := strconv.Atoi(prefix)
	if err != nil {
		return 0
	}
	return order
}

// DiscoverPersonNames returns the Name_field of the generated person-name
// records under rootID, keyed by MasterTrackingId. Dynamics sends one such
// record per approver row, and one sharing the report record's tracking ID
// for its final approver.
func DiscoverPersonNames(data []byte, rootID string) (map[string]string, error) {
	value, err := decodeResponseValue(data)
	if err != nil {
		return nil, fmt.Errorf("discover person names: %w", err)
	}
	names := make(map[string]string)
	var walk func(any, string)
	walk = func(value any, currentRoot string) {
		switch typed := value.(type) {
		case map[string]any:
			if root := stringValue(typed["RootId"]); root != "" {
				currentRoot = root
			}
			if properties, ok := typed["Properties"].(map[string]any); ok && currentRoot == rootID &&
				generatedNameDataSource.MatchString(stringValue(properties["dataSourceName_internal"])) {
				tracking := stringValue(properties["MasterTrackingId"])
				if name, ok := scalarString(properties["Name_field"]); ok && tracking != "" {
					names[tracking] = name
				}
			}
			for _, child := range typed {
				walk(child, currentRoot)
			}
		case []any:
			for _, child := range typed {
				walk(child, currentRoot)
			}
		}
	}
	walk(value, "")
	return names, nil
}

// ResponseRemovesOnlyRow reports whether a response deletes the only row of
// a list. Dynamics then sends delete-only splices, one naming the row's
// tracking ID, and no active row instead of reloading the list.
func ResponseRemovesOnlyRow(data []byte, dataSource, rootID, trackingID string) (bool, error) {
	value, err := decodeResponseValue(data)
	if err != nil {
		return false, fmt.Errorf("discover %s deletion: %w", dataSource, err)
	}
	named, inserted, noActive := false, false, false
	var walk func(any, string)
	walk = func(value any, currentRoot string) {
		switch typed := value.(type) {
		case map[string]any:
			if root := stringValue(typed["RootId"]); root != "" {
				currentRoot = root
			}
			if currentRoot == rootID && stringValue(typed["Name"]) == dataSource {
				if active, ok := typed["ActiveMasterTrackingId"]; ok {
					noActive = stringValue(active) == "0"
				}
				operations, _ := typed["SpliceOperations"].([]any)
				for _, raw := range operations {
					operation, _ := raw.(map[string]any)
					inserts, _ := operation["Insert"].([]any)
					clear, _ := operation["ClearCache"].(bool)
					inserted = inserted || len(inserts) != 0 || clear
					named = named || stringValue(operation["MasterTrackingIndex"]) == trackingID
				}
			}
			for _, child := range typed {
				walk(child, currentRoot)
			}
		case []any:
			for _, child := range typed {
				walk(child, currentRoot)
			}
		}
	}
	walk(value, "")
	return named && !inserted && noActive, nil
}
