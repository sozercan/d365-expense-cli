# Advanced usage

## Direct HAR execution

Use a private workspace HAR without importing it:

```bash
d365-expense create \
  --draft \
  --har workspace.har \
  --purpose "Travel" \
  --receipt taxi.png
```

Treat the HAR as one-shot after a network operation begins. The CLI cannot
checkpoint updated sequence state back into the HAR.

Direct-HAR submission uses the same explicit gates:

```bash
d365-expense create \
  --submit \
  --confirm-submit \
  --har workspace.har \
  --purpose "Travel"
```

Prefer a named session so an unverified result can be persisted as
`uncertain`. Never reuse the HAR or retry the submission after execution begins.

## Attach a receipt to an existing captured Draft

This report-specific compatibility flow requires a receipt-capable HAR:

```bash
d365-expense receipt attach \
  --draft \
  --har receipt-attach.har \
  --report <report-id> \
  --receipt receipt.png \
  --dry-run
```

Remove `--dry-run` only after verifying the plan.
Receipt attachment never submits the existing Draft.

## Inspect a HAR

```bash
d365-expense har inspect workspace.har
```

Inspection prints only safe metadata and credential names.

## Capture a filtered HAR

```bash
d365-expense har capture \
  --out workspace.har \
  'https://tenant.operations.dynamics.com/?mi=ExpenseWorkspace&cmp=USMF'
```

Raw HAR output contains credentials and is written with owner-only permissions.
Prefer `session import NAME --cdp ENDPOINT` when a raw HAR is not required.

## Global flags

```text
--config-dir PATH
--timeout DURATION
--verbose / --quiet
--no-color
--version
```

Command-specific help is authoritative:

```bash
d365-expense <command> --help
```
