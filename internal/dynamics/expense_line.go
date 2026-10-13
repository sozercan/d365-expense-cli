package dynamics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

const (
	CommandResolveChanges = "ResolveChanges"

	InteractionTypeDeleteViewModel = "DeleteViewModelInteraction"

	FormExpenseAddNewExpense  = "ExpenseAddNewExpense_form"
	FormExpenseItemizeExpense = "ExpenseItemizeExpense_form"

	// Details-form controls used to add and itemize expense lines.
	ControlNewExpenseButton     = "NewExpenseButton"
	ControlExpenseCardMenu      = "CardBottomMenuFormMenuButtonControl"
	ControlItemizeExpenseButton = "ItemizeExpenseButton"

	// New-expense dialog controls.
	ControlCategoryInput = "CategoryInput"
	ControlDateInput     = "DateInput"
	ControlAmountInput   = "AmountInput"
	ControlCurrencyInput = "CurrencyInput"
	ControlMerchantInput = "MerchantInputNoLookup"
	ControlNotesInput    = "NotesInput"
	ControlSaveButton    = "SaveButton"

	// Itemize-expense dialog controls.
	ControlItemizationNewButton    = "NewButtonItemizationGroup"
	ControlItemizationSubcategory  = "ExpenseItemizationTransTmp_SubCategoryRecId"
	ControlItemizationSubcatName   = "ExpenseItemizationTransTmp_SubCategoryRecId_TrvSharedSubCategory_Name"
	ControlItemizationStartDate    = "ExpenseItemizationTransTmp_StartDate"
	ControlItemizationDailyRate    = "ExpenseItemizationTransTmp_DailyRate"
	ControlItemizationQuantity     = "ExpenseItemizationTransTmp_Quantity"
	ControlItemizationCloseButton  = "CloseButton"
	ControlItemizationTotal        = "TotalAmount"
	ControlItemizationRemaining    = "ToBeItemizedAmount"
	SelectedControlToBePaidTotal   = "ToBePaidTotal"
	SelectedControlItemizationDone = ControlItemizationCloseButton

	DataSourceExpenseLines       = "TrvExpTrans_ds"
	DataSourceItemizationRows    = "ExpenseItemizationTransTmp_ds"
	DataSourceItemizationSubcats = "Ref_TrvSharedSubCategory_SubCategory_TrvSharedSubCategory1_ds"

	itemizeMenuItemName = "ExpenseItemizeExpense"
)

// ExpenseLineCommandTargets is the session-scoped allowlist used by
// ValidateExpenseLineCommands. Callers populate only the IDs admitted for the
// current stage.
type ExpenseLineCommandTargets struct {
	DetailsRootID      string
	NewExpenseButtonID string
	CardMenuID         string
	ItemizeButtonID    string

	DialogRootID   string
	DialogFieldIDs []string
	SaveButtonID   string

	ItemizeRootID      string
	SubcategoryID      string
	SubcategoryNameID  string
	ItemizeFieldIDs    []string
	ItemizeNewButtonID string
	ItemizeCloseID     string
}

// BuildOpenNewExpenseMessage builds the captured request that opens the
// New expense dialog from an open report's details form.
func BuildOpenNewExpenseMessage(sequence int64, detailsRootID, newExpenseButtonID string) Message {
	return buildSelectAndClickMessage(sequence, detailsRootID, newExpenseButtonID, SelectedControlToBePaidTotal, nil)
}

// BuildOpenExpenseCardMenuMessage opens the active expense line's Actions menu.
func BuildOpenExpenseCardMenuMessage(sequence int64, detailsRootID, cardMenuID string) Message {
	return buildSelectAndClickMessage(sequence, detailsRootID, cardMenuID, SelectedControlToBePaidTotal, nil)
}

// BuildOpenItemizeExpenseMessage clicks the Actions menu's Itemize item. The
// captured menu-item click uses empty, not null, PositionalParameters.
func BuildOpenItemizeExpenseMessage(sequence int64, detailsRootID, itemizeButtonID string) Message {
	return buildSelectAndClickMessage(sequence, detailsRootID, itemizeButtonID, ControlExpenseCardMenu, []any{})
}

// BuildNewItemizationRowMessage clicks the itemization grid's New button.
func BuildNewItemizationRowMessage(sequence int64, itemizeRootID, newButtonID string) Message {
	return buildSelectAndClickMessage(sequence, itemizeRootID, newButtonID, SelectedControlItemizationDone, nil)
}

// BuildSubcategoryMessages sets an itemization subcategory by name and then
// resolves the reference group, as two consecutive messages.
func BuildSubcategoryMessages(sequence int64, itemizeRootID, subcategoryID, subcategoryNameID, name string) []Message {
	resolve := baseCommand(CommandResolveChanges, itemizeRootID, subcategoryID)
	resolve.Telemetry = true
	resolve.ExtraFields = map[string]json.RawMessage{"complete": json.RawMessage("null")}
	return []Message{
		BuildSetValueMessage(sequence, itemizeRootID, subcategoryNameID, name, "", ""),
		commandMessage(sequence+1, resolve),
	}
}

func buildSelectAndClickMessage(sequence int64, rootID, targetID, selectedControl string, parameters []any) Message {
	message := BuildUpdateLastSelectedControlClickMessage(sequence, rootID, targetID, selectedControl)
	if parameters == nil {
		click, err := ParseCommandInteraction(message.Interactions[1])
		if err != nil {
			panic(fmt.Sprintf("dynamics: parse built click: %v", err))
		}
		click.PositionalParameters = nil
		message.Interactions[1], err = MarshalCommandInteraction(click)
		if err != nil {
			panic(fmt.Sprintf("dynamics: marshal built click: %v", err))
		}
	}
	return message
}

// ValidateExpenseLineCommands permits only the captured expense-line and
// itemization request shapes against the supplied session-scoped targets.
func ValidateExpenseLineCommands(envelope Envelope, targets ExpenseLineCommandTargets) error {
	if err := validateForbiddenCommandInvariant(envelope); err != nil {
		return err
	}
	switch len(envelope.Messages) {
	case 1:
		commands, err := envelope.Messages[0].CommandInteractions()
		if err != nil {
			return unsafe(0, -1, "", err.Error())
		}
		switch len(commands) {
		case 1:
			return validateExpenseSingleCommand(commands[0], targets)
		case 2:
			return validateExpenseSelectAndClick(commands, targets)
		default:
			return unsafe(0, -1, "", "expense-line message has an unsupported interaction count")
		}
	case 2:
		if envelope.Messages[1].SequenceNumber != envelope.Messages[0].SequenceNumber+1 {
			return unsafe(1, -1, "", "SetValue and ResolveChanges sequences must be consecutive")
		}
		set, err := envelope.Messages[0].CommandInteractions()
		if err != nil || len(set) != 1 {
			return unsafe(0, -1, "", "first subcategory message must contain exactly one SetValue")
		}
		resolve, err := envelope.Messages[1].CommandInteractions()
		if err != nil || len(resolve) != 1 {
			return unsafe(1, -1, "", "second subcategory message must contain exactly one ResolveChanges")
		}
		if targets.ItemizeRootID == "" || targets.SubcategoryID == "" || targets.SubcategoryNameID == "" {
			return unsafe(0, 0, set[0].CommandName, "subcategory targets are incomplete")
		}
		if err := validateSetValueShape(set[0], targets.ItemizeRootID, targets.SubcategoryNameID); err != nil {
			return unsafe(0, 0, set[0].CommandName, err.Error())
		}
		if err := validateResolveChanges(resolve[0], targets.ItemizeRootID, targets.SubcategoryID); err != nil {
			return unsafe(1, 0, resolve[0].CommandName, err.Error())
		}
		return nil
	default:
		return unsafe(-1, -1, "", "envelope message shape is not part of the expense-line flow")
	}
}

func validateExpenseSingleCommand(command CommandInteraction, targets ExpenseLineCommandTargets) error {
	switch command.CommandName {
	case CommandSetValue:
		switch {
		case targets.DialogRootID != "" && command.RootID == targets.DialogRootID && slices.Contains(targets.DialogFieldIDs, command.TargetID):
			if err := validateSetValueShape(command, targets.DialogRootID, command.TargetID); err != nil {
				return unsafe(0, 0, command.CommandName, err.Error())
			}
			return nil
		case targets.ItemizeRootID != "" && command.RootID == targets.ItemizeRootID && slices.Contains(targets.ItemizeFieldIDs, command.TargetID):
			if err := validateSetValueShape(command, targets.ItemizeRootID, command.TargetID); err != nil {
				return unsafe(0, 0, command.CommandName, err.Error())
			}
			return nil
		}
		return unsafe(0, 0, command.CommandName, "SetValue target is not an allowlisted expense field")
	case CommandClick:
		for _, allowed := range []struct{ rootID, targetID string }{
			{targets.DialogRootID, targets.SaveButtonID},
			{targets.ItemizeRootID, targets.ItemizeCloseID},
		} {
			if allowed.rootID == "" || allowed.targetID == "" || command.RootID != allowed.rootID || command.TargetID != allowed.targetID {
				continue
			}
			if err := validateClick(command, allowed.rootID, allowed.targetID, true); err != nil {
				return unsafe(0, 0, command.CommandName, err.Error())
			}
			return nil
		}
		return unsafe(0, 0, command.CommandName, "Click target is not the allowlisted Save or itemization Close control")
	default:
		return unsafe(0, 0, command.CommandName, "command is not part of the expense-line flow")
	}
}

func validateExpenseSelectAndClick(commands []CommandInteraction, targets ExpenseLineCommandTargets) error {
	update, click := commands[0], commands[1]
	for _, allowed := range []struct {
		rootID, targetID, selected string
		nullParameters             bool
	}{
		{targets.DetailsRootID, targets.NewExpenseButtonID, SelectedControlToBePaidTotal, true},
		{targets.DetailsRootID, targets.CardMenuID, SelectedControlToBePaidTotal, true},
		{targets.DetailsRootID, targets.ItemizeButtonID, ControlExpenseCardMenu, false},
		{targets.ItemizeRootID, targets.ItemizeNewButtonID, SelectedControlItemizationDone, true},
	} {
		if allowed.rootID == "" || allowed.targetID == "" || click.RootID != allowed.rootID || click.TargetID != allowed.targetID {
			continue
		}
		if err := validateCommon(update, CommandUpdateLastSelectedControl, allowed.rootID, allowed.rootID); err != nil {
			return unsafe(0, 0, update.CommandName, err.Error())
		}
		if !update.NoAsyncIncrement || update.Telemetry || update.Throttle || !shouldBlockOmitted(update) ||
			!oneString(update.PositionalParameters, allowed.selected) || len(update.ExtraFields) != 0 {
			return unsafe(0, 0, update.CommandName, "UpdateLastSelectedControl shape does not match the captured expense-line flow")
		}
		if err := validateClick(click, allowed.rootID, allowed.targetID, allowed.nullParameters); err != nil {
			return unsafe(0, 1, click.CommandName, err.Error())
		}
		return nil
	}
	return unsafe(0, 1, click.CommandName, "Click target is not an allowlisted expense-line control")
}

func validateResolveChanges(command CommandInteraction, rootID, targetID string) error {
	if err := validateCommon(command, CommandResolveChanges, rootID, targetID); err != nil {
		return err
	}
	if command.NoAsyncIncrement || !command.Telemetry || command.Throttle || !shouldBlockOmitted(command) ||
		command.PositionalParameters != nil || !onlyNullComplete(command.ExtraFields) {
		return errors.New("ResolveChanges shape does not match the captured itemization flow")
	}
	return nil
}

func onlyNullComplete(extra map[string]json.RawMessage) bool {
	complete, ok := extra["complete"]
	return len(extra) == 1 && ok && bytes.Equal(bytes.TrimSpace(complete), []byte("null"))
}

// ValidateItemizeButton verifies the Actions menu's Itemize item before its
// dynamic ID is admitted to ExpenseLineCommandTargets.
func ValidateItemizeButton(node ModelNode, detailsRootID string) error {
	if node.ID == "" || node.Name != ControlItemizeExpenseButton || node.RootID != detailsRootID {
		return errors.New("Itemize menu item is not in the open report")
	}
	for _, expectation := range []struct {
		properties map[string]json.RawMessage
		name       string
		want       string
	}{
		{node.ValueProperties, "MenuItemName", itemizeMenuItemName},
		{node.ValueProperties, "PrimaryModelName", DataSourceExpenseLines},
		{node.SerializedValueProperties, "Enabled", "true"},
		{node.SerializedValueProperties, "Visible", "true"},
	} {
		if err := requireModelString(expectation.properties, expectation.name, expectation.want); err != nil {
			return fmt.Errorf("Itemize menu item metadata: %w", err)
		}
	}
	return nil
}

// DataRecord is one data-source record descriptor found in a response.
type DataRecord struct {
	RootID   string
	RecID    string
	Inserted bool
	Fields   map[string]string
}

// DiscoverDataRecords returns every record of the named data source in
// response order, with scalar fields as strings.
func DiscoverDataRecords(data []byte, dataSource string) ([]DataRecord, error) {
	value, err := decodeResponseValue(data)
	if err != nil {
		return nil, fmt.Errorf("discover %s records: %w", dataSource, err)
	}
	var records []DataRecord
	var walk func(any, string, bool)
	walk = func(value any, rootID string, inserted bool) {
		switch typed := value.(type) {
		case map[string]any:
			if root := stringValue(typed["RootId"]); root != "" {
				rootID = root
			}
			if properties, ok := typed["Properties"].(map[string]any); ok && stringValue(properties["dataSourceName_internal"]) == dataSource {
				fields := make(map[string]string, len(properties))
				for name, raw := range properties {
					if text, ok := scalarString(raw); ok {
						fields[name] = text
					}
				}
				records = append(records, DataRecord{RootID: rootID, RecID: fields["recId_internal"], Inserted: inserted, Fields: fields})
			}
			for key, child := range typed {
				walk(child, rootID, inserted || key == "Insert")
			}
		case []any:
			for _, child := range typed {
				walk(child, rootID, inserted)
			}
		}
	}
	walk(value, "", false)
	return records, nil
}

// ResponseDeletesViewModel reports whether a response closes the view model
// rooted at rootID.
func ResponseDeletesViewModel(data []byte, rootID string) (bool, error) {
	value, err := decodeResponseValue(data)
	if err != nil {
		return false, fmt.Errorf("discover deleted view models: %w", err)
	}
	found := false
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if stringValue(typed["$type"]) == InteractionTypeDeleteViewModel && stringValue(typed["TargetId"]) == rootID {
				found = true
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return found, nil
}

// ControlValue returns a control's scalar ValueProperties.Value.
func ControlValue(node ModelNode) (string, bool) {
	raw, ok := node.ValueProperties["Value"]
	if !ok {
		return "", false
	}
	return rawScalar(raw), true
}

func decodeResponseValue(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return value, nil
}

// TrimmedEqualFold compares server-normalized text case-insensitively.
func TrimmedEqualFold(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}
