# d365-expense

Create and submit Dynamics 365 Finance expense reports from the command line,
with optional receipts and an explicit `--draft` opt-out.

> [!IMPORTANT]
> **Submission is the default.** `create` submits the newly created report after
> every receipt succeeds. Add `--draft` only when you want to save without
> submitting.
>
> Always preview with `--dry-run`. The CLI still cannot approve, post, recall,
> or run arbitrary workflow actions, and it cannot submit an already-closed
> Draft by report number.

## What it does

- Creates a new expense report in **Draft** status.
- Attaches up to 20 PNG receipts in the order provided.
- Selects either **Save and close** or the exact discovered **Submit** action
  only after every receipt succeeds.
- Reuses a securely stored authenticated Dynamics session.
- Stops instead of retrying when the remote result may be uncertain.

## Before you begin

You need:

1. Access to the Dynamics 365 Finance **Expense management** workspace.
2. Permission to create and submit expense reports.
3. A current authenticated session, acquired from either:
   - a private browser HAR; or
   - a dedicated local Microsoft Edge session through CDP.
4. Receipts saved as private PNG files no larger than 1,024,000 bytes each.

HAR files and imported sessions contain credentials. Never share, upload, or
commit them.

### A few terms

- **Session** — the local authenticated state used by the CLI.
- **HAR** — a private browser network capture that can be imported or used once
  directly.
- **CDP** — a local connection to an authenticated Microsoft Edge browser used
  to acquire session state without manually exporting a HAR.

## Install

Install Go 1.24 or newer, then run:

```bash
go install github.com/sozercan/d365-expense-cli/cmd/d365-expense@latest
```

Verify the installation:

```bash
d365-expense version
```

If you are working from a source checkout, see
[CONTRIBUTING.md](CONTRIBUTING.md) for build and test instructions.

## How it works

1. Acquire current Dynamics authentication from a HAR or local Edge session.
2. Store it under a short session name such as `work`.
3. Preview the expense with `--dry-run`.
4. Create and submit one report, or add `--draft` to save without submitting.

## Quick start

### 1. Acquire a session

Choose one method.

#### Import a private HAR

```bash
chmod 600 expense-workspace.har

d365-expense session import work \
  --har expense-workspace.har
```

This is the browser-free path after import. The HAR must come from an
authenticated Expense workspace and must include sensitive request and response
data.

#### Import from a signed-in Edge browser

From a source checkout, start the dedicated browser profile:

```bash
./scripts/open-edge-cdp.sh
```

Sign in to Dynamics, open the **Expense management** workspace, and import the
session:

```bash
d365-expense session import work \
  --cdp http://127.0.0.1:9222
```

For the current CDP workflow, keep that Edge tab and browser open until the
expense command finishes. No raw HAR is written.

### 2. Preview the expense

Always start with a dry run:

```bash
d365-expense create \
  --session work \
  --purpose "Conference travel" \
  --receipt outbound.png \
  --receipt return.png \
  --receipt-note "Ground transportation" \
  --dry-run
```

The preview validates the session and every local receipt without creating an
expense report.

### 3. Create and submit the report

Review the preview, then run the same command without `--dry-run`:

```bash
d365-expense create \
  --session work \
  --purpose "Conference travel" \
  --receipt outbound.png \
  --receipt return.png \
  --receipt-note "Ground transportation"
```

This creates one report, attaches both receipts, and submits it.

### 4. Confirm the result

A successful result looks like:

```text
created and submitted report <report-id>: purpose="Conference travel"
status=<non-draft-status> receipts=2 ... submitted=true
```

Confirm these fields:

- a non-Draft status
- the expected receipt count
- `submitted=true`

## Common recipes

### Create a Draft without receipts

```bash
d365-expense create \
  --draft \
  --session work \
  --purpose "Team offsite"
```

### Save a Draft with several receipts

Repeat `--receipt`; order is preserved:

```bash
d365-expense create \
  --draft \
  --session work \
  --purpose "Customer visit" \
  --receipt flight.png \
  --receipt hotel.png \
  --receipt taxi.png
```

`--receipt-note` applies the same note to every receipt in the command.

### Create and submit a report

Preview first. Submission is already the default, and a dry run sends no
network requests:

```bash
d365-expense create \
  --session work \
  --purpose "Customer visit" \
  --receipt flight.png \
  --receipt hotel.png \
  --dry-run
```

After reviewing the plan, execute the same command without `--dry-run`:

```bash
d365-expense create \
  --session work \
  --purpose "Customer visit" \
  --receipt flight.png \
  --receipt hotel.png
```

The CLI admits only the exact `SubmitButton` exposed by the new report's
details form. It reports success only when the response identifies the same
report and shows that it is no longer Draft. Otherwise the operation fails and
a named session becomes `uncertain`; inspect Dynamics and do not retry.

### Use a HAR directly

Importing is optional:

```bash
d365-expense create \
  --draft \
  --har expense-workspace.har \
  --purpose "Conference travel" \
  --receipt taxi.png \
  --dry-run
```

Direct HAR mode is one-shot after a network operation begins. Named sessions
are safer for repeated use because the CLI can checkpoint updated sequence and
credential state.

### View or remove sessions

```bash
d365-expense session list
d365-expense session show work
d365-expense session remove work
```

`list` and `show` display only safe metadata and credential names, never secret
values.

## Receipt requirements

Every receipt must be:

- a regular, non-symlink file;
- PNG format with a `.png` extension;
- non-empty;
- no larger than 1,024,000 bytes; and
- owner-only on Unix-like systems, normally mode `0600`.

A single report can include up to 20 receipts. Receipts are attached at report
level; the CLI does not match them to individual expense lines.

## Session status

- **`ready`** — available for an expense command.
- **`expired`** — Dynamics rejected the authentication. Sign in and import
  again.
- **`uncertain`** — a remote operation may have partially succeeded. Inspect
  Dynamics, then import again.
- **`in_progress`** — a command started but did not finish cleanly. Do not
  retry; check for a running process.

If a crashed process left a stale lock, first confirm no `d365-expense` process
is using the session, then run:

```bash
d365-expense session unlock work
```

Unlocking does not make a non-ready session reusable. Import fresh
authentication afterward.

## Troubleshooting

- **A report was saved as Draft unexpectedly** — remove `--draft`; submission is
  the default create outcome.
- **Submit result cannot be verified** — the report may have changed remotely;
  do not retry. Inspect Dynamics and replace an `uncertain` named session.
- **Session is `expired`** — sign in again and re-import from HAR or CDP.
- **Session is `uncertain`** — check Dynamics for partial work, then replace
  the session.
- **Authentication redirects to Microsoft sign-in** — refresh authentication
  and import again.
- **Receipt is rejected** — convert it to a private PNG under the size limit.
- **CDP cannot find the Expense workspace** — open exactly one authenticated
  Expense workspace tab.
- **A dry run works but execution fails** — do not automatically retry; inspect
  the session status first.

More help is available in [the troubleshooting guide](docs/troubleshooting.md).

## Platform notes

- macOS and Linux support the complete workflow, including the Edge launcher
  and owner-only HAR capture.
- The CLI builds on Windows, but raw HAR capture is not supported where the CLI
  cannot guarantee Unix-style owner-only file permissions.
- CDP acquisition currently targets Microsoft Edge. HAR import does not require
  Edge after the capture already exists.

## Security and cleanup

- Treat HARs and session files like passwords.
- Keep CDP bound to loopback (`127.0.0.1`).
- Do not use the dedicated Edge profile for unrelated browsing.
- Never edit a session status back to `ready` manually.
- Never automatically retry an uncertain expense operation.
- Delete raw HAR files when they are no longer needed.
- Remove imported credentials with `d365-expense session remove <name>`.

Read the full [security model](docs/security.md) before using the CLI with real
expense data.

## More documentation

- [Sessions and recovery](docs/sessions.md)
- [CDP session acquisition](docs/cdp.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Advanced usage](docs/advanced-usage.md)
- [Migration from `msexpense`](docs/migration.md)
- [Dynamics protocol internals](docs/internals/dynamics-protocol.md)
- [Receipt protocol internals](docs/internals/receipt-protocol.md)
- [Contributing](CONTRIBUTING.md)
