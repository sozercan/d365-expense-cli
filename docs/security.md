# Security model

## Default-submission boundary

`d365-expense-cli` can save a new report as a Draft or explicitly submit that
same newly created report.

- `create` submits by default; `--draft` is the explicit opt-out that selects
  Save and close instead.
- `--dry-run` sends no network requests for either outcome.
- `receipt attach` remains Draft-only and requires `--draft`.
- Draft and receipt validators still reject submit, workflow, approval, posting,
  and recall commands. A separate submission validator permits only one exact
  `Click` against the dynamically discovered `SubmitButton` for the newly
  created report.
- Submission success requires response evidence for the same report explicitly
  showing `Submitted` or the modeled status code `2`. Stable code evidence takes
  precedence over localized display text; unknown non-Draft states without that
  evidence, an HTTP success, or a sequence acknowledgement alone are insufficient.
- No generic click, control, message, or upload interface is exposed.

Canonical mutating commands execute by default. Use `--dry-run` to perform local
validation without sending network requests, and use `--draft` only to prevent
the default submission. The CLI does not approve, post, recall, or submit an
already-closed Draft by report number.

## Raw captures and imported sessions are credentials

An executable HAR contains authenticated cookies and CSRF/session headers used
by the Dynamics browser. Anyone who can read it may be able to act as that
browser session until the server expires or revokes it.

A named session is smaller than a HAR but is still a plaintext credential. It
contains the Dynamics origin and endpoint, company and language, channel and
sequence state, allowlisted request headers and cookies, and the current New
Report control. It intentionally omits HAR response history and unrelated
browser traffic, but it requires the same protection as the raw capture.

- Raw HARs must be regular, non-symlink files with owner-only permissions.
- Imported sessions are stored as owner-only files and must be ignored by Git.
- The CLI never prints cookie, token, or header values.
- `session list` and `session show NAME` expose only safe metadata and
  credential names.
- Sanitized captures have authentication values redacted and response bodies
  removed; they are inspectable but intentionally not executable.

## Configuration and session storage

The canonical configuration root is:

```text
<os.UserConfigDir()>/d365-expense
```

Sessions are stored below it:

```text
<user-config-dir>/d365-expense/sessions/<name>.session.json
```

Typical roots are:

- macOS: `~/Library/Application Support/d365-expense`
- Linux: `${XDG_CONFIG_HOME:-$HOME/.config}/d365-expense`
- Windows: `%AppData%\d365-expense`

`D365_EXPENSE_CONFIG_DIR` overrides the root. `MSEXPENSE_CONFIG_DIR` remains a
temporary lower-precedence compatibility alias.

Configuration and session directories require mode `0700`; session files
require mode `0600`. Writes are atomic. The store rejects symlinks, non-regular
files, oversized files, unknown session fields or versions, and permissions
that grant group or other access. Session names are limited to 1–64 ASCII
letters, digits, `.`, `_`, or `-`.

Executed named-session commands hold an exclusive per-session lock so two
processes cannot advance one Dynamics channel concurrently. `--dry-run` reads
and validates the session but sends no requests and does not change its state.

## Session status and recovery

Session reuse is fail-closed:

- `ready` means the session can be used for one serialized operation;
- `in_progress` is written before the first network mutation;
- `uncertain` means execution or local checkpointing failed after mutation may
  have begun; and
- `expired` means authentication is known to be unusable or revoked.

A successful operation checkpoints current replay state and returns the session
to `ready`. A process crash can leave `in_progress`. A remote failure,
unexpected Dynamics response, or checkpoint problem can produce `uncertain` or
leave `in_progress`. Dynamics may have accepted a request even if the CLI did
not finish.

Only `ready` sessions are executable. Never manually edit a status back to
`ready`, and never automatically retry a failed expense operation. For
`in_progress`, `uncertain`, or `expired`, obtain current authentication and
re-import it:

```bash
d365-expense session import work --har expense-workspace.har --force
# or
d365-expense session import work --cdp http://127.0.0.1:9222 --force
```

If a crash left only a stale lock, first prove no process is still using the
session, then run `d365-expense session unlock work`. Unlocking never makes the
old session reusable.

Direct `--har` execution remains available, but its input is immutable. Because
no advanced sequence state is persisted, treat it as one-shot and do not reuse
it after a network operation begins.

## CDP boundary

`scripts/open-edge-cdp.sh` starts a separate Microsoft Edge profile below the
canonical configuration root:

```text
<user-config-dir>/d365-expense/edge-cdp
```

The directory is restricted to mode `0700`, and CDP binds to `127.0.0.1`.
Remote debugging grants control of that browser instance. Keep the endpoint on
loopback, keep the dedicated browser open only as long as needed, and do not use
that profile for unrelated browsing.

The launcher uses `D365_EXPENSE_*` variables. Corresponding `MSEXPENSE_*`
variables remain temporary lower-precedence aliases and may emit deprecation
warnings.

Two canonical operations may consume CDP:

```bash
d365-expense session import work --cdp http://127.0.0.1:9222
d365-expense har capture --cdp http://127.0.0.1:9222 --out workspace.har URL
```

`session import NAME --cdp` validates the authenticated Expense bootstrap in
memory and stores only a compact session. `har capture` writes the filtered raw
HAR only after validation succeeds. Neither operation creates, saves, or
submits an expense report.

Response bodies are collected because Dynamics models are required to discover
the active workspace and New Report control. CDP may observe other primary-tab
traffic in memory during capture. Do not navigate elsewhere or enter unrelated
sensitive information while acquisition is running.

The acquisition path retains only same-origin `POST` traffic to the exact
Dynamics `ReliableCommunicationManager.svc/ProcessMessages` endpoint. It must
reject ambiguous targets, incomplete or unacknowledged state, failed model
validation, and authentication redirects rather than persisting partial state.

A browser is optional for acquiring fresh authentication. Once a named session
exists, report creation and default creation-time submission are browser-free.

## Mutation boundary

The canonical CLI supports these bounded mutations:

1. `d365-expense create --draft` creates and saves a new Draft;
2. repeatable `--receipt FILE` flags attach one or more report-level PNG
   receipts before the chosen final action;
3. `d365-expense receipt attach --draft` attaches a receipt to an already open,
   report-specific captured Draft; and
4. `d365-expense create` creates a new Draft, attaches any receipts, validates
   the exact `SubmitButton` contract, and submits only after every prior stage
   succeeds. `--draft` selects `SaveAndClose` instead.

Receipt attachment uses exact stage-specific commands, a same-origin
`/filemanagement` upload, a short-lived token discovered during execution, and
the response model's `SaveAndClose`. Files are limited to 1,024,000 bytes;
chunked uploads and expense-line matching are not implemented. The CLI
validates permissions and PNG magic, then holds an immutable bounded snapshot
of validated bytes so pathname replacement cannot change what is uploaded.

For multi-receipt creation, every local file is validated before the first
network request. Receipts are attached in argument order, and cumulative count
is verified after each upload. `SaveAndClose` or `SubmitButton` is sent only
after all receipts succeed. A failure after any accepted request marks a named
session non-ready; the CLI does not automatically retry or compensate.

The captured response models consistently expose `SubmitButton` backed by the
`TrvSubmit` action, but the repository does not contain a successful outbound
Submit capture. The implementation therefore fail-closes on metadata drift and
requires positive post-submit status evidence. Before first production use,
validate the path with an authorized disposable report and inspect the result in
Dynamics. Never retry an unverified submission.

The built-in upload contract is used by default. The old
`--receipt-protocol-har` flag remains only as a temporary compatibility input;
when supplied, only its non-secret fixed contract is used.

## Compatibility boundary

The `msexpense` binary, old flat commands, legacy flags, and `MSEXPENSE_*`
environment variables remain temporary migration aliases. Canonical
`d365-expense`, `D365_EXPENSE_*`, and nested command syntax take precedence.
Compatibility must never broaden an operation allowlist or silently change the
canonical default-submit / explicit-Draft contract.

## Operational limitations

The internal Dynamics protocol uses expiring browser authentication,
session-scoped sequence numbers, and dynamic control IDs. Imported sessions are
bound to their captured origin and company. Server-side changes, credential
revocation, concurrent use, or replay from stale sequence state can invalidate
a session.

On redirects, authentication failures, unexpected response models, validation
messages, or ambiguous state, the client stops rather than following alternate
commands. The absence of a successful CLI result does not prove Dynamics
accepted no mutation; use persisted status and re-import policy instead of
retrying uncertain work.
