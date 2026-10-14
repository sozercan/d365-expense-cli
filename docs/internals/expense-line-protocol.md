# Observed expense line and existing report protocol

Captured on October 13, 2026 by adding a Hotel line to a new Draft, itemizing
it into a room rate and hotel tax, and saving the report as a Draft. The
report was not submitted.

The CLI exposes this flow only during Draft creation:

```bash
d365-expense create \
  --draft \
  --session work \
  --purpose "Conference travel" \
  --expenses expenses.json
```

Submitting a report with lines was not captured, so `--expenses` requires
`--draft`.

## Add one line

All requests run against the open `ExpenseReportDetails_form` created by the
Draft flow.

1. Open the dialog with one message:
   - `UpdateLastSelectedControl("ToBePaidTotal")` on the details root;
   - `Click` on `NewExpenseButton` with null `PositionalParameters`.
2. The response creates `ExpenseAddNewExpense_form` with `CategoryInput`,
   `DateInput`, `AmountInput`, `CurrencyInput`, `MerchantInputNoLookup`,
   `NotesInput`, and `SaveButton`, plus the dialog's `TrvExpTrans_ds` record.
3. Send one `SetValue` per field, category first. Dates use `YYYY-MM-DD` and
   amounts use an invariant decimal string.
4. Click `SaveButton`.

The browser also sends lookup `RequestPopup` and `RequestClose` commands while
the user types; they only drive the lookup UI and are not replayed.

Each `SetValue` response carries a `TrvExpTrans_ds` update for the dialog
record. The first update is complete, but later updates can contain only the
changed field, so the CLI merges updates before checking `CostType_field`,
`TransDate_field`, `AmountCurr_field`, `ExchangeCode_field`,
`MerchantId_field`, and `AdditionalInformation_field`.

Every save and itemization close reloads the whole details-form
`TrvExpTrans_ds` list: a `ClearCache` splice followed by inserts of every
row's `MasterTrackingId`, with row fields in separate updates. The create and
open responses carry the initial list as `Items`. `recId_internal` values in a
reloaded list follow row position rather than the record, so the CLI compares
lists by content (category and date). A save must add exactly one line with
the requested category and date.

## Itemize a line

The saved line becomes the active record and its card exposes
`CardBottomMenuFormMenuButtonControl`.

1. Click the menu button, with `UpdateLastSelectedControl("ToBePaidTotal")`.
   The response adds `ItemizeExpenseButton`, a `Display` menu item for
   `ExpenseItemizeExpense` on `TrvExpTrans_ds`. The CLI requires it to be
   visible and enabled.
2. Click `ItemizeExpenseButton`, with
   `UpdateLastSelectedControl("CardBottomMenuFormMenuButtonControl")` and empty
   `PositionalParameters`.
3. The response creates `ExpenseItemizeExpense_form` with one blank
   `ExpenseItemizationTransTmp_ds` row and `TotalAmount`, which must equal the
   line amount.

For each row:

1. After the first row, click `NewButtonItemizationGroup` with
   `UpdateLastSelectedControl("CloseButton")`. The response inserts a blank row.
2. Set the subcategory with two consecutive messages: `SetValue` on
   `ExpenseItemizationTransTmp_SubCategoryRecId_TrvSharedSubCategory_Name`,
   then `ResolveChanges` on the `ExpenseItemizationTransTmp_SubCategoryRecId`
   reference group. The response must resolve a subcategory record with a
   non-zero RecId.
3. Optionally set `ExpenseItemizationTransTmp_StartDate`; it defaults to the
   line date.
4. Set `ExpenseItemizationTransTmp_DailyRate` and
   `ExpenseItemizationTransTmp_Quantity` (days).

`ToBeItemizedAmount` must be zero before the CLI clicks `CloseButton`. The
close response must delete the dialog, and its reloaded list must add exactly
one line per day for each row, dated from the row's start date; for example,
two days of room rate and two days of tax produce four itemized lines.

## Safety

`ValidateExpenseLineCommands` admits only the shapes above against the
session-scoped IDs discovered for the current stage, and rejects submit,
workflow, posting, approval, and recall command names. Any verification
failure stops the workflow: lines already saved stay on the report, later lines
are not added, the report is not saved and closed by the CLI, and a named
session becomes `uncertain`. The report remains a Draft.

## Open an existing Draft

`session import` records the workspace's `ReportsTabPageGrid` and its
`ExpenseReportNumber` hyperlink column as optional targets. They come from the
page's initial `GetFormInteraction` response; the browser sends that request
before `Language` and session headers are set, so the importer links it to the
session by sequence continuity.

1. `ExecuteShortcuts("Refresh")` on the workspace root lists the reports.
2. Unless the report is already active, `ChangeSelectedIndex` on the grid with
   `[target, 0, active, false, true]` activates its row.
3. `ExecuteHyperlink` on `ExpenseReportNumber` opens the active row's
   `ExpenseReportDetails_form`, whose `TrvExpTrans_ds` `Items` list the lines in
   display order with `ActiveMasterTrackingId` for the active line.

To act on another line, `ChangeSelectedIndex` with mark mode 0 on the details
form's `MainGrid` makes it active; the card's Actions menu then targets it.
`report submit` clicks the validated `SubmitButton` from the opened form,
requires the workspace to be restored, and refreshes the list to confirm the
report's `ApprovalStatus_field` is no longer `1`.

## Attach a report receipt to a line

On the opened report, `ActivateTab` on `ReceiptsTabPage` returns the
`DocuValue_ds` receipt rows with `FileName_field`, the `ReceiptsTabPageGrid`,
and `MatchExpensesButton` (Attach to expense).

1. `ChangeSelectedIndex` with mark mode 0 on `ReceiptsTabPageGrid` selects the
   receipt unless it is already active.
2. `UpdateLastSelectedControl("ImagePreview_Receipts")` plus `RequestPopup` on
   `MatchExpensesButton` opens `ExpenseReceiptCardAddDialog_form`. The browser
   numbers popups through `NamedParameters.Arg1`; the CLI uses the message
   sequence. The popup lists the lines that can take the receipt
   (`TransDate_field`, `AmountCurr_field`, `MerchantId_field`).
3. `ChangeSelectedIndex` with mark mode 0 on the popup's `MainGrid` selects the
   line. The browser uses mode 5 when the click lands on the attach image.
4. `Click` on `AttachButton` attaches the receipt. The response adds a
   `DocuRef_MatchedToExpense_ds` record and reloads the popup's lines without
   the attached one.
5. `RequestClose` with `[null]` closes the popup, and `SaveAndClose` saves.

## Remove a report receipt

`RemoveButtonReceiptsTab` removes the active receipt when none is marked.

1. `ChangeSelectedIndex` with mark mode 0 on `ReceiptsTabPageGrid` selects the
   receipt unless it is already active.
2. `UpdateLastSelectedControl("ImagePreview_Receipts")` plus `Click` on
   `RemoveButtonReceiptsTab` opens the `SysBoxForm_form` Yes/No confirmation.
3. `Click` on `Yes` removes it. The response closes the confirmation, lowers
   `ReceiptCount` by one, and reloads `DocuValue_ds` with file names and the
   `DocuRef_ds` `getAttachmentStatus_dataMethod`, which is non-empty for
   receipts attached to an expense line.

The CLI removes one receipt at a time and checks the reloaded list after each.

## Unattached expenses and receipts

`session import` also records the workspace's `ReportsTabPage`,
`ReceiptsTabPage`, and `ExpenseTabPage` from the initial form load.

1. `ActivateTab` on the tab (throttle group `<root>_WorkspaceTabControl`)
   lists its rows as `TrvExpTrans_ds` or `DocuValue_ds` records linked by
   `previous_`/`next_MasterTrackingId`. The first activation in a session
   also sends `ExpenseTabPageGrid`/`DeleteButtonExpenseTab` or
   `ReceiptsTabPageGrid`/`DeleteButtonReceiptsTab`; the session remembers them
   because later activations omit them.
2. `ChangeSelectedIndex` with mark mode 0 activates and marks one row.
3. `UpdateLastSelectedControl("ExpTransDate")` or `("Filename")` plus `Click`
   on the tab's Delete button opens the `SysBoxForm_form` Yes/No confirmation.
4. `Click` on `Yes` deletes the marked row (or the active row when none is
   marked). The CLI then refreshes the workspace and requires exactly that row
   to be gone.

Each command ends with `ActivateTab` on `ReportsTabPage`, because an import
from a workspace left on another tab cannot find **New expense report**.

## Delete a Draft

Captured on October 13, 2026 from the workspace Reports grid, up to and
including the confirmation dialog, then verified by deleting the test Drafts
with the CLI.

`session import` records the workspace's `ReportsTabPageGrid` control as an
optional `reportsGrid` target. `report delete` sends one message per stage:

1. `ExecuteShortcuts("Refresh")` on the workspace root. The response reloads
   the first page of `TrvExpTable_ds` rows with fresh `MasterTrackingId`
   values, `ExpNumber_field`, `ApprovalStatus_field` (`1` is Draft),
   `_RESERVED_IsMarked`, the collection's `ActiveMasterTrackingId`, and
   `DeleteButtonReportsTab`.
2. `ChangeSelectedIndex` on the grid with `[target, 1, active, false, true]`
   (current row, mark mode, range pivot). `ResetThrottleTime` and
   `ThrottleFirst` are serialized as strings. The response must mark only the
   target row; when the target was already active, Dynamics omits the active
   row.
3. `UpdateLastSelectedControl("ReportsTabPageGrid")` plus `Click` on
   `DeleteButtonReportsTab`. The response opens `SysBoxForm_form` with `Yes` and
   `No` buttons.
4. `Click` on `Yes`. The response must close the confirmation dialog.
5. `Refresh` again; the report number must no longer be listed.

Dynamics deletes every marked row, which is why the CLI refuses to continue
when any other row is marked. Deleting a report keeps its lines as unattached
expenses, and creating a report attaches all unattached expenses to it. Deleting a report with lines can take close to a
minute, so the default timeout is three minutes.
