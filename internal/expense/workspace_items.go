package expense

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/sozercan/d365-expense-cli/internal/dynamics"
)

const maxWorkspaceDeletions = 50

// ErrWorkspaceTabsUnavailable means the session predates workspace tab support
// and must be re-imported.
var ErrWorkspaceTabsUnavailable = errors.New("expense: session does not identify the workspace tabs; re-import the session")

// UnattachedExpense is one expense on the workspace Expenses tab.
type UnattachedExpense struct {
	Number   int
	Date     string
	Category string
	Merchant string
	Amount   string
	Receipts bool
}

// workspaceTab describes one workspace tab whose rows can be deleted.
type workspaceTab struct {
	label string
	tabID func(*Client) string
	// ids returns the remembered grid and Delete IDs for the tab.
	ids        func(*Client) (*string, *string)
	dataSource string
	keyField   string
	grid       string
	delete     string
	selected   string
}

var (
	expensesTab = workspaceTab{
		label: "unattached expense",
		tabID: func(client *Client) string { return client.workspaceTabs.Expenses },
		ids: func(client *Client) (*string, *string) {
			return &client.workspaceTabs.ExpensesGrid, &client.workspaceTabs.ExpensesDelete
		},
		dataSource: dynamics.DataSourceExpenseLines,
		keyField:   "MerchantId_field",
		grid:       dynamics.ControlExpensesGrid,
		delete:     dynamics.ControlDeleteExpenseButton,
		selected:   dynamics.SelectedControlExpenseDate,
	}
	receiptsTab = workspaceTab{
		label: "unattached receipt",
		tabID: func(client *Client) string { return client.workspaceTabs.Receipts },
		ids: func(client *Client) (*string, *string) {
			return &client.workspaceTabs.ReceiptsGrid, &client.workspaceTabs.ReceiptsDelete
		},
		dataSource: dynamics.DataSourceReceipts,
		keyField:   "FileName_field",
		grid:       dynamics.ControlReceiptsGrid,
		delete:     dynamics.ControlDeleteReceiptButton,
		selected:   dynamics.SelectedControlReceiptName,
	}
)

// ListUnattachedExpenses lists the workspace Expenses tab and returns the
// workspace to its Reports tab.
func (client *Client) ListUnattachedExpenses(ctx context.Context) ([]UnattachedExpense, error) {
	var expenses []UnattachedExpense
	err := client.withWorkspaceTab(ctx, expensesTab, func(_ dynamics.WorkspaceItemCommandTargets, body []byte) error {
		ids, rows, err := linkedRows(body, dynamics.DataSourceExpenseLines, client.workspaceRootID)
		if err != nil {
			return err
		}
		for index, id := range ids {
			fields := rows[id]
			expenses = append(expenses, UnattachedExpense{
				Number:   index + 1,
				Date:     fields["TransDate_field"],
				Category: fields["CostType_field"],
				Merchant: fields["MerchantId_field"],
				Amount:   fields["amountWithCurrencyCode_dataMethod"],
				Receipts: fields["ReceiptsAttached_field"] != "" && fields["ReceiptsAttached_field"] != "0",
			})
		}
		return nil
	})
	return expenses, err
}

// DeleteUnattachedExpenses deletes every unattached expense whose merchant is
// exactly merchant. count must equal the number of such expenses.
func (client *Client) DeleteUnattachedExpenses(ctx context.Context, merchant string, count int) (int, error) {
	return client.deleteWorkspaceItems(ctx, expensesTab, merchant, count)
}

// DeleteUnattachedReceipts deletes every unattached receipt whose file name is
// exactly name. count must equal the number of such receipts.
func (client *Client) DeleteUnattachedReceipts(ctx context.Context, name string, count int) (int, error) {
	return client.deleteWorkspaceItems(ctx, receiptsTab, name, count)
}

// CheckWorkspaceTabs validates offline that the session can open workspace
// tabs.
func (client *Client) CheckWorkspaceTabs() error {
	if client == nil {
		return errors.New("expense: client is nil")
	}
	if client.workspaceTabs.Reports == "" || client.workspaceTabs.Receipts == "" || client.workspaceTabs.Expenses == "" {
		return ErrWorkspaceTabsUnavailable
	}
	return nil
}

// withWorkspaceTab activates tab, runs operation with its activation
// response, and always tries to return the workspace to its Reports tab.
func (client *Client) withWorkspaceTab(ctx context.Context, tab workspaceTab, operation func(dynamics.WorkspaceItemCommandTargets, []byte) error) (err error) {
	if ctx == nil {
		return errors.New("expense: context is nil")
	}
	if err := client.CheckWorkspaceTabs(); err != nil {
		return err
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.nextClientSequence > math.MaxInt64-int64(4*maxWorkspaceDeletions+4) {
		return errors.New("expense: client sequence lacks headroom for workspace changes")
	}
	root := client.workspaceRootID
	targets := dynamics.WorkspaceItemCommandTargets{
		WorkspaceRootID: root,
		TabIDs:          []string{client.workspaceTabs.Reports, client.workspaceTabs.Receipts, client.workspaceTabs.Expenses},
	}
	activate := func(tabID string) ([]byte, error) {
		message, err := dynamics.BuildActivateWorkspaceTabMessage(client.nextClientSequence, root, tabID)
		if err != nil {
			return nil, fmt.Errorf("expense: %w", err)
		}
		return client.sendValidated(ctx, []dynamics.Message{message}, func(envelope dynamics.Envelope) error {
			return dynamics.ValidateWorkspaceItemCommands(envelope, targets)
		})
	}
	body, err := activate(tab.tabID(client))
	if err != nil {
		return fmt.Errorf("expense: open the workspace %s tab: %w", tab.label, err)
	}
	defer func() {
		// Leave the workspace on Reports so later imports and report
		// commands find the controls they expect.
		if _, restoreErr := activate(client.workspaceTabs.Reports); restoreErr != nil && err == nil {
			err = markOperationUncertain(fmt.Errorf("expense: return to the Reports tab: %w", restoreErr))
		}
	}()
	model, err := dynamics.DiscoverResponseModel(body)
	if err != nil {
		return err
	}
	gridID, deleteID := tab.ids(client)
	if grid, ok := model.FindControlInRoot(tab.grid, root); ok && grid.ID != "" {
		*gridID = grid.ID
	}
	if deleteButton, ok := model.FindControlInRoot(tab.delete, root); ok && deleteButton.ID != "" {
		*deleteID = deleteButton.ID
	}
	if *gridID == "" || *deleteID == "" {
		return fmt.Errorf("expense: the %s tab's grid or Delete control is unknown; re-import the session", tab.label)
	}
	targets.GridID = *gridID
	targets.DeleteButtonID = *deleteID
	targets.SelectedControl = tab.selected
	return operation(targets, body)
}

func (client *Client) deleteWorkspaceItems(ctx context.Context, tab workspaceTab, key string, count int) (removed int, err error) {
	if strings.TrimSpace(key) == "" || count < 1 || count > maxWorkspaceDeletions {
		return 0, fmt.Errorf("expense: a name and a count from 1 to %d are required", maxWorkspaceDeletions)
	}
	err = client.withWorkspaceTab(ctx, tab, func(targets dynamics.WorkspaceItemCommandTargets, body []byte) error {
		root := targets.WorkspaceRootID
		ids, rows, err := linkedRows(body, tab.dataSource, root)
		if err != nil {
			return err
		}
		if matches := matchingRows(ids, rows, tab.keyField, key); len(matches) != count {
			return fmt.Errorf("expense: found %d %ss named %q, not %d", len(matches), tab.label, key, count)
		}
		active, err := dynamics.DiscoverActiveTrackingID(body, tab.dataSource, root)
		if err != nil {
			return err
		}
		send := func(message dynamics.Message, stage dynamics.WorkspaceItemCommandTargets) ([]byte, error) {
			return client.sendValidated(ctx, []dynamics.Message{message}, func(envelope dynamics.Envelope) error {
				return dynamics.ValidateWorkspaceItemCommands(envelope, stage)
			})
		}
		for removed < count {
			matches := matchingRows(ids, rows, tab.keyField, key)
			if len(matches) != count-removed {
				return fmt.Errorf("expense: expected %d %ss named %q, found %d", count-removed, tab.label, key, len(matches))
			}
			target := matches[0]
			if active != target {
				if active == "" {
					active = ids[0]
				}
				selectRow, err := dynamics.BuildChangeSelectedIndexMessage(client.nextClientSequence, root, targets.GridID, target, dynamics.MarkModeActivate, active)
				if err != nil {
					return fmt.Errorf("expense: %w", err)
				}
				body, err := send(selectRow, dynamics.WorkspaceItemCommandTargets{WorkspaceRootID: root, GridID: targets.GridID})
				if err != nil {
					return fmt.Errorf("expense: select %s: %w", tab.label, err)
				}
				if now, err := dynamics.DiscoverActiveTrackingID(body, tab.dataSource, root); err != nil || now != target {
					return fmt.Errorf("expense: the %s did not become the active row", tab.label)
				}
				marks, err := recordsByTracking(body, tab.dataSource, root)
				if err != nil {
					return err
				}
				for tracking, fields := range marks {
					if fields["_RESERVED_IsMarked"] == "true" && tracking != target {
						return fmt.Errorf("expense: another %s is marked; clear the selection in Dynamics and re-import", tab.label)
					}
				}
				active = target
			}

			body, err := send(dynamics.BuildDeleteWorkspaceItemMessage(client.nextClientSequence, root, targets.DeleteButtonID, tab.selected), dynamics.WorkspaceItemCommandTargets{
				WorkspaceRootID: root, DeleteButtonID: targets.DeleteButtonID, SelectedControl: tab.selected,
			})
			if err != nil {
				return markOperationUncertain(fmt.Errorf("expense: click Delete: %w", err))
			}
			model, err := dynamics.DiscoverResponseModel(body)
			if err != nil {
				return markOperationUncertain(err)
			}
			confirm, ok := model.FindUniqueForm(dynamics.FormConfirmation)
			yes, yesOK := model.FindControlInRoot(dynamics.ControlConfirmYes, confirm.ID)
			no, noOK := model.FindControlInRoot(dynamics.ControlConfirmNo, confirm.ID)
			if !ok || confirm.ID == "" || !yesOK || !noOK || yes.ID == "" || no.ID == "" {
				return markOperationUncertain(errors.New("expense: Delete did not open the captured Yes/No confirmation"))
			}
			body, err = send(dynamics.BuildSaveAndCloseClickMessage(client.nextClientSequence, confirm.ID, yes.ID), dynamics.WorkspaceItemCommandTargets{
				ConfirmRootID: confirm.ID, ConfirmYesID: yes.ID,
			})
			if err != nil {
				return markOperationUncertain(fmt.Errorf("expense: confirm Delete: %w", err))
			}
			if closed, err := dynamics.ResponseDeletesViewModel(body, confirm.ID); err != nil || !closed {
				return markOperationUncertain(errors.New("expense: Delete confirmation did not close"))
			}
			removed++

			// Verify against a fresh list rather than the delete response.
			refreshed, err := client.sendValidated(ctx, []dynamics.Message{dynamics.BuildRefreshMessage(client.nextClientSequence, root)}, func(envelope dynamics.Envelope) error {
				return dynamics.ValidateReportCommands(envelope, dynamics.ReportCommandTargets{WorkspaceRootID: root})
			})
			if err != nil {
				return markOperationUncertain(fmt.Errorf("expense: refresh after Delete: %w", err))
			}
			before := len(ids)
			if ids, rows, err = linkedRows(refreshed, tab.dataSource, root); err != nil {
				return markOperationUncertain(err)
			}
			if len(ids) != before-1 || len(matchingRows(ids, rows, tab.keyField, key)) != count-removed {
				return markOperationUncertain(fmt.Errorf("expense: Dynamics did not delete exactly one %s named %q", tab.label, key))
			}
			if active, err = dynamics.DiscoverActiveTrackingID(refreshed, tab.dataSource, root); err != nil {
				return markOperationUncertain(err)
			}
		}
		return nil
	})
	return removed, err
}

func matchingRows(ids []string, rows map[string]map[string]string, field, value string) []string {
	var matches []string
	for _, id := range ids {
		if rows[id][field] == value {
			matches = append(matches, id)
		}
	}
	return matches
}

// linkedRows orders a data source's rows in rootID by their
// previous/next MasterTrackingId links and fails on an incomplete chain.
func linkedRows(body []byte, dataSource, rootID string) ([]string, map[string]map[string]string, error) {
	rows, err := recordsByTracking(body, dataSource, rootID)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		// An empty tab sends no row records.
		return nil, rows, nil
	}
	head := ""
	for tracking, fields := range rows {
		if previous := fields["previous_MasterTrackingId"]; previous == "" || previous == "0" || rows[previous] == nil {
			if head != "" {
				return nil, nil, fmt.Errorf("expense: the %s rows do not form one list", dataSource)
			}
			head = tracking
		}
	}
	var ids []string
	for current := head; current != "" && current != "0" && rows[current] != nil; current = rows[current]["next_MasterTrackingId"] {
		if len(ids) > len(rows) {
			return nil, nil, fmt.Errorf("expense: the %s rows form a cycle", dataSource)
		}
		ids = append(ids, current)
	}
	if len(ids) != len(rows) {
		return nil, nil, fmt.Errorf("expense: the %s rows do not form one list", dataSource)
	}
	return ids, rows, nil
}
