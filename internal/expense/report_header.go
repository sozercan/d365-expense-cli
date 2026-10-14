package expense

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

const (
	maxInterimApprovers = 10
	maxPersonNameRunes  = 100
	// reportHeaderHeadroom covers opening the editor, the title, the final
	// approver, up to two approver dialogs, and selecting, deleting, adding,
	// and resolving every allowed approver row.
	reportHeaderHeadroom = int64(16 + 12*maxInterimApprovers)
	// interimApproverSeparator joins the names Dynamics lists in the header.
	interimApproverSeparator = ", "
)

// userAlias matches an interim approver given by user alias rather than by
// display name.
var userAlias = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ReportHeaderInput changes a Draft's header. An empty Title or FinalApprover
// and a nil InterimApprovers leave that field unchanged. InterimApprovers is
// the complete, ordered list; each entry is a display name, as Dynamics shows
// it, or a user alias, which tells apart workers who share a name.
// FinalApprover is a display name.
type ReportHeaderInput struct {
	Title            string
	InterimApprovers []string
	FinalApprover    string
}

// ReportHeader is a report's header as Dynamics shows it.
type ReportHeader struct {
	Title            string
	InterimApprovers []string
	FinalApprover    string
}

// approverInput is the header change for a new report's requested approvers.
func approverInput(interim []string, final string) ReportHeaderInput {
	return ReportHeaderInput{InterimApprovers: interim, FinalApprover: final}
}

// applyApprovers changes a newly created Draft's approvers when any were
// requested and returns the resulting header, or nil. The caller must hold
// client.mu.
func (client *Client) applyApprovers(ctx context.Context, draft openDraftDetails, input ReportHeaderInput) (*ReportHeader, error) {
	if input.empty() {
		return nil, nil
	}
	header, err := client.editOpenDraftHeader(ctx, draft, input)
	if err != nil {
		return nil, err
	}
	return &header, nil
}

func headerSequenceHeadroom(input ReportHeaderInput) int64 {
	if input.empty() {
		return 0
	}
	return reportHeaderHeadroom
}

// headerPlanActions describes a header change for a dry run.
func headerPlanActions(input ReportHeaderInput) []string {
	if input.empty() {
		return nil
	}
	actions := []string{"open Edit expense report from the report's Actions menu"}
	if input.Title != "" {
		actions = append(actions, fmt.Sprintf("set the title to %q", input.Title))
	}
	if input.FinalApprover != "" {
		actions = append(actions, fmt.Sprintf("set the final approver to %q and verify Dynamics resolved that name", input.FinalApprover))
	}
	if input.InterimApprovers != nil {
		actions = append(actions, fmt.Sprintf("make the interim approvers exactly %q, in order: keep matching leading rows, delete the rest, add and resolve the missing names, click OK",
			strings.Join(input.InterimApprovers, interimApproverSeparator)))
	}
	return append(actions, "verify the header and click OK; never click a Set as default button")
}

// headerRequestCount is the number of requests a header change needs when no
// existing interim approver row has to be deleted or reselected.
func headerRequestCount(input ReportHeaderInput) int {
	if input.empty() {
		return 0
	}
	count := 3 // Actions menu, Edit expense report, OK
	if input.Title != "" {
		count++
	}
	if input.FinalApprover != "" {
		count++
	}
	if input.InterimApprovers != nil {
		// Open the dialog, New and resolve per name, OK in both dialog copies.
		count += 1 + 2*len(input.InterimApprovers) + 2
	}
	return count
}

func (input ReportHeaderInput) empty() bool {
	return input.Title == "" && input.InterimApprovers == nil && input.FinalApprover == ""
}

func validateReportHeaderInput(input ReportHeaderInput) error {
	if input.Title != "" {
		if err := validatePurpose(input.Title); err != nil {
			return err
		}
	}
	if input.FinalApprover != "" {
		if err := validatePersonName(input.FinalApprover); err != nil {
			return fmt.Errorf("expense: final approver: %w", err)
		}
	}
	if input.InterimApprovers == nil {
		return nil
	}
	if len(input.InterimApprovers) == 0 || len(input.InterimApprovers) > maxInterimApprovers {
		return fmt.Errorf("expense: between 1 and %d interim approvers are required", maxInterimApprovers)
	}
	for index, name := range input.InterimApprovers {
		if err := validatePersonName(name); err != nil {
			return fmt.Errorf("expense: interim approver %d: %w", index+1, err)
		}
		for _, earlier := range input.InterimApprovers[:index] {
			if strings.EqualFold(earlier, name) {
				return fmt.Errorf("expense: interim approver %q is listed twice", name)
			}
		}
	}
	return nil
}

// validatePersonName accepts a display name Dynamics can resolve. Commas are
// rejected because Dynamics joins interim approver names with them.
func validatePersonName(name string) error {
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name || name == "" ||
		utf8.RuneCountInString(name) > maxPersonNameRunes || strings.Contains(name, ",") {
		return errors.New("name must be a non-empty display name without commas or surrounding spaces")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("name must not contain control characters")
		}
	}
	return nil
}

// PlanReportHeader validates a header change offline and describes it.
func PlanReportHeader(input ReportHeaderInput) ([]string, error) {
	if input.empty() {
		return nil, errors.New("expense: nothing to change in the report header")
	}
	if err := validateReportHeaderInput(input); err != nil {
		return nil, err
	}
	return headerPlanActions(input), nil
}

// EditDraftReportHeader opens an existing Draft, changes its header, and
// saves and closes it.
func (client *Client) EditDraftReportHeader(ctx context.Context, reportNumber string, input ReportHeaderInput) (header ReportHeader, err error) {
	if err := client.validateExistingReport(ctx, reportNumber); err != nil {
		return ReportHeader{}, err
	}
	if input.empty() {
		return ReportHeader{}, errors.New("expense: nothing to change in the report header")
	}
	if err := validateReportHeaderInput(input); err != nil {
		return ReportHeader{}, err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-existingReportHeadroom-reportHeaderHeadroom {
		return ReportHeader{}, errors.New("expense: client sequence lacks headroom for editing a report header")
	}
	draft, _, err := client.openDraftReport(ctx, reportNumber)
	if err != nil {
		return ReportHeader{}, err
	}
	defer func() {
		if err != nil {
			err = markOperationUncertain(err)
		}
	}()
	if header, err = client.editOpenDraftHeader(ctx, draft, input); err != nil {
		return ReportHeader{}, err
	}
	if err := client.saveAndCloseOpenDraft(ctx, draft); err != nil {
		return ReportHeader{}, err
	}
	return header, nil
}

// editOpenDraftHeader changes the open Draft's header through its Edit expense
// report dialog and closes the dialog with OK, leaving the details form open.
// The caller must hold client.mu.
func (client *Client) editOpenDraftHeader(ctx context.Context, draft openDraftDetails, input ReportHeaderInput) (ReportHeader, error) {
	root := draft.detailsRootID
	send := func(messages []dynamics.Message, targets dynamics.ReportHeaderCommandTargets) ([]byte, error) {
		return client.sendValidated(ctx, messages, func(envelope dynamics.Envelope) error {
			return dynamics.ValidateReportHeaderCommands(envelope, targets)
		})
	}
	model, err := dynamics.DiscoverResponseModel(draft.responseBody)
	if err != nil {
		return ReportHeader{}, err
	}
	more, ok := model.FindControlInRoot(dynamics.ControlMoreActions, root)
	if !ok || more.ID == "" {
		return ReportHeader{}, errors.New("expense: the report's Actions menu was not found")
	}
	body, err := send([]dynamics.Message{dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, root, more.ID)},
		dynamics.ReportHeaderCommandTargets{DetailsRootID: root, MoreActionsID: more.ID})
	if err != nil {
		return ReportHeader{}, fmt.Errorf("expense: open the Actions menu: %w", err)
	}
	if model, err = dynamics.DiscoverResponseModel(body); err != nil {
		return ReportHeader{}, err
	}
	editItem, ok := model.FindControlInRoot(dynamics.ControlEditExpenseReport, root)
	if !ok || editItem.ID == "" {
		return ReportHeader{}, errors.New("expense: the Actions menu has no Edit expense report item")
	}
	body, err = send([]dynamics.Message{dynamics.BuildMenuItemClickMessage(client.nextClientSequence, root, editItem.ID)},
		dynamics.ReportHeaderCommandTargets{DetailsRootID: root, EditMenuItemID: editItem.ID})
	if err != nil {
		return ReportHeader{}, fmt.Errorf("expense: open Edit expense report: %w", err)
	}
	if model, err = dynamics.DiscoverResponseModel(body); err != nil {
		return ReportHeader{}, err
	}
	form, ok := model.FindUniqueForm(dynamics.FormEditExpenseReport)
	if !ok || form.ID == "" {
		return ReportHeader{}, errors.New("expense: Edit expense report did not open its dialog")
	}
	editRoot := form.ID
	controls := make(map[string]string)
	for _, name := range []string{dynamics.ControlNamePurpose, dynamics.ControlFinalApprover, dynamics.ControlFinalApproverName,
		dynamics.ControlSelectInterimApprovers, dynamics.ControlEditReportOK} {
		node, ok := model.FindControlInRoot(name, editRoot)
		if !ok || node.ID == "" {
			return ReportHeader{}, fmt.Errorf("expense: Edit expense report control %s not found", name)
		}
		controls[name] = node.ID
	}
	editTargets := dynamics.ReportHeaderCommandTargets{
		EditRootID:          editRoot,
		PurposeID:           controls[dynamics.ControlNamePurpose],
		FinalApproverID:     controls[dynamics.ControlFinalApprover],
		FinalApproverNameID: controls[dynamics.ControlFinalApproverName],
		SelectInterimID:     controls[dynamics.ControlSelectInterimApprovers],
		EditOKID:            controls[dynamics.ControlEditReportOK],
	}

	editor := headerState{rootID: editRoot}
	if err := editor.observe(body, true); err != nil {
		return ReportHeader{}, err
	}
	if input.Title != "" && editor.header().Title != input.Title {
		body, err := send([]dynamics.Message{dynamics.BuildSetValueMessage(client.nextClientSequence, editRoot, editTargets.PurposeID, input.Title, "", "")}, editTargets)
		if err != nil {
			return ReportHeader{}, fmt.Errorf("expense: set the title: %w", err)
		}
		if err := editor.observe(body, false); err != nil {
			return ReportHeader{}, err
		}
		if got := editor.header().Title; got != input.Title {
			return ReportHeader{}, fmt.Errorf("expense: Dynamics reports title %q after setting %q", got, input.Title)
		}
	}
	if input.FinalApprover != "" && !dynamics.TrimmedEqualFold(editor.header().FinalApprover, input.FinalApprover) {
		body, err := send(dynamics.BuildReferenceNameMessages(client.nextClientSequence, editRoot,
			editTargets.FinalApproverID, editTargets.FinalApproverNameID, input.FinalApprover), editTargets)
		if err != nil {
			return ReportHeader{}, fmt.Errorf("expense: set the final approver: %w", err)
		}
		if err := editor.observe(body, false); err != nil {
			return ReportHeader{}, err
		}
		if workerLookupOpened(body) {
			return ReportHeader{}, fmt.Errorf("expense: more than one worker is named %q; choose the final approver in Dynamics", input.FinalApprover)
		}
		if got := editor.header().FinalApprover; !dynamics.TrimmedEqualFold(got, input.FinalApprover) {
			return ReportHeader{}, fmt.Errorf("expense: Dynamics did not resolve final approver %q (it shows %q)", input.FinalApprover, got)
		}
	}
	if input.InterimApprovers != nil && !sameNames(editor.header().InterimApprovers, input.InterimApprovers) {
		resolved, err := client.setInterimApprovers(ctx, editTargets, input.InterimApprovers, &editor)
		if err != nil {
			return ReportHeader{}, err
		}
		if got := editor.header().InterimApprovers; !sameNames(got, resolved) {
			return ReportHeader{}, fmt.Errorf("expense: Dynamics lists interim approvers %q, not %q",
				strings.Join(got, interimApproverSeparator), strings.Join(resolved, interimApproverSeparator))
		}
	}

	header := editor.header()
	body, err = send([]dynamics.Message{dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, editRoot, editTargets.EditOKID)}, editTargets)
	if err != nil {
		return ReportHeader{}, fmt.Errorf("expense: click OK in Edit expense report: %w", err)
	}
	if closed, err := dynamics.ResponseDeletesViewModel(body, editRoot); err != nil || !closed {
		return ReportHeader{}, errors.New("expense: Edit expense report did not close")
	}
	details := headerState{rootID: root}
	if err := details.observe(body, false); err != nil {
		return ReportHeader{}, err
	}
	if listed, ok := details.fields["interimApprovers_dataMethod"]; ok && !sameNames(splitApprovers(listed), header.InterimApprovers) {
		return ReportHeader{}, fmt.Errorf("expense: the report lists interim approvers %q after OK", listed)
	}
	return header, nil
}

// setInterimApprovers makes the interim approvers dialog list exactly names,
// in order, and returns their display names. It keeps the rows that already
// match the start of names, deletes the rest from the end, and adds the
// missing names in order. Dynamics opens the dialog twice; both copies are
// closed with OK, newest first, as in the browser. The caller must hold
// client.mu.
func (client *Client) setInterimApprovers(ctx context.Context, editTargets dynamics.ReportHeaderCommandTargets, names []string, editor *headerState) ([]string, error) {
	send := func(messages []dynamics.Message, targets dynamics.ReportHeaderCommandTargets) ([]byte, error) {
		return client.sendValidated(ctx, messages, func(envelope dynamics.Envelope) error {
			return dynamics.ValidateReportHeaderCommands(envelope, targets)
		})
	}
	body, err := send([]dynamics.Message{dynamics.BuildMenuItemClickMessage(client.nextClientSequence, editTargets.EditRootID, editTargets.SelectInterimID)},
		dynamics.ReportHeaderCommandTargets{EditRootID: editTargets.EditRootID, SelectInterimID: editTargets.SelectInterimID})
	if err != nil {
		return nil, fmt.Errorf("expense: open Select interim approvers: %w", err)
	}
	model, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		return nil, err
	}
	dialogs := model.FindForms(dynamics.FormInterimApprovers)
	if len(dialogs) == 0 || len(dialogs) > 2 {
		return nil, fmt.Errorf("expense: Select interim approvers opened %d dialogs", len(dialogs))
	}
	okIDs := make([]string, len(dialogs))
	for index, dialog := range dialogs {
		node, ok := model.FindControlInRoot(dynamics.ControlApproversOK, dialog.ID)
		if !ok || node.ID == "" {
			return nil, errors.New("expense: an interim approvers dialog has no OK button")
		}
		okIDs[index] = node.ID
	}
	dialog := dialogs[len(dialogs)-1].ID
	controls := make(map[string]string)
	for _, name := range []string{dynamics.ControlApproverGrid, dynamics.ControlApproverNewButton, dynamics.ControlApproverDeleteButton,
		dynamics.ControlApproverWorker, dynamics.ControlApproverWorkerName, dynamics.ControlApproverAlias} {
		node, ok := model.FindControlInRoot(name, dialog)
		if !ok || node.ID == "" {
			return nil, fmt.Errorf("expense: interim approvers control %s not found", name)
		}
		controls[name] = node.ID
	}
	targets := dynamics.ReportHeaderCommandTargets{
		ApproversRootID: dialog,
		ApproverGridID:  controls[dynamics.ControlApproverGrid],
		ApproverNewID:   controls[dynamics.ControlApproverNewButton],
		ApproverDelete:  controls[dynamics.ControlApproverDeleteButton],
		WorkerID:        controls[dynamics.ControlApproverWorker],
		WorkerNameID:    controls[dynamics.ControlApproverWorkerName],
		AliasID:         controls[dynamics.ControlApproverAlias],
	}

	rows := approverRows{rootID: dialog, alias: make(map[string]string), name: make(map[string]string)}
	if full, err := rows.observe(body); err != nil {
		return nil, err
	} else if !full {
		return nil, errors.New("expense: the interim approvers dialog did not list its rows")
	}
	if len(rows.order) > maxInterimApprovers {
		return nil, fmt.Errorf("expense: the report has %d interim approvers; at most %d are supported", len(rows.order), maxInterimApprovers)
	}
	active, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceInterimApprovers, dialog)
	if err != nil {
		return nil, err
	}
	// With no approvers, the dialog opens with one blank, unsaved row that
	// takes the first name.
	placeholder := ""
	if len(rows.order) == 1 && rows.alias[rows.order[0]] == "" && rows.name[rows.order[0]] == "" {
		placeholder = rows.order[0]
		rows.order = nil
	}
	if _, err := rows.names(); err != nil {
		return nil, err
	}
	keep := 0
	for keep < len(rows.order) && keep < len(names) && rows.matches(rows.order[keep], names[keep]) {
		keep++
	}
	selectRow := func(tracking string) error {
		if active == tracking {
			return nil
		}
		message, err := dynamics.BuildChangeSelectedIndexMessage(client.nextClientSequence, dialog, targets.ApproverGridID, tracking, dynamics.MarkModeActivate, active)
		if err != nil {
			return fmt.Errorf("expense: %w", err)
		}
		body, err := send([]dynamics.Message{message}, targets)
		if err != nil {
			return fmt.Errorf("expense: select an interim approver: %w", err)
		}
		if now, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceInterimApprovers, dialog); err != nil || now != tracking {
			return errors.New("expense: the interim approver row did not become active")
		}
		active = tracking
		_, err = rows.observe(body)
		return err
	}

	for len(rows.order) > keep {
		before := rows.aliases()
		last := rows.order[len(rows.order)-1]
		if err := selectRow(last); err != nil {
			return nil, err
		}
		body, err := send([]dynamics.Message{dynamics.BuildDeleteApproverMessage(client.nextClientSequence, dialog, targets.ApproverDelete)}, targets)
		if err != nil {
			return nil, fmt.Errorf("expense: delete an interim approver: %w", err)
		}
		if body, err = confirmDeletion(body, func(confirm dynamics.ReportHeaderCommandTargets) ([]byte, error) {
			return send([]dynamics.Message{dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, confirm.ConfirmRootID, confirm.ConfirmYesID)}, confirm)
		}); err != nil {
			return nil, err
		}
		full, err := rows.observe(body)
		if err != nil {
			return nil, err
		}
		if !full && len(before) == 1 {
			// Deleting the only row empties the list without reloading it.
			if full, err = dynamics.ResponseRemovesOnlyRow(body, dynamics.DataSourceInterimApprovers, dialog, last); err != nil {
				return nil, err
			} else if full {
				rows.order = nil
			}
		}
		if !full || !slices.Equal(rows.aliases(), before[:len(before)-1]) {
			return nil, errors.New("expense: Dynamics did not delete exactly the last interim approver")
		}
		if active, err = dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceInterimApprovers, dialog); err != nil {
			return nil, err
		}
	}
	for _, name := range names[keep:] {
		added := placeholder
		if added != "" {
			placeholder = ""
			if err := selectRow(added); err != nil {
				return nil, err
			}
			rows.order = []string{added}
		} else {
			if added, err = client.addApproverRow(send, targets, &rows, selectRow); err != nil {
				return nil, err
			}
			active = added
		}
		messages := dynamics.BuildReferenceNameMessages(client.nextClientSequence, dialog, targets.WorkerID, targets.WorkerNameID, name)
		if userAlias.MatchString(name) {
			messages = []dynamics.Message{dynamics.BuildSetValueMessage(client.nextClientSequence, dialog, targets.AliasID, name, "", "")}
		}
		body, err := send(messages, targets)
		if err != nil {
			return nil, fmt.Errorf("expense: set interim approver %q: %w", name, err)
		}
		if workerLookupOpened(body) {
			return nil, fmt.Errorf("expense: more than one worker is named %q; give their user alias instead", name)
		}
		if _, err := rows.observe(body); err != nil {
			return nil, err
		}
		if rows.alias[added] == "" || rows.name[added] == "" || !rows.matches(added, name) {
			return nil, fmt.Errorf("expense: Dynamics did not resolve interim approver %q", name)
		}
	}
	resolved, err := rows.names()
	if err != nil {
		return nil, err
	}
	if len(rows.order) != len(names) {
		return nil, fmt.Errorf("expense: the interim approvers dialog lists %q", strings.Join(resolved, interimApproverSeparator))
	}
	for index, tracking := range rows.order {
		if !rows.matches(tracking, names[index]) {
			return nil, fmt.Errorf("expense: the interim approvers dialog lists %q", strings.Join(resolved, interimApproverSeparator))
		}
	}

	for index := len(dialogs) - 1; index >= 0; index-- {
		id := dialogs[index].ID
		body, err := send([]dynamics.Message{dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, id, okIDs[index])},
			dynamics.ReportHeaderCommandTargets{ApproversRootID: id, ApproversOKID: okIDs[index]})
		if err != nil {
			return nil, fmt.Errorf("expense: click OK in interim approvers: %w", err)
		}
		if closed, err := dynamics.ResponseDeletesViewModel(body, id); err != nil || !closed {
			return nil, errors.New("expense: an interim approvers dialog did not close")
		}
		if err := editor.observe(body, false); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

// workerLookupOpened reports whether a response opened the worker lookup,
// which Dynamics does when a name matches more than one worker.
func workerLookupOpened(body []byte) bool {
	model, err := dynamics.DiscoverResponseModel(body)
	return err == nil && len(model.FindForms(dynamics.FormWorkerLookup)) != 0
}

// addApproverRow clicks New after making the last row active, since New
// inserts after the active row, and returns the added row's tracking ID.
func (client *Client) addApproverRow(
	send func([]dynamics.Message, dynamics.ReportHeaderCommandTargets) ([]byte, error),
	targets dynamics.ReportHeaderCommandTargets,
	rows *approverRows,
	selectRow func(string) error,
) (string, error) {
	if len(rows.order) != 0 {
		if err := selectRow(rows.order[len(rows.order)-1]); err != nil {
			return "", err
		}
	}
	body, err := send([]dynamics.Message{dynamics.BuildNewApproverMessage(client.nextClientSequence, targets.ApproversRootID, targets.ApproverNewID)}, targets)
	if err != nil {
		return "", fmt.Errorf("expense: add an interim approver row: %w", err)
	}
	added, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceInterimApprovers, targets.ApproversRootID)
	if err != nil {
		return "", err
	}
	if added == "" || slices.Contains(rows.order, added) {
		return "", errors.New("expense: New did not add an active interim approver row")
	}
	before := len(rows.order)
	if _, err := rows.observe(body); err != nil {
		return "", err
	}
	if !slices.Contains(rows.order, added) {
		rows.order = append(rows.order, added)
	}
	if len(rows.order) != before+1 || rows.order[len(rows.order)-1] != added {
		return "", errors.New("expense: New did not add exactly one row at the end")
	}
	return added, nil
}

// confirmDeletion answers Yes when a Delete response asks for confirmation,
// as it does for a saved approver row, and returns the response that carries
// the deletion.
func confirmDeletion(body []byte, sendYes func(dynamics.ReportHeaderCommandTargets) ([]byte, error)) ([]byte, error) {
	model, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		return nil, err
	}
	confirm, ok := model.FindUniqueForm(dynamics.FormConfirmation)
	if !ok || confirm.ID == "" {
		return body, nil
	}
	yes, yesOK := model.FindControlInRoot(dynamics.ControlConfirmYes, confirm.ID)
	no, noOK := model.FindControlInRoot(dynamics.ControlConfirmNo, confirm.ID)
	if !yesOK || !noOK || yes.ID == "" || no.ID == "" {
		return nil, errors.New("expense: Delete opened a confirmation without Yes and No")
	}
	body, err = sendYes(dynamics.ReportHeaderCommandTargets{ConfirmRootID: confirm.ID, ConfirmYesID: yes.ID})
	if err != nil {
		return nil, fmt.Errorf("expense: confirm deleting an interim approver: %w", err)
	}
	if closed, err := dynamics.ResponseDeletesViewModel(body, confirm.ID); err != nil || !closed {
		return nil, errors.New("expense: the Delete confirmation did not close")
	}
	return body, nil
}

// headerState tracks the report record and final approver name shown under
// one form root.
type headerState struct {
	rootID   string
	tracking string
	fields   map[string]string
	final    string
}

// observe merges the root's report record. The form shows one report, and
// Dynamics re-reads it under a new tracking ID after the interim approvers
// dialog closes, so the record body lists is followed. With requireRecord,
// body must list it.
func (state *headerState) observe(body []byte, requireRecord bool) error {
	records, err := recordsByTracking(body, dynamics.DataSourceReports, state.rootID)
	if err != nil {
		return err
	}
	if len(records) > 1 {
		return errors.New("expense: the report header form lists more than one report")
	}
	if state.fields == nil {
		state.fields = make(map[string]string)
	}
	for tracking, fields := range records {
		state.tracking = tracking
		for name, value := range fields {
			state.fields[name] = value
		}
	}
	if requireRecord && state.tracking == "" {
		return errors.New("expense: the report header form did not list the report")
	}
	names, err := dynamics.DiscoverPersonNames(body, state.rootID)
	if err != nil {
		return err
	}
	if name, ok := names[state.tracking]; ok && state.tracking != "" {
		state.final = name
	}
	return nil
}

func (state *headerState) header() ReportHeader {
	return ReportHeader{
		Title:            state.fields["Txt2_field"],
		InterimApprovers: splitApprovers(state.fields["interimApprovers_dataMethod"]),
		FinalApprover:    state.final,
	}
}

// approverRows tracks the interim approvers grid from the latest full list.
type approverRows struct {
	rootID string
	order  []string
	alias  map[string]string
	name   map[string]string
}

// observe merges row fields from body and replaces the order when body lists
// every row. It reports whether body listed every row.
func (rows *approverRows) observe(body []byte) (bool, error) {
	ids, full, err := dynamics.DiscoverFullList(body, dynamics.DataSourceInterimApprovers, rows.rootID)
	if err != nil {
		return false, err
	}
	if full {
		rows.order = ids
	}
	records, err := recordsByTracking(body, dynamics.DataSourceInterimApprovers, rows.rootID)
	if err != nil {
		return false, err
	}
	for tracking, fields := range records {
		if alias, ok := fields["UserAlias_field"]; ok {
			rows.alias[tracking] = alias
		}
	}
	names, err := dynamics.DiscoverPersonNames(body, rows.rootID)
	if err != nil {
		return false, err
	}
	for tracking, name := range names {
		rows.name[tracking] = name
	}
	return full, nil
}

// matches reports whether a row is the approver given by want, a user alias
// or a display name.
func (rows *approverRows) matches(tracking, want string) bool {
	if userAlias.MatchString(want) {
		return strings.EqualFold(rows.alias[tracking], want)
	}
	return dynamics.TrimmedEqualFold(rows.name[tracking], want)
}

func (rows *approverRows) names() ([]string, error) {
	names := make([]string, len(rows.order))
	for index, tracking := range rows.order {
		if rows.name[tracking] == "" {
			return nil, errors.New("expense: Dynamics did not name every interim approver")
		}
		names[index] = rows.name[tracking]
	}
	return names, nil
}

func (rows *approverRows) aliases() []string {
	aliases := make([]string, len(rows.order))
	for index, tracking := range rows.order {
		aliases[index] = rows.alias[tracking]
	}
	return aliases
}

func splitApprovers(listed string) []string {
	if strings.TrimSpace(listed) == "" {
		return nil
	}
	names := strings.Split(listed, interimApproverSeparator)
	for index, name := range names {
		names[index] = strings.TrimSpace(name)
	}
	return names
}

func sameNames(a, b []string) bool {
	return slices.EqualFunc(a, b, dynamics.TrimmedEqualFold)
}
