package expense

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

const maxReceiptRemovals = 20

// RemoveReportReceiptsRequest removes every receipt with one file name from a
// Draft's Receipts tab. Count must equal the number of receipts with that name.
type RemoveReportReceiptsRequest struct {
	ReportNumber string
	Receipt      string
	Count        int
}

// receiptRow is one receipt on the Receipts tab.
type receiptRow struct {
	name     string
	attached bool
	marked   bool
}

// receiptList tracks the Receipts tab from the latest full list Dynamics sent.
type receiptList struct {
	root  string
	order []string
	rows  map[string]receiptRow
}

// observe merges receipt fields from body and replaces the order when body
// reloads the whole list.
func (list *receiptList) observe(body []byte) error {
	if ids, ok, err := dynamics.DiscoverFullList(body, dynamics.DataSourceReceipts, list.root); err != nil {
		return err
	} else if ok {
		list.order = ids
		rows := make(map[string]receiptRow, len(ids))
		for _, id := range ids {
			rows[id] = list.rows[id]
		}
		list.rows = rows
	}
	for _, dataSource := range []string{dynamics.DataSourceReceipts, dynamics.DataSourceReceiptRefs} {
		records, err := recordsByTracking(body, dataSource, list.root)
		if err != nil {
			return err
		}
		for tracking, fields := range records {
			row, ok := list.rows[tracking]
			if !ok {
				continue
			}
			if name, ok := fields["FileName_field"]; ok {
				row.name = name
			}
			if status, ok := fields["getAttachmentStatus_dataMethod"]; ok {
				row.attached = strings.TrimSpace(status) != ""
			}
			if marked, ok := fields["_RESERVED_IsMarked"]; ok {
				row.marked = marked == "true"
			}
			list.rows[tracking] = row
		}
	}
	return nil
}

// named returns the receipts with name in display order, failing when names
// are unknown or any receipt is marked.
func (list *receiptList) named(name string) ([]string, error) {
	var matches []string
	for _, id := range list.order {
		row := list.rows[id]
		if row.name == "" {
			return nil, errors.New("expense: Dynamics did not name every receipt")
		}
		if row.marked {
			return nil, errors.New("expense: receipts are marked on the Receipts tab; clear the selection in Dynamics and re-import")
		}
		if row.name == name {
			matches = append(matches, id)
		}
	}
	return matches, nil
}

// RemoveReportReceipts removes every receipt named request.Receipt, one at a
// time, then saves and closes the Draft. It refuses receipts attached to an
// expense line and stops unless exactly request.Count receipts match.
func (client *Client) RemoveReportReceipts(ctx context.Context, request RemoveReportReceiptsRequest) (removed int, err error) {
	if err := client.validateExistingReport(ctx, request.ReportNumber); err != nil {
		return 0, err
	}
	if strings.TrimSpace(request.Receipt) == "" || request.Count < 1 || request.Count > maxReceiptRemovals {
		return 0, fmt.Errorf("expense: a receipt name and a count from 1 to %d are required", maxReceiptRemovals)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-existingReportHeadroom-int64(4*request.Count+2) {
		return 0, errors.New("expense: client sequence lacks headroom for removing receipts")
	}
	draft, _, err := client.openDraftReport(ctx, request.ReportNumber)
	if err != nil {
		return 0, err
	}
	root := draft.detailsRootID
	openModel, err := dynamics.DiscoverResponseModel(draft.responseBody)
	if err != nil {
		return 0, err
	}
	tab, ok := openModel.FindControlInRoot(dynamics.ControlReceiptsTabPage, root)
	if !ok || tab.ID == "" {
		return 0, errors.New("expense: Receipts tab not found in the report")
	}
	count, ok := receiptCount(openModel, root)
	if !ok {
		return 0, errors.New("expense: the report's receipt count is missing")
	}
	activate, err := buildActivateReceiptsTabMessage(client.nextClientSequence, root, tab.ID)
	if err != nil {
		return 0, err
	}
	body, err := client.sendValidated(ctx, []dynamics.Message{activate}, func(envelope dynamics.Envelope) error {
		return validateActivateReceiptsTabEnvelope(envelope, root, tab.ID)
	})
	if err != nil {
		return 0, fmt.Errorf("expense: activate Receipts tab: %w", err)
	}
	tabModel, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		return 0, err
	}
	grid, gridOK := tabModel.FindControlInRoot(dynamics.ControlReceiptsGrid, root)
	remove, removeOK := openModel.FindControlInRoot(dynamics.ControlRemoveReceiptButton, root)
	if node, ok := tabModel.FindControlInRoot(dynamics.ControlRemoveReceiptButton, root); ok && node.ID != "" {
		remove, removeOK = node, true
	}
	if !gridOK || !removeOK || grid.ID == "" || remove.ID == "" {
		return 0, errors.New("expense: receipts grid or Remove control not found")
	}
	list := &receiptList{root: root, rows: make(map[string]receiptRow)}
	for _, response := range [][]byte{draft.responseBody, body} {
		if err := list.observe(response); err != nil {
			return 0, err
		}
	}
	if len(list.order) != count {
		return 0, fmt.Errorf("expense: the Receipts tab lists %d receipts, not the report's %d", len(list.order), count)
	}
	targets, err := list.named(request.Receipt)
	if err != nil {
		return 0, err
	}
	if len(targets) != request.Count {
		return 0, fmt.Errorf("expense: the report has %d receipts named %q, not %d", len(targets), request.Receipt, request.Count)
	}
	for _, id := range targets {
		if list.rows[id].attached {
			return 0, fmt.Errorf("expense: a receipt named %q is attached to an expense line", request.Receipt)
		}
	}
	active, err := activeTracking(dynamics.DataSourceReceipts, root, body, draft.responseBody)
	if err != nil {
		return 0, err
	}

	defer func() {
		if err != nil {
			err = markOperationUncertain(err)
		}
	}()
	send := func(message dynamics.Message, targets dynamics.ReceiptRemoveCommandTargets) ([]byte, error) {
		return client.sendValidated(ctx, []dynamics.Message{message}, func(envelope dynamics.Envelope) error {
			return dynamics.ValidateReceiptRemoveCommands(envelope, targets)
		})
	}
	for removed < request.Count {
		targets, err := list.named(request.Receipt)
		if err != nil {
			return removed, err
		}
		if len(targets) != request.Count-removed {
			return removed, fmt.Errorf("expense: expected %d receipts named %q, found %d", request.Count-removed, request.Receipt, len(targets))
		}
		target := targets[0]
		if active != target {
			selectReceipt, err := dynamics.BuildChangeSelectedIndexMessage(client.nextClientSequence, root, grid.ID, target, dynamics.MarkModeActivate, active)
			if err != nil {
				return removed, fmt.Errorf("expense: %w", err)
			}
			body, err := send(selectReceipt, dynamics.ReceiptRemoveCommandTargets{DetailsRootID: root, ReceiptsGridID: grid.ID})
			if err != nil {
				return removed, fmt.Errorf("expense: select receipt: %w", err)
			}
			if now, err := dynamics.DiscoverActiveTrackingID(body, dynamics.DataSourceReceipts, root); err != nil || now != target {
				return removed, fmt.Errorf("expense: receipt %q did not become the selected receipt", request.Receipt)
			}
			if err := list.observe(body); err != nil {
				return removed, err
			}
		}

		body, err := send(dynamics.BuildRemoveReceiptClickMessage(client.nextClientSequence, root, remove.ID), dynamics.ReceiptRemoveCommandTargets{
			DetailsRootID:  root,
			RemoveButtonID: remove.ID,
		})
		if err != nil {
			return removed, fmt.Errorf("expense: click Remove: %w", err)
		}
		model, err := dynamics.DiscoverResponseModel(body)
		if err != nil {
			return removed, err
		}
		confirm, ok := model.FindUniqueForm(dynamics.FormConfirmation)
		yes, yesOK := model.FindControlInRoot(dynamics.ControlConfirmYes, confirm.ID)
		no, noOK := model.FindControlInRoot(dynamics.ControlConfirmNo, confirm.ID)
		if !ok || confirm.ID == "" || !yesOK || !noOK || yes.ID == "" || no.ID == "" {
			return removed, errors.New("expense: Remove did not open the captured Yes/No confirmation")
		}
		body, err = send(dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, confirm.ID, yes.ID), dynamics.ReceiptRemoveCommandTargets{
			ConfirmRootID: confirm.ID,
			ConfirmYesID:  yes.ID,
		})
		if err != nil {
			return removed, fmt.Errorf("expense: confirm Remove: %w", err)
		}
		if closed, err := dynamics.ResponseDeletesViewModel(body, confirm.ID); err != nil || !closed {
			return removed, errors.New("expense: Remove confirmation did not close")
		}
		before := len(list.order)
		if err := list.observe(body); err != nil {
			return removed, err
		}
		model, err = dynamics.DiscoverResponseModel(body)
		if err != nil {
			return removed, err
		}
		if now, ok := receiptCount(model, root); !ok || now != count-1 || len(list.order) != before-1 {
			return removed, errors.New("expense: Dynamics did not remove exactly one receipt")
		}
		count--
		removed++
		if left, err := list.named(request.Receipt); err != nil || len(left) != request.Count-removed {
			return removed, fmt.Errorf("expense: the removed receipt was not named %q", request.Receipt)
		}
		if active, err = activeTracking(dynamics.DataSourceReceipts, root, body); err != nil && len(list.order) != 0 {
			return removed, err
		}
	}
	if err := client.saveAndCloseOpenDraft(ctx, draft); err != nil {
		return removed, err
	}
	return removed, nil
}

func receiptCount(model dynamics.ResponseModel, rootID string) (int, bool) {
	node, ok := model.FindControlInRoot(dynamics.ControlReceiptCount, rootID)
	if !ok {
		return 0, false
	}
	value, present := dynamics.ControlValue(node)
	count, err := strconv.Atoi(value)
	return count, present && err == nil && count >= 0
}
