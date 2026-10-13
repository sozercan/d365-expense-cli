# Observed expense line and Draft deletion protocol

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

The save response must delete the dialog view model and insert exactly one new
details-form `TrvExpTrans_ds` record with a non-zero `recId_internal` and the
requested date and category.

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
close response must delete the dialog and insert one details-form
`TrvExpTrans_ds` line per day for each row; for example, two days of room rate
and two days of tax produce four itemized lines.

## Safety

`ValidateExpenseLineCommands` admits only the shapes above against the
session-scoped IDs discovered for the current stage, and rejects submit,
workflow, posting, approval, and recall command names. Any verification
failure stops the workflow: lines already saved stay on the report, later lines
are not added, the report is not saved and closed by the CLI, and a named
session becomes `uncertain`. The report remains a Draft.

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
when any other row is marked. Deleting a report with lines can take close to a
minute, so the default timeout is three minutes.
