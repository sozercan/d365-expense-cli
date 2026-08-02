# Observed receipt attachment protocol

Captured on July 31, 2026 against a Draft expense report. The synthetic PNG was
attached at the **report level**; no expense line was selected and the report
was not submitted.

The canonical CLI surfaces this flow through repeatable receipt flags during
creation:

```bash
d365-expense create \
  --draft \
  --session work \
  --purpose "Conference travel" \
  --receipt outbound.png \
  --receipt return.png
```

It also supports report-specific attachment through:

```bash
d365-expense receipt attach \
  --draft \
  --har receipt-attach.har \
  --report D00000000000001 \
  --receipt taxi.png
```

Neither path has a submission operation.

## Preconditions

For an existing-report attachment, the captured session must end with:

- the target expense report open;
- the report status equal to `Draft`;
- the Receipts tab active; and
- the Add-new-receipt dialog closed.

A receipt-capable capture is therefore session- and report-specific. Concurrent
browser use or navigation can make its sequence numbers and control IDs stale.

For new-report creation, the CLI discovers the same controls from live
responses after creating the Draft; it does not reuse report-specific control
IDs or upload tokens from a HAR.

## RCM setup

Before an upload, the client opens **Add receipts**, verifies the target report,
then clicks the captured or dynamically discovered `CloseButtonAddNewTabPage`.
That close response must contain fresh, exact `Draft` status evidence. The
client then reopens Add receipts and uses only metadata from the new response,
minimizing the window between the Draft check and upload.

Each **Add receipts** open sends one message containing:

1. `UpdateLastSelectedControl("ImagePreview_Receipts")`
2. `Click` on the dynamically discovered `NewReceiptButton`

The fresh response creates `ExpenseAddNewReceipt_form` and exposes:

- `UploadControl` (`DocumentUpload`)
- `OkButtonAddNewTabPage`
- `CloseButtonAddNewTabPage`
- `UploadControl.ValueProperties.AccessToken`
- `CurrentRecId` and `CurrentDocuRefRecId`
- `SelectedDocumentType`
- `UploadControl.SerializedValueProperties.CurrentTableId`

The upload token is short-lived and must be obtained from the fresh response;
it must never be replayed from a HAR profile or printed.

## File validation commands

Selecting a file sends two sequenced messages:

1. `PropertyChangeInteraction` setting `DocuName`
2. `CheckFile(docuName, filename)`

Clicking Upload sends a second `CheckFile` before the binary HTTP request.

## Binary upload

Each receipt is posted separately to the same-origin endpoint:

```text
POST /filemanagement
Content-Type: multipart/form-data; boundary=<fresh boundary>
```

Observed multipart fields, in order:

1. `clientId` — fresh nine-character uppercase alphanumeric identifier
2. `maxChunkSize` — `1024000`
3. `tableid` — from `CurrentTableId`
4. `recid` — from `CurrentRecId`
5. `companyid` — captured company
6. `accesstoken` — fresh token from `UploadControl`
7. `notes`
8. `docuname`
9. `docutypeid`
10. `ischunked` — `false`
11. `docuRefRecId`
12. `files[]` — receipt bytes and detected MIME type

Each upload is intentionally restricted to one non-empty private PNG no larger
than the observed `maxChunkSize`, avoiding the uncaptured chunked-upload
protocol. Multi-receipt creation repeats the complete bounded upload flow
sequentially for each `--receipt`, up to the CLI limit of 20 receipts.

A successful response is a JSON array containing one `fileId`.

## Finalization

After each upload:

1. `PropertyChangeInteraction` sets `UploadedFileId`.
2. `CloseDialog` is sent against `UploadControl`.
3. `Click` is sent to `OkButtonAddNewTabPage`.
4. The client verifies the cumulative receipt count increased and the report
   remains `Draft`.
5. For multi-receipt creation, the next receipt starts only after that
   verification succeeds.
6. Only after all requested receipts succeed may the dynamically discovered
   `SaveAndClose` control be clicked.

All local files are validated before the first network request. On a partial
failure the CLI stops, does not attach later files, does not invoke
`SaveAndClose`, and does not retry or compensate automatically.

Receipt attachment itself has no Submit operation. Receipt validation rejects
submit, workflow, posting, approval, and recall commands at every RCM stage.
During the default `create` flow, the receipt workflow must finish and verify
every attachment before control returns to the separate, exact submission
validator. `--draft` selects `SaveAndClose` instead.

## Compatibility

The old `msexpense create-draft-with-receipt`,
`create-draft-with-receipts`, and `attach-receipt` forms remain temporary
aliases. Legacy `--file` and `--notes` map to canonical `--receipt` and
`--receipt-note`. Compatibility does not broaden the receipt operation's
Draft-only safety boundary or imply submission intent.
