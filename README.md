# d365-expense

Create Dynamics 365 Finance expense reports from the command line, attach
receipts, submit reports, or save them as Drafts.

## What you can do

- Create and submit a new expense report.
- Save a new report as a Draft with `--draft`.
- Attach up to 20 PNG receipts to a new report.
- Preview a report with `--dry-run` before making changes.
- Reuse a named authenticated Dynamics session.
- Attach a receipt to an existing captured Draft.

## Requirements

- Access to the Dynamics 365 Finance **Expense management** workspace.
- Permission to create and submit expense reports.
- Go 1.24 or newer.
- Authentication from either:
  - a private browser HAR captured from the Expense workspace; or
  - a signed-in Microsoft Edge session available through local CDP.

Receipts must be PNG files no larger than 1,024,000 bytes. HAR files and saved
sessions contain credentials; keep them private and never commit them.

## Install

```bash
go install github.com/sozercan/d365-expense-cli/cmd/d365-expense@latest
```

Verify the installation:

```bash
d365-expense version
```

For local development and source builds, see
[CONTRIBUTING.md](CONTRIBUTING.md).

## Quick start

### 1. Import a session

Import a private HAR from an authenticated Expense workspace:

```bash
chmod 600 expense-workspace.har

d365-expense session import work \
  --har expense-workspace.har
```

Or import from a signed-in Edge browser running with local CDP:

```bash
d365-expense session import work \
  --cdp http://127.0.0.1:9222
```

The repository includes `./scripts/open-edge-cdp.sh` to start a dedicated Edge
profile on macOS or Linux. Sign in, open exactly one Expense management tab,
and keep it open until the import and immediate expense command finish.

### 2. Preview the report

```bash
d365-expense create \
  --session work \
  --purpose "Conference travel" \
  --receipt outbound.png \
  --receipt return.png \
  --receipt-note "Ground transportation" \
  --dry-run
```

A dry run validates the session and local receipt files without creating a
report or sending receipt data.

### 3. Create and submit

Run the reviewed command without `--dry-run`:

```bash
d365-expense create \
  --session work \
  --purpose "Conference travel" \
  --receipt outbound.png \
  --receipt return.png \
  --receipt-note "Ground transportation"
```

## Create reports

### Without receipts

```bash
d365-expense create \
  --session work \
  --purpose "Team offsite"
```

### With several receipts

Repeat `--receipt` in the order the files should be attached:

```bash
d365-expense create \
  --session work \
  --purpose "Customer visit" \
  --receipt flight.png \
  --receipt hotel.png \
  --receipt taxi.png
```

`--receipt-note` applies the same note to every receipt in the command.

### Save as a Draft

`create` submits the new report. Add `--draft` to save it without submitting:

```bash
d365-expense create \
  --draft \
  --session work \
  --purpose "Customer visit" \
  --receipt flight.png
```

### Use a HAR directly

A named session is recommended for normal use, but a private HAR can be used
for a one-off command:

```bash
d365-expense create \
  --har expense-workspace.har \
  --purpose "Conference travel" \
  --receipt taxi.png \
  --dry-run
```

Do not reuse a HAR after an executing command has started.

## Attach a receipt to an existing Draft

This compatibility workflow requires a private, report-specific HAR captured
with the Draft already open:

```bash
d365-expense receipt attach \
  --draft \
  --har receipt-attach.har \
  --report <report-id> \
  --receipt receipt.png \
  --dry-run
```

Remove `--dry-run` after reviewing the command. This operation attaches the
receipt and keeps the report as a Draft.

## Sessions

```bash
d365-expense session list
d365-expense session show work
d365-expense session remove work
```

Session states:

- **`ready`** — available for a command.
- **`expired`** — authentication is no longer accepted; sign in and import a new
  session.
- **`uncertain`** — a command may have partially completed; inspect the report
  in Dynamics and import a new session before continuing.
- **`in_progress`** — a command started but did not finish cleanly; check whether
  a process is still running.

If a crashed process leaves a stale lock, first verify that no
`d365-expense` process is using the session, then run:

```bash
d365-expense session unlock work
```

Unlocking does not make a non-ready session reusable. Import fresh
authentication afterward.

## Receipt requirements

Each receipt must be:

- a PNG with a `.png` extension;
- non-empty and no larger than 1,024,000 bytes;
- a regular, non-symlink file; and
- owner-only on macOS and Linux, normally mode `0600`.

A report can include up to 20 receipts. Receipts are attached to the report, not
to individual expense lines.

## Troubleshooting and recovery

- **Authentication expired or redirected to sign-in** — sign in again and
  import a new session.
- **Session is `uncertain`** — inspect Dynamics before doing anything else. Do
  not retry the same operation.
- **Receipt rejected** — verify the format, extension, size, and file
  permissions.
- **CDP cannot find the workspace** — leave exactly one authenticated Expense
  management tab open in the dedicated Edge profile.
- **Dry run succeeds but execution fails** — inspect the session state and the
  report in Dynamics before retrying with fresh authentication.

See [Troubleshooting](docs/troubleshooting.md) for detailed recovery guidance.

## Limitations

- `create` operates on new reports; it cannot reopen and submit an existing
  Draft by report number.
- The CLI does not approve, post, or recall reports.
- Receipts must be PNG files and are attached at report level.
- CDP acquisition currently targets Microsoft Edge.
- Raw HAR capture requires owner-only file permissions and is not supported on
  Windows when those permissions cannot be enforced.

## Security

- Treat HAR and session files like passwords.
- Keep CDP bound to loopback (`127.0.0.1`).
- Use the dedicated Edge profile only for Dynamics expense work.
- Never edit a session state back to `ready` manually.
- Never retry an operation whose result is uncertain.
- Delete raw HAR files when they are no longer needed.
- Remove saved credentials with `d365-expense session remove <name>`.

Read [Security model](docs/security.md) before using the CLI with real expense
data.

## Documentation

### Using the CLI

- [Sessions and recovery](docs/sessions.md)
- [CDP session acquisition](docs/cdp.md)
- [Advanced usage](docs/advanced-usage.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Migration from `msexpense`](docs/migration.md)

### Development and protocol details

- [Dynamics protocol internals](docs/internals/dynamics-protocol.md)
- [Receipt protocol internals](docs/internals/receipt-protocol.md)
- [Contributing](CONTRIBUTING.md)
