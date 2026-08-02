# Troubleshooting

Start with a dry run whenever possible:

```bash
d365-expense create --draft --session work --purpose "Test" --dry-run
```

## Common errors

### `--draft is required`

All mutating canonical commands require the explicit `--draft` safety flag.
There is no submit mode.

### Exactly one of `--har` or `--session` is required

Choose one authentication source:

```bash
d365-expense create --draft --session work --purpose "Travel"
```

or:

```bash
d365-expense create --draft --har workspace.har --purpose "Travel"
```

### Session is `expired`

Dynamics rejected the authentication or redirected to Microsoft sign-in.
Acquire current authentication and import again.

### Session is `uncertain`

The CLI cannot prove whether Dynamics accepted all or part of the operation.
Do not retry automatically. Inspect Dynamics for a partially created Draft,
then import a fresh session.

### Session is `in_progress`

A process may still be running or may have crashed. Verify no process is using
the session. If the lock is stale, run `session unlock`, then re-import.

### Receipt is not accepted

Verify that the file:

- is a PNG;
- has a `.png` extension;
- is not empty;
- is no larger than 1,024,000 bytes;
- is not a symlink; and
- has owner-only permissions on Unix-like systems.

### `New expense report` control was not found

The session may have been imported from the wrong Dynamics page, a stale HAR,
or a browser tab whose state changed. Return to Expense management and import
again.

### CDP found zero or multiple workspace tabs

Leave exactly one authenticated Expense management tab open in the dedicated
Edge profile.

### Authentication works in the browser but not in the CLI

Re-import immediately before creating the Draft. Sessions can expire or become
stale when the browser navigates, reloads, or closes its stateful tab.

### A partial receipt batch failed

Earlier receipts may already be attached, while later receipts were not. The
CLI intentionally does not retry, compensate, or select Save and close after a
partial failure. Inspect the Draft in Dynamics and re-import before any further
action.

## Collecting safe diagnostics

Useful, non-secret information includes:

- the command and flags with paths and identifiers redacted;
- the exit code;
- session status from `session show`;
- whether the operation was a dry run;
- the CLI version; and
- the exact error message after removing private paths.

Never paste raw HAR contents, session JSON, cookies, headers, receipt images, or
financial data into an issue.
