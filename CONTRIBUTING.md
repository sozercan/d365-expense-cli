# Contributing to d365-expense-cli

Thank you for helping improve `d365-expense-cli`.

This project interacts with an unsupported, stateful Dynamics 365 web-client
protocol. Changes must preserve the Draft-only mutation boundary, strict input
validation, and fail-closed behavior.

## Prerequisites

- Go 1.24 or newer
- Git
- Microsoft Edge and `curl` for optional live CDP testing
- `shellcheck` for launcher changes
- A Markdown linter for documentation changes, when available

## Set up the repository

```bash
git clone https://github.com/sozercan/d365-expense-cli.git
cd d365-expense-cli
go mod download
```

Build the canonical and compatibility binaries:

```bash
go build -o bin/d365-expense ./cmd/d365-expense
go build -o bin/msexpense ./cmd/msexpense
```

## Repository layout

| Path | Responsibility |
| --- | --- |
| `cmd/d365-expense` | Canonical executable entry point |
| `cmd/msexpense` | Temporary deprecated binary wrapper |
| `internal/cli` | Kong command tree, routing adapters, and legacy aliases |
| `internal/capture` | Dynamics HAR and workspace-bootstrap parsing |
| `internal/cdphar` | CDP-backed network capture |
| `internal/dynamics` | Protocol models, builders, and allowlist validation |
| `internal/expense` | Draft creation and receipt workflows |
| `internal/har` | Generic HAR model and owner-only storage |
| `internal/session` | Named session persistence, status, and locking |
| `docs/internals` | Observed Dynamics protocol documentation |
| `scripts` | User/operator helper scripts |

## Development workflow

Format changed Go files:

```bash
gofmt -w ./cmd ./internal
```

Run the normal validation suite:

```bash
go test ./...
go test -race ./...
go vet ./...
go mod verify
```

Validate the Edge launcher:

```bash
bash -n scripts/open-edge-cdp.sh
shellcheck scripts/open-edge-cdp.sh
```

Build the supported command packages:

```bash
go build ./cmd/d365-expense
go build ./cmd/msexpense
```

Optional cross-compilation checks:

```bash
GOOS=linux GOARCH=amd64 go build ./cmd/d365-expense
GOOS=windows GOARCH=amd64 go build ./cmd/d365-expense
```

## Tests

Tests must not require live Dynamics access by default.

Prefer:

- table-driven unit tests;
- `httptest.Server` for HTTP workflows;
- synthetic Dynamics envelopes and response models;
- synthetic or deliberately reviewed HAR fixtures;
- exact CLI routing and output assertions;
- permission, symlink, locking, and atomic-write tests; and
- explicit negative tests for Submit, approval, posting, and workflow commands.

The major test areas are:

- **CLI tests** — canonical Kong parsing, legacy routing, exit codes, and help;
- **capture tests** — credential extraction, mixed sessions, sequence state, and
  workspace controls;
- **CDP tests** — loopback restrictions, event ordering, response bodies, and
  bounded capture;
- **expense tests** — command allowlists, dynamic control discovery, receipt
  ordering, partial failures, and SaveAndClose ordering; and
- **session tests** — owner-only storage, migration, status transitions, locks,
  and secret-safe summaries.

Live acceptance tests are manual, opt-in, and must use a disposable Draft-only
scenario. They must never submit an expense.

## Secret and fixture policy

Never commit:

- raw HAR files;
- imported session files;
- cookies, authorization headers, CSRF tokens, or upload tokens;
- real receipt images;
- real expense reports, report numbers, employee data, or financial details;
- browser profiles; or
- logs containing credential values.

Keep raw captures below ignored paths such as `captures/`, with owner-only
permissions. Checked-in fixtures must be synthetic and contain no transformed
or truncated production secrets.

Do not assume that a browser-generated “sanitized” HAR is safe to publish.
Review every field before creating a synthetic regression fixture.

## CLI design rules

The canonical binary is `d365-expense`.

Canonical commands use verbs and resource subcommands:

```text
create
receipt attach
session import|list|show|remove|unlock
har inspect|capture
version
```

Flags describe mode and input:

- `--draft` is required for mutations;
- `--receipt` is repeatable;
- canonical mutation commands execute by default;
- `--dry-run` disables network mutation; and
- no Submit flag or command may be added.

The old `msexpense` binary and flat commands are temporary compatibility
surfaces. Legacy create commands retain their historical dry-run-by-default
behavior. Compatibility changes must never broaden the mutation allowlist.

When changing CLI behavior, update all of:

1. Kong command structs and validation;
2. routing and compatibility tests;
3. `README.md` workflow examples;
4. relevant operator or migration documentation; and
5. generated/help-text assertions.

## Protocol changes

When Dynamics changes its web-client protocol:

1. Collect a private capture in an authorized environment.
2. Analyze it locally without publishing credentials or expense data.
3. Update the narrow protocol model and allowlist.
4. Create a synthetic regression fixture.
5. Add negative tests for unsafe alternate shapes.
6. Update the protocol provenance and security documentation.
7. Remove or securely retain the private capture outside Git.

Do not add generic command execution, arbitrary control targeting, automatic
retries after uncertain mutations, authentication bypasses, or submission
support.

## Documentation responsibilities

- `README.md` is entirely user-facing.
- `docs/security.md` is the operator and security reference.
- `docs/sessions.md`, `docs/cdp.md`, and `docs/troubleshooting.md` are operational
  guides.
- `docs/internals/` is for protocol maintainers.
- This file contains developer setup, architecture, testing, and contribution
  policy.

Keep examples generic and avoid real identifiers or dates unless they are
explicitly synthetic.

## Commits and pull requests

- Use Conventional Commit style, for example:
  - `feat(cli): add session status output`
  - `fix(capture): reject mixed channel state`
  - `docs: rewrite first-run guide`
- Sign commits with:

  ```bash
  git commit -s
  ```

- Use a semantic/Conventional Commit title for pull requests.
- Do not add `[codex]` to PR titles.
- Do not open a draft PR unless explicitly requested.
- Include the tests you ran and any live-validation limitations in the PR
  description.
