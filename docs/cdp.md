# CDP session acquisition

CDP—the Chrome DevTools Protocol—lets `d365-expense` acquire current Dynamics
authentication from a dedicated local Microsoft Edge profile without manually
exporting a HAR.

CDP is used only to acquire session state. Acquisition itself does not submit
an expense; a later `create` command submits by default unless `--draft` is
supplied.

## Requirements

- macOS or Linux
- Microsoft Edge
- `curl`
- Access to the Dynamics 365 Finance Expense management workspace
- A local loopback port, default `9222`

## Start the dedicated browser

From a source checkout:

```bash
./scripts/open-edge-cdp.sh
```

The script creates a dedicated profile below the `d365-expense` configuration
directory and binds CDP to `127.0.0.1`.

Open the Expense management workspace and complete Microsoft authentication.
Do not use this profile for unrelated browsing.

## Import the session

```bash
d365-expense session import work --cdp http://127.0.0.1:9222
```

The command requires exactly one open authenticated Expense workspace tab. It
records only the state needed for the CLI and does not write a raw HAR.

For the current CDP workflow, keep the source tab and browser open until the
immediate expense operation finishes. The Dynamics channel belongs to that live
tab.

## Environment variables

| Variable | Purpose |
| --- | --- |
| `D365_EXPENSE_CONFIG_DIR` | Configuration root |
| `D365_EXPENSE_CDP_PORT` | Loopback CDP port |
| `D365_EXPENSE_CDP_PROFILE_DIR` | Dedicated Edge profile |
| `D365_EXPENSE_CDP_START_TIMEOUT` | Browser startup timeout |
| `D365_EXPENSE_EDGE_BIN` | Edge executable override |

Legacy `MSEXPENSE_*` names remain temporary lower-precedence aliases.

## Security

A CDP endpoint controls the attached browser profile.

- Keep it bound to loopback.
- Do not expose the debugging port on a network interface.
- Do not browse unrelated sites in the dedicated profile.
- Close the dedicated browser when the expense workflow is complete.
- Remove the profile when it is no longer needed.

## Troubleshooting

### CDP endpoint is unavailable

Run:

```bash
curl --noproxy '*' http://127.0.0.1:9222/json/version
```

If that fails, restart `scripts/open-edge-cdp.sh` and check whether another
process is using the port.

### No Expense workspace tab was found

Open exactly one authenticated Expense management tab. Close duplicate,
sign-in, blank, or unrelated Dynamics tabs.

### Authentication redirects to Microsoft sign-in

Complete sign-in in the dedicated Edge profile and import again.

### The imported session fails after closing Edge

Reopen the dedicated profile, import again, and keep the source tab open until
the expense command completes. For a browser-free reusable session, import a
suitable raw HAR instead.
