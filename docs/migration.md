# Migration from msexpense

The repository is now `d365-expense-cli`, and the canonical binary is
`d365-expense`.

## Command mapping

- `msexpense create-draft` → `d365-expense create --draft`
- `msexpense create-draft-with-receipt` →
  `d365-expense create --draft --receipt FILE`
- `msexpense create-draft-with-receipts` →
  `d365-expense create --draft --receipt FILE ...`
- `msexpense attach-receipt` → `d365-expense receipt attach --draft`
- `msexpense inspect --har FILE` → `d365-expense har inspect FILE`
- `msexpense capture-draft` → `d365-expense har capture`
- `session inspect --name work` → `session show work`
- `session remove --name work` → `session remove work`

## Flag mapping

| Legacy | Canonical |
| --- | --- |
| `--file` | `--receipt` |
| `--notes` | `--receipt-note` |
| `--execute` | Canonical commands execute by default |
| Omit `--execute` | Use `--dry-run` |
| `--name work` | Positional session name: `work` |

There is no standalone legacy submit command. Canonical creation-time
submission uses `create --submit`; execution additionally requires
`--confirm-submit`. Legacy `--execute` by itself never authorizes submission.

## Environment variables

Canonical variables use the `D365_EXPENSE_` prefix. Legacy `MSEXPENSE_*`
variables remain temporary lower-precedence aliases.

## Behavioral difference

Legacy mutation commands remain dry-run-by-default and require `--execute`.
Canonical `create` executes by default and requires exactly one of `--draft` or
`--submit`; executing the latter also requires `--confirm-submit`. Use
`--dry-run` for preview.

The deprecated `msexpense` wrapper and flat commands emit warnings. They will be
removed in a future breaking release.
