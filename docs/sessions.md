# Sessions and recovery

A session is the authenticated Dynamics state used by `d365-expense`. It
contains credentials and stateful sequence information, so it must be handled
like a password.

## Import a session

### From a HAR

```bash
d365-expense session import work --har expense-workspace.har
```

HAR import is the browser-free path after import. The HAR must be private,
current, and captured from the Expense management workspace.

### From local CDP

```bash
d365-expense session import work --cdp http://127.0.0.1:9222
```

The command auto-detects exactly one authenticated Expense workspace tab. Keep
that tab and browser open until the immediate expense operation completes.

Replace an existing named session deliberately with `--force`:

```bash
d365-expense session import work --har expense-workspace.har --force
```

## Inspect sessions

```bash
d365-expense session list
d365-expense session show work
```

These commands print safe metadata and credential names, not secret values.

## Status lifecycle

- **`ready`** — the session may be used.
- **`in_progress`** — an operation started and may have been interrupted. Do
  not retry automatically.
- **`uncertain`** — Dynamics may have accepted partial work or a submission that
  the CLI could not verify. Inspect Dynamics, do not retry, and re-import.
- **`expired`** — authentication was rejected or redirected to sign-in. Sign
  in and re-import.

Only `ready` sessions can execute a mutation.

## Locks

A named session is locked for the entire expense operation. This prevents two
processes from advancing the same sequence state concurrently.

If a process crashed and left a stale lock:

1. Verify no `d365-expense` process is still using the session.
2. Remove the stale lock:

   ```bash
   d365-expense session unlock work
   ```

3. Re-import current authentication.

Unlocking never changes the session status and never makes old state reusable.

## Storage

Sessions are stored below the operating system's user configuration directory:

```text
<user-config-dir>/d365-expense/sessions/<name>.session.json
```

Common roots:

- macOS: `~/Library/Application Support/d365-expense`
- Linux: `${XDG_CONFIG_HOME:-$HOME/.config}/d365-expense`
- Windows: `%AppData%\d365-expense`

Override the root with:

```bash
export D365_EXPENSE_CONFIG_DIR=/private/path/to/d365-expense
```

Session files are authenticated and encrypted with a per-session key held by
the operating system keyring (macOS Keychain, Windows Credential Manager, or
Linux Secret Service). On Unix-like systems, configuration directories are
owner-only (`0700`) and encrypted session files are owner-only (`0600`).
Symlinks and broadly accessible session files are rejected. If the keyring is
locked or unavailable, the CLI fails closed instead of writing plaintext.

Legacy plaintext sessions are still readable. The next executing named-session
command migrates the file when it writes its pre-network `in_progress`
checkpoint, so no browser reauthentication is required solely for migration.
Session names are bound into authenticated encryption; use the exact casing
shown by `session list`, including on case-insensitive filesystems.

## Remove credentials

```bash
d365-expense session remove work
```

Also delete any raw HAR used for import when it is no longer needed.

If removal or forced replacement reports that the session file was committed
but an old key could not be removed, retry the keyring cleanup using the exact
non-secret key ID from that error:

```bash
d365-expense session cleanup-key <key-id>
```

## Direct HAR mode

You can skip session import and pass `--har` directly to `create`, which submits
by default unless `--draft` is supplied. Direct HAR mode does not checkpoint
updated sequence state, so it must be treated as one-shot after any network
request begins. Named sessions are safer for submission because uncertain
outcomes are persisted as non-reusable state.
