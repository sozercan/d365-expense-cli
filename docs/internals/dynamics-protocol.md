# Observed Dynamics 365 expense protocol

This client is derived from authenticated Dynamics 365 Finance and Operations
Expense workspace traffic. It uses the web client's internal, stateful message
protocol; it is **not** an official or stable public API.

The canonical executable is `d365-expense`, built from the
`github.com/sozercan/d365-expense-cli` module.

## Endpoint

The Expense UI sends JSON envelopes to:

```text
POST /Services/ReliableCommunicationManager.svc/ProcessMessages?cmp=<company>&lng=<language>&
```

Requests depend on authenticated Dynamics cookies plus allowlisted `ms-dyn-*`
session and CSRF headers from the same browser session.

## Envelope state

Each request contains:

- `ChannelId`;
- `CompanyId`;
- `Language`;
- `LastAcknowledgedSequenceNumber`, the highest server message sequence the
  client has processed;
- monotonically increasing `Messages[].SequenceNumber` values; and
- `Messages[].Interactions[]`, commands against dynamic form and control IDs.

Control IDs and sequence numbers are session-scoped. A HAR or imported session
is not a long-lived API key, and the same state must not be used concurrently.

## Workspace bootstrap import

The standalone workflow does not require a recorded Draft-creation sequence.
`session import NAME --har PATH` uses the latest coherent, successful contiguous
Dynamics `ProcessMessages` session in the HAR and requires enough
response-model state to identify:

- the current `ExpenseWorkspace_form` root;
- the `NewExpenseReportReportsTab` control under that root;
- the current origin and exact `ProcessMessages` endpoint;
- company, language, channel, and sequence state; and
- the required request headers and cookies.

The importer rejects redacted credentials, missing response bodies, failed or
unacknowledged trailing traffic, ambiguous workspace/control models, mixed
sessions, and stale workspace roots. It synthesizes only the known allowlisted
`Click` target for **New expense report**; it does not need a captured click.

A successful import stores a compact named session rather than the HAR's full
response history. Existing complete Draft-flow HARs remain valid input, and
`create` can consume a private workspace HAR directly with `--har`.

## Import sources and session management

A session name is positional. Import requires exactly one acquisition source:

```bash
d365-expense session import work --har expense-workspace.har
d365-expense session import work --cdp http://127.0.0.1:9222
```

The CDP form discovers and validates the authenticated Expense workspace in a
dedicated loopback browser, then persists only the compact session; it does not
need to write a raw HAR.

Management commands are:

```bash
d365-expense session list
d365-expense session show work
d365-expense session remove work
d365-expense session unlock work
```

`session import work --force ...` intentionally replaces an existing name.
Names resolve under:

```text
<user-config-dir>/d365-expense/sessions
```

`D365_EXPENSE_CONFIG_DIR` overrides the configuration root. The legacy
`MSEXPENSE_CONFIG_DIR` name remains a temporary lower-precedence alias.

## Profile sources for expense commands

`create --draft` requires exactly one of:

```text
--session <name>
--har <private-raw-har>
```

Named-session mode acquires exclusive use of the session and persists advanced
replay state after an operation. Direct HAR mode never modifies its input and
must be treated as one-shot after a network operation begins.

Canonical mutating commands execute by default. `--dry-run` performs local
validation and prints the plan without sending requests or changing named
session state. The required `--draft` flag is a safety assertion; there is no
`--submit` mode.

## Session status lifecycle

Imported sessions start as `ready`.

Before a named-session operation sends its first mutating request, the CLI
obtains the session lock and persists `in_progress`. This write-ahead status
prevents a crash from leaving old state falsely reusable.

After execution:

- full remote success plus a successful local checkpoint returns the session
  to `ready` with current sequence and credential state;
- an operation error or untrusted checkpoint produces `uncertain` when that
  status can be persisted;
- interruption or checkpoint-write failure can leave `in_progress`; and
- authentication known to be revoked or unusable is represented as `expired`.

Only `ready` sessions are accepted for execution. `in_progress`, `uncertain`,
and `expired` are deliberately non-reusable because Dynamics may already have
accepted one or more requests. Re-import current authentication rather than
retrying from possibly stale sequence state.

If a process crash leaves the lock directory behind, verify no command is still
running, use `session unlock NAME`, and then re-import. Unlocking does not
change the non-ready session status.

## Draft creation flow

The canonical operation is:

```bash
d365-expense create \
  --draft \
  --session work \
  --purpose "Conference travel"
```

Starting from the imported workspace target, the CLI performs the allowlisted
flow directly over HTTP:

1. Open **New expense report** with two commands in one client message:
   - `UpdateLastSelectedControl("NewExpenseReportReportsTab")`;
   - `Click` on the imported workspace button.
2. Parse the response's `CreateViewModelInteraction` for
   `ExpenseNewExpenseReport_form` and discover the `NamePurpose` input.
3. Send:
   - `SetValue(<purpose>)` to `NamePurpose`;
   - `ExecuteShortcuts("InvokeDefaultButton")` to the dialog root.
4. Parse the response for `ExpenseReportDetails_form`, the generated report
   number, Draft status, and the `SaveAndClose` control.
5. Click only `SaveAndClose` to return to the workspace.

The details form may expose a `SubmitButton`. This project deliberately has no
submit operation, and outbound-command validation rejects submit, approve,
post, workflow-transition, and recall commands.

## Multiple receipts during creation

Repeat `--receipt` to attach files in argument order:

```bash
d365-expense create \
  --draft \
  --session work \
  --purpose "Conference travel" \
  --receipt outbound.png \
  --receipt return.png \
  --receipt-note "Ground transport receipts"
```

The CLI keeps the newly created details form open instead of immediately
clicking `SaveAndClose`. For each receipt it:

1. discovers `ReceiptsTabPage` and the current `ReceiptCount`;
2. activates the dynamic Receipts tab;
3. discovers `NewReceiptButton`;
4. performs the bounded preflight, upload, finalization, Draft-status, and
   receipt-count checks in [receipt-protocol.md](receipt-protocol.md); and
5. proceeds to the next receipt only after the cumulative count is confirmed.

Only after every receipt succeeds does the CLI click the new report's dynamic
`SaveAndClose` control. It validates all local files before the first network
request and never automatically retries, compensates, or submits a partially
populated Draft.

The preferred path uses the built-in, validated, non-secret upload contract:

- endpoint path `/filemanagement`;
- document type `File`;
- exact multipart field ordering; and
- maximum single-file size of 1,024,000 bytes.

The legacy `--receipt-protocol-har` option remains temporarily available, but
only its report-independent fixed contract is retained. No captured report
number, credential, sequence, or control ID is reused.

## Attach to an existing Draft

The canonical report-specific command is:

```bash
d365-expense receipt attach \
  --draft \
  --har receipt-attach.har \
  --report D00000000000001 \
  --receipt taxi.png
```

This remains a receipt-capable HAR workflow because it operates on an already
open captured report and needs that report's dynamic state. It is bounded to a
Draft and has no submission path.

## HAR acquisition and inspection

Inspect a legacy complete-flow HAR:

```bash
d365-expense har inspect expense-create.har
```

Capture a workspace bootstrap HAR:

```bash
./scripts/open-edge-cdp.sh

DYNAMICS_EXPENSE_URL='https://your-tenant.operations.dynamics.com/?mi=ExpenseWorkspace&cmp=USMF'
d365-expense har capture \
  --cdp http://127.0.0.1:9222 \
  --out captures/expense-workspace.har \
  "$DYNAMICS_EXPENSE_URL"
```

`har capture` allocates a temporary tab in the authenticated browser profile,
installs CDP recording before navigation, and records workspace initialization
for a bounded period. No user-created expense flow or disposable Draft is
required. Response-body capture is mandatory because the importer needs the
workspace and New Report response models.

After recording, the command removes every entry except same-origin `POST`
requests to the exact Dynamics `ProcessMessages` path. It validates an
executable workspace bootstrap before atomically writing the raw HAR with mode
`0600`. Invalid captures are not promoted to the output path. Existing files
require `--force`; symlink and non-regular output paths are rejected.

A browser is an optional acquisition mechanism only. Once a HAR is imported,
or when direct HAR mode is used, expense execution does not require a browser.

## Compatibility command mapping

Temporary compatibility aliases map the old interface to the canonical tree:

- `msexpense create-draft` → `d365-expense create --draft`
- `msexpense create-draft-with-receipt` →
  `d365-expense create --draft --receipt FILE`
- `msexpense create-draft-with-receipts` →
  `d365-expense create --draft --receipt FILE...`
- `msexpense attach-receipt` → `d365-expense receipt attach --draft`
- `msexpense capture-draft` → `d365-expense har capture`
- `msexpense inspect --har FILE` → `d365-expense har inspect FILE`
- `session inspect --name NAME` → `session show NAME`

Legacy `--file`, `--notes`, `--execute`, `--name`, and `MSEXPENSE_*` inputs are
temporary aliases. New automation should use the canonical binary, command
structure, flags, and `D365_EXPENSE_*` environment variables.

## Capture requirements

A usable raw HAR must retain request headers, cookies, request bodies, and
response bodies for successful Dynamics traffic. A sanitized HAR is useful for
inspection but is not executable because authentication values and response
models are removed.
