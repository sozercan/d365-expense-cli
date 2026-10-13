package expense

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

const (
	// MaxExpenseLines bounds the expense lines added to one new report.
	MaxExpenseLines       = 50
	maxItemizationRows    = 20
	maxItemizationDays    = 366
	expenseDateLayout     = "2006-01-02"
	maxExpenseAmountCents = int64(1e14)
)

var (
	expenseAmountPattern   = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,2})?$`)
	expenseCurrencyPattern = regexp.MustCompile(`^[A-Za-z]{3}$`)
)

// ExpenseLineInput describes one expense line added to a new Draft report.
// Empty Currency, Merchant, and Notes keep the Dynamics form defaults.
type ExpenseLineInput struct {
	Category    string
	Date        string // YYYY-MM-DD
	Amount      string // positive decimal with at most two fractional digits
	Currency    string
	Merchant    string
	Notes       string
	Itemization []ItemizationInput
}

// ItemizationInput is one itemization row: Days daily charges of DailyRate
// starting on StartDate. An empty StartDate keeps the expense line's date.
type ItemizationInput struct {
	Subcategory string
	StartDate   string
	DailyRate   string
	Days        int
}

// ExpenseLineResult contains only non-secret outcome data for one line.
type ExpenseLineResult struct {
	Category      string
	Date          string
	Amount        string
	ItemizedLines int
}

func validateExpenseLines(lines []ExpenseLineInput, finalAction ReportFinalAction) error {
	if len(lines) == 0 {
		return nil
	}
	if finalAction != ReportFinalActionSaveDraft {
		return errors.New("expense: expense lines are supported only when saving a Draft")
	}
	if len(lines) > MaxExpenseLines {
		return fmt.Errorf("expense: at most %d expense lines are supported", MaxExpenseLines)
	}
	for index, line := range lines {
		if err := validateExpenseLine(line); err != nil {
			return fmt.Errorf("expense: expense line %d: %w", index+1, err)
		}
	}
	return nil
}

func validateExpenseLine(line ExpenseLineInput) error {
	if err := requireText("category", line.Category, true); err != nil {
		return err
	}
	if err := validateExpenseDate("date", line.Date); err != nil {
		return err
	}
	amount, err := parseExpenseCents("amount", line.Amount)
	if err != nil {
		return err
	}
	if line.Currency != "" && !expenseCurrencyPattern.MatchString(line.Currency) {
		return errors.New("currency must be a three-letter code")
	}
	if err := requireText("merchant", line.Merchant, false); err != nil {
		return err
	}
	if err := requireText("notes", line.Notes, false); err != nil {
		return err
	}
	if len(line.Itemization) > maxItemizationRows {
		return fmt.Errorf("at most %d itemization rows are supported", maxItemizationRows)
	}
	var itemized int64
	for index, row := range line.Itemization {
		if err := requireText("subcategory", row.Subcategory, true); err != nil {
			return fmt.Errorf("itemization row %d: %w", index+1, err)
		}
		if row.StartDate != "" {
			if err := validateExpenseDate("start date", row.StartDate); err != nil {
				return fmt.Errorf("itemization row %d: %w", index+1, err)
			}
		}
		rate, err := parseExpenseCents("daily rate", row.DailyRate)
		if err != nil {
			return fmt.Errorf("itemization row %d: %w", index+1, err)
		}
		if row.Days < 1 || row.Days > maxItemizationDays {
			return fmt.Errorf("itemization row %d: days must be between 1 and %d", index+1, maxItemizationDays)
		}
		itemized += rate * int64(row.Days)
	}
	if len(line.Itemization) != 0 && itemized != amount {
		return fmt.Errorf("itemization totals %s but the amount is %s", formatCents(itemized), formatCents(amount))
	}
	return nil
}

func requireText(name, value string, required bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is empty", name)
	}
	return nil
}

func validateExpenseDate(name, value string) error {
	if _, err := time.Parse(expenseDateLayout, value); err != nil {
		return fmt.Errorf("%s must use YYYY-MM-DD", name)
	}
	return nil
}

func parseExpenseCents(name, value string) (int64, error) {
	if !expenseAmountPattern.MatchString(value) {
		return 0, fmt.Errorf("%s must be a positive decimal with at most two fractional digits", name)
	}
	whole, fraction, _ := strings.Cut(value, ".")
	fraction = (fraction + "00")[:2]
	cents, err := strconv.ParseInt(whole+fraction, 10, 64)
	if err != nil || cents <= 0 || cents >= maxExpenseAmountCents {
		return 0, fmt.Errorf("%s must be positive and below %s", name, formatCents(maxExpenseAmountCents))
	}
	return cents, nil
}

func formatCents(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// decimalEqual compares a requested decimal with a server-serialized decimal
// such as "300.0000000000000000".
func decimalEqual(want, got string) bool {
	left, ok := new(big.Rat).SetString(want)
	if !ok {
		return false
	}
	right, ok := new(big.Rat).SetString(strings.TrimSpace(got))
	return ok && left.Cmp(right) == 0
}

func expenseLineRequestCount(line ExpenseLineInput) int {
	count := 2 + len(expenseLineFields(line))
	if rows := len(line.Itemization); rows != 0 {
		count += 3 + (rows - 1)
		for _, row := range line.Itemization {
			count += 3
			if row.StartDate != "" {
				count++
			}
		}
	}
	return count
}

func expenseLinesRequestCount(lines []ExpenseLineInput) int {
	count := 0
	for _, line := range lines {
		count += expenseLineRequestCount(line)
	}
	return count
}

// expenseLinesSequenceHeadroom bounds the client messages sent for lines; a
// subcategory request carries two messages.
func expenseLinesSequenceHeadroom(lines []ExpenseLineInput) int64 {
	var headroom int64
	for _, line := range lines {
		headroom += int64(expenseLineRequestCount(line) + len(line.Itemization))
	}
	return headroom
}

func expenseLinePlanActions(lines []ExpenseLineInput) []string {
	if len(lines) == 0 {
		return nil
	}
	actions := []string{"add every expense line in request order: open New expense, set and verify each field, then Save"}
	for _, line := range lines {
		if len(line.Itemization) != 0 {
			actions = append(actions, "itemize requested lines from the line's Actions menu, verify nothing remains to itemize, then Close")
			break
		}
	}
	return actions
}

type expenseField struct {
	control string
	value   string
	field   string
	check   func(map[string]string) bool
}

func expenseLineFields(line ExpenseLineInput) []expenseField {
	fields := []expenseField{
		{dynamics.ControlCategoryInput, line.Category, "CostType_field", func(record map[string]string) bool {
			return dynamics.TrimmedEqualFold(record["CostType_field"], line.Category)
		}},
		{dynamics.ControlDateInput, line.Date, "TransDate_field", func(record map[string]string) bool {
			return record["TransDate_field"] == line.Date
		}},
		{dynamics.ControlAmountInput, line.Amount, "AmountCurr_field", func(record map[string]string) bool {
			return decimalEqual(line.Amount, record["AmountCurr_field"])
		}},
	}
	if line.Currency != "" {
		fields = append(fields, expenseField{dynamics.ControlCurrencyInput, line.Currency, "ExchangeCode_field", func(record map[string]string) bool {
			return dynamics.TrimmedEqualFold(record["ExchangeCode_field"], line.Currency)
		}})
	}
	if line.Merchant != "" {
		fields = append(fields, expenseField{dynamics.ControlMerchantInput, line.Merchant, "MerchantId_field", func(record map[string]string) bool {
			return strings.TrimSpace(record["MerchantId_field"]) == strings.TrimSpace(line.Merchant)
		}})
	}
	if line.Notes != "" {
		fields = append(fields, expenseField{dynamics.ControlNotesInput, line.Notes, "AdditionalInformation_field", func(record map[string]string) bool {
			return normalizeNotes(record["AdditionalInformation_field"]) == normalizeNotes(line.Notes)
		}})
	}
	return fields
}

func normalizeNotes(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
}

// expenseLineSession tracks details-form controls and line records across the
// responses of one report-creation workflow.
type expenseLineSession struct {
	client             *Client
	detailsRootID      string
	newExpenseButtonID string
	cardMenu           dynamics.ModelNode
	itemizeButton      dynamics.ModelNode
	knownLines         map[string]bool
}

// addExpenseLines adds every requested line to the open Draft. The caller must
// hold client.mu and reserve sequence headroom.
func (client *Client) addExpenseLines(ctx context.Context, draft openDraftDetails, lines []ExpenseLineInput) ([]ExpenseLineResult, error) {
	model, err := dynamics.DiscoverResponseModel(draft.responseBody)
	if err != nil {
		return nil, err
	}
	newExpense, ok := model.FindControlInRoot(dynamics.ControlNewExpenseButton, draft.detailsRootID)
	if !ok || newExpense.ID == "" {
		return nil, errors.New("expense: NewExpenseButton not found in new report details form")
	}
	session := &expenseLineSession{
		client:             client,
		detailsRootID:      draft.detailsRootID,
		newExpenseButtonID: newExpense.ID,
		knownLines:         make(map[string]bool),
	}
	if _, err := session.newLines(draft.responseBody); err != nil {
		return nil, err
	}
	results := make([]ExpenseLineResult, 0, len(lines))
	for index, line := range lines {
		result, err := session.addLine(ctx, line)
		if err != nil {
			return nil, fmt.Errorf("expense: expense line %d (%s %s): %w", index+1, line.Category, line.Date, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func (session *expenseLineSession) send(ctx context.Context, messages []dynamics.Message, targets dynamics.ExpenseLineCommandTargets) ([]byte, error) {
	body, err := session.client.sendValidated(ctx, messages, func(envelope dynamics.Envelope) error {
		return dynamics.ValidateExpenseLineCommands(envelope, targets)
	})
	if err != nil {
		return nil, err
	}
	model, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		return nil, err
	}
	if node, ok := model.FindControlInRoot(dynamics.ControlNewExpenseButton, session.detailsRootID); ok && node.ID != "" {
		session.newExpenseButtonID = node.ID
	}
	if node, ok := model.FindControlInRoot(dynamics.ControlExpenseCardMenu, session.detailsRootID); ok {
		session.cardMenu = dynamics.MergeModelNode(session.cardMenu, node)
	}
	if node, ok := model.FindControlInRoot(dynamics.ControlItemizeExpenseButton, session.detailsRootID); ok {
		session.itemizeButton = dynamics.MergeModelNode(session.itemizeButton, node)
	}
	return body, nil
}

// newLines returns details-form expense lines first seen in body, keyed by
// RecId with their latest fields, and marks them known.
func (session *expenseLineSession) newLines(body []byte) (map[string]map[string]string, error) {
	records, err := dynamics.DiscoverDataRecords(body, dynamics.DataSourceExpenseLines)
	if err != nil {
		return nil, err
	}
	lines := make(map[string]map[string]string)
	for _, record := range records {
		if record.RootID != session.detailsRootID || record.RecID == "" || record.RecID == "0" || session.knownLines[record.RecID] {
			continue
		}
		lines[record.RecID] = record.Fields
	}
	for recID := range lines {
		session.knownLines[recID] = true
	}
	return lines, nil
}

func (session *expenseLineSession) addLine(ctx context.Context, line ExpenseLineInput) (ExpenseLineResult, error) {
	client := session.client
	open := dynamics.BuildOpenNewExpenseMessage(client.nextClientSequence, session.detailsRootID, session.newExpenseButtonID)
	body, err := session.send(ctx, []dynamics.Message{open}, dynamics.ExpenseLineCommandTargets{
		DetailsRootID:      session.detailsRootID,
		NewExpenseButtonID: session.newExpenseButtonID,
	})
	if err != nil {
		return ExpenseLineResult{}, fmt.Errorf("open New expense: %w", err)
	}
	model, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		return ExpenseLineResult{}, err
	}
	dialog, ok := model.FindUniqueForm(dynamics.FormExpenseAddNewExpense)
	if !ok || dialog.ID == "" {
		return ExpenseLineResult{}, errors.New("New expense dialog not found")
	}
	fields := expenseLineFields(line)
	fieldIDs := make([]string, len(fields))
	for index, field := range fields {
		control, ok := model.FindControlInRoot(field.control, dialog.ID)
		if !ok || control.ID == "" {
			return ExpenseLineResult{}, fmt.Errorf("%s not found in New expense dialog", field.control)
		}
		fieldIDs[index] = control.ID
	}
	save, ok := model.FindControlInRoot(dynamics.ControlSaveButton, dialog.ID)
	if !ok || save.ID == "" {
		return ExpenseLineResult{}, errors.New("SaveButton not found in New expense dialog")
	}
	// Dynamics sends the new record once and then only changed fields, so
	// verification runs against the merged record state.
	record := make(map[string]string)
	if err := mergeRecords(record, body, dynamics.DataSourceExpenseLines, dialog.ID, ""); err != nil {
		return ExpenseLineResult{}, err
	}

	for index, field := range fields {
		set := dynamics.BuildSetValueMessage(client.nextClientSequence, dialog.ID, fieldIDs[index], field.value, "", "")
		body, err := session.send(ctx, []dynamics.Message{set}, dynamics.ExpenseLineCommandTargets{
			DialogRootID:   dialog.ID,
			DialogFieldIDs: fieldIDs,
		})
		if err != nil {
			return ExpenseLineResult{}, fmt.Errorf("set %s: %w", field.control, err)
		}
		if err := mergeRecords(record, body, dynamics.DataSourceExpenseLines, dialog.ID, ""); err != nil {
			return ExpenseLineResult{}, err
		}
		for _, previous := range fields[:index+1] {
			if !previous.check(record) {
				return ExpenseLineResult{}, fmt.Errorf("set %s: Dynamics reports %s as %q, not the requested %q",
					field.control, previous.control, record[previous.field], previous.value)
			}
		}
	}

	saveClick := dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, dialog.ID, save.ID)
	body, err = session.send(ctx, []dynamics.Message{saveClick}, dynamics.ExpenseLineCommandTargets{
		DialogRootID: dialog.ID,
		SaveButtonID: save.ID,
	})
	if err != nil {
		return ExpenseLineResult{}, fmt.Errorf("save expense: %w", err)
	}
	if closed, err := dynamics.ResponseDeletesViewModel(body, dialog.ID); err != nil || !closed {
		return ExpenseLineResult{}, errors.New("save expense: New expense dialog did not close")
	}
	saved, err := session.newLines(body)
	if err != nil {
		return ExpenseLineResult{}, err
	}
	if len(saved) != 1 {
		return ExpenseLineResult{}, fmt.Errorf("save expense: expected one new expense line, found %d", len(saved))
	}
	for _, record := range saved {
		if record["TransDate_field"] != line.Date || !dynamics.TrimmedEqualFold(record["categoryOrSubCategoryName_dataMethod"], line.Category) {
			return ExpenseLineResult{}, errors.New("save expense: saved line does not match the requested category and date")
		}
	}

	result := ExpenseLineResult{Category: line.Category, Date: line.Date, Amount: line.Amount}
	if len(line.Itemization) != 0 {
		result.ItemizedLines, err = session.itemize(ctx, line)
		if err != nil {
			return ExpenseLineResult{}, fmt.Errorf("itemize: %w", err)
		}
	}
	return result, nil
}

func (session *expenseLineSession) itemize(ctx context.Context, line ExpenseLineInput) (int, error) {
	client := session.client
	if session.cardMenu.ID == "" || session.cardMenu.RootID != session.detailsRootID {
		return 0, errors.New("expense line Actions menu not found")
	}
	menu := dynamics.BuildOpenExpenseCardMenuMessage(client.nextClientSequence, session.detailsRootID, session.cardMenu.ID)
	if _, err := session.send(ctx, []dynamics.Message{menu}, dynamics.ExpenseLineCommandTargets{
		DetailsRootID: session.detailsRootID,
		CardMenuID:    session.cardMenu.ID,
	}); err != nil {
		return 0, fmt.Errorf("open Actions menu: %w", err)
	}
	if err := dynamics.ValidateItemizeButton(session.itemizeButton, session.detailsRootID); err != nil {
		return 0, err
	}
	open := dynamics.BuildOpenItemizeExpenseMessage(client.nextClientSequence, session.detailsRootID, session.itemizeButton.ID)
	body, err := session.send(ctx, []dynamics.Message{open}, dynamics.ExpenseLineCommandTargets{
		DetailsRootID:   session.detailsRootID,
		ItemizeButtonID: session.itemizeButton.ID,
	})
	if err != nil {
		return 0, fmt.Errorf("open Itemize: %w", err)
	}
	model, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		return 0, err
	}
	form, ok := model.FindUniqueForm(dynamics.FormExpenseItemizeExpense)
	if !ok || form.ID == "" {
		return 0, errors.New("Itemize expense dialog not found")
	}
	controls := make(map[string]string)
	for _, name := range []string{
		dynamics.ControlItemizationNewButton, dynamics.ControlItemizationSubcategory, dynamics.ControlItemizationSubcatName,
		dynamics.ControlItemizationStartDate, dynamics.ControlItemizationDailyRate, dynamics.ControlItemizationQuantity,
		dynamics.ControlItemizationCloseButton,
	} {
		control, ok := model.FindControlInRoot(name, form.ID)
		if !ok || control.ID == "" {
			return 0, fmt.Errorf("%s not found in Itemize expense dialog", name)
		}
		controls[name] = control.ID
	}
	total, ok := model.FindControlInRoot(dynamics.ControlItemizationTotal, form.ID)
	if value, present := dynamics.ControlValue(total); !ok || !present || !decimalEqual(line.Amount, value) {
		return 0, errors.New("Itemize expense dialog total does not match the saved line amount")
	}

	remaining := ""
	observeRemaining := func(body []byte) error {
		model, err := dynamics.DiscoverResponseModel(body)
		if err != nil {
			return err
		}
		if node, ok := model.FindControlInRoot(dynamics.ControlItemizationRemaining, form.ID); ok {
			if value, present := dynamics.ControlValue(node); present {
				remaining = value
			}
		}
		return nil
	}
	if err := observeRemaining(body); err != nil {
		return 0, err
	}
	current := make(map[string]string)
	trackRow := func(body []byte) error {
		records, err := dynamics.DiscoverDataRecords(body, dynamics.DataSourceItemizationRows)
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.RootID == form.ID && record.Inserted && record.RecID == "0" {
				clear(current)
				current["MasterTrackingId"] = record.Fields["MasterTrackingId"]
			}
		}
		if current["MasterTrackingId"] == "" {
			blank, ok, err := lastRecordInRoot(body, dynamics.DataSourceItemizationRows, form.ID)
			if err != nil || !ok || blank.RecID != "0" {
				return errors.New("Itemize expense dialog has no blank itemization row")
			}
			current["MasterTrackingId"] = blank.Fields["MasterTrackingId"]
		}
		return mergeRecords(current, body, dynamics.DataSourceItemizationRows, form.ID, current["MasterTrackingId"])
	}
	if err := trackRow(body); err != nil {
		return 0, err
	}
	fieldIDs := []string{
		controls[dynamics.ControlItemizationStartDate],
		controls[dynamics.ControlItemizationDailyRate],
		controls[dynamics.ControlItemizationQuantity],
	}
	setRowField := func(control, value string) ([]byte, error) {
		set := dynamics.BuildSetValueMessage(client.nextClientSequence, form.ID, controls[control], value, "", "")
		body, err := session.send(ctx, []dynamics.Message{set}, dynamics.ExpenseLineCommandTargets{
			ItemizeRootID:   form.ID,
			ItemizeFieldIDs: fieldIDs,
		})
		if err != nil {
			return nil, fmt.Errorf("set %s: %w", control, err)
		}
		if err := trackRow(body); err != nil {
			return nil, err
		}
		return body, observeRemaining(body)
	}

	expected := make(map[string]int)
	wantLines := 0
	for index, row := range line.Itemization {
		if index > 0 {
			newRow := dynamics.BuildNewItemizationRowMessage(client.nextClientSequence, form.ID, controls[dynamics.ControlItemizationNewButton])
			body, err := session.send(ctx, []dynamics.Message{newRow}, dynamics.ExpenseLineCommandTargets{
				ItemizeRootID:      form.ID,
				ItemizeNewButtonID: controls[dynamics.ControlItemizationNewButton],
			})
			if err != nil {
				return 0, fmt.Errorf("row %d: new row: %w", index+1, err)
			}
			if !hasInsertedBlankRow(body, form.ID) {
				return 0, fmt.Errorf("row %d: Dynamics did not add an itemization row", index+1)
			}
			if err := trackRow(body); err != nil {
				return 0, err
			}
			if err := observeRemaining(body); err != nil {
				return 0, err
			}
		}

		subcategory := dynamics.BuildSubcategoryMessages(client.nextClientSequence, form.ID,
			controls[dynamics.ControlItemizationSubcategory], controls[dynamics.ControlItemizationSubcatName], row.Subcategory)
		body, err := session.send(ctx, subcategory, dynamics.ExpenseLineCommandTargets{
			ItemizeRootID:     form.ID,
			SubcategoryID:     controls[dynamics.ControlItemizationSubcategory],
			SubcategoryNameID: controls[dynamics.ControlItemizationSubcatName],
		})
		if err != nil {
			return 0, fmt.Errorf("row %d: set subcategory: %w", index+1, err)
		}
		if !resolvedSubcategory(body, form.ID, row.Subcategory) {
			return 0, fmt.Errorf("row %d: Dynamics did not resolve subcategory %q", index+1, row.Subcategory)
		}
		if err := trackRow(body); err != nil {
			return 0, err
		}
		startDate := line.Date
		if row.StartDate != "" {
			startDate = row.StartDate
			if _, err := setRowField(dynamics.ControlItemizationStartDate, row.StartDate); err != nil {
				return 0, fmt.Errorf("row %d: %w", index+1, err)
			}
		}
		if _, err := setRowField(dynamics.ControlItemizationDailyRate, row.DailyRate); err != nil {
			return 0, fmt.Errorf("row %d: %w", index+1, err)
		}
		if _, err := setRowField(dynamics.ControlItemizationQuantity, strconv.Itoa(row.Days)); err != nil {
			return 0, fmt.Errorf("row %d: %w", index+1, err)
		}
		if current["StartDate_field"] != startDate || !decimalEqual(row.DailyRate, current["DailyRate_field"]) ||
			current["Quantity_field"] != strconv.Itoa(row.Days) {
			return 0, fmt.Errorf("row %d: Dynamics reports start date %q, daily rate %q, and days %q", index+1,
				current["StartDate_field"], current["DailyRate_field"], current["Quantity_field"])
		}
		expected[strings.ToLower(strings.TrimSpace(row.Subcategory))] += row.Days
		wantLines += row.Days
	}
	if !decimalEqual("0", remaining) {
		return 0, fmt.Errorf("Dynamics reports %q still to itemize", remaining)
	}

	closeClick := dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, form.ID, controls[dynamics.ControlItemizationCloseButton])
	body, err = session.send(ctx, []dynamics.Message{closeClick}, dynamics.ExpenseLineCommandTargets{
		ItemizeRootID:  form.ID,
		ItemizeCloseID: controls[dynamics.ControlItemizationCloseButton],
	})
	if err != nil {
		return 0, fmt.Errorf("close Itemize: %w", err)
	}
	if closed, err := dynamics.ResponseDeletesViewModel(body, form.ID); err != nil || !closed {
		return 0, errors.New("close Itemize: Itemize expense dialog did not close")
	}
	itemized, err := session.newLines(body)
	if err != nil {
		return 0, err
	}
	for _, record := range itemized {
		expected[strings.ToLower(strings.TrimSpace(record["categoryOrSubCategoryName_dataMethod"]))]--
	}
	for _, remainingDays := range expected {
		if remainingDays != 0 || len(itemized) != wantLines {
			return 0, fmt.Errorf("close Itemize: expected %d itemized lines matching the requested subcategories, found %d", wantLines, len(itemized))
		}
	}
	return len(itemized), nil
}

// mergeRecords applies every record update in rootID, optionally limited to
// one MasterTrackingId, to state in response order.
func mergeRecords(state map[string]string, body []byte, dataSource, rootID, trackingID string) error {
	records, err := dynamics.DiscoverDataRecords(body, dataSource)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.RootID != rootID || (trackingID != "" && record.Fields["MasterTrackingId"] != trackingID) {
			continue
		}
		for name, value := range record.Fields {
			state[name] = value
		}
	}
	return nil
}

func lastRecordInRoot(body []byte, dataSource, rootID string) (dynamics.DataRecord, bool, error) {
	records, err := dynamics.DiscoverDataRecords(body, dataSource)
	if err != nil {
		return dynamics.DataRecord{}, false, err
	}
	for index := len(records) - 1; index >= 0; index-- {
		if records[index].RootID == rootID {
			return records[index], true, nil
		}
	}
	return dynamics.DataRecord{}, false, nil
}

func hasInsertedBlankRow(body []byte, rootID string) bool {
	records, err := dynamics.DiscoverDataRecords(body, dynamics.DataSourceItemizationRows)
	if err != nil {
		return false
	}
	for _, record := range records {
		if record.RootID == rootID && record.Inserted && record.RecID == "0" {
			return true
		}
	}
	return false
}

func resolvedSubcategory(body []byte, rootID, name string) bool {
	records, err := dynamics.DiscoverDataRecords(body, dynamics.DataSourceItemizationSubcats)
	if err != nil {
		return false
	}
	for _, record := range records {
		if record.RootID == rootID && record.RecID != "" && record.RecID != "0" && dynamics.TrimmedEqualFold(record.Fields["Name_field"], name) {
			return true
		}
	}
	return false
}
