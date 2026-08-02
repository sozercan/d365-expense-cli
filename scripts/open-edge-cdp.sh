#!/usr/bin/env bash
set -euo pipefail

legacy_alias_used() {
  local legacy_name="$1"
  local canonical_name="$2"
  if [[ -n "${!legacy_name:-}" && -z "${!canonical_name:-}" ]]; then
    printf 'warning: %s is deprecated; use %s instead\n' "$legacy_name" "$canonical_name" >&2
  fi
}

legacy_alias_used MSEXPENSE_CONFIG_DIR D365_EXPENSE_CONFIG_DIR
legacy_alias_used MSEXPENSE_CDP_PORT D365_EXPENSE_CDP_PORT
legacy_alias_used MSEXPENSE_CDP_PROFILE_DIR D365_EXPENSE_CDP_PROFILE_DIR
legacy_alias_used MSEXPENSE_CDP_START_TIMEOUT D365_EXPENSE_CDP_START_TIMEOUT
legacy_alias_used MSEXPENSE_EDGE_BIN D365_EXPENSE_EDGE_BIN

default_config_dir() {
  case "$(uname -s)" in
    Darwin)
      printf '%s/Library/Application Support/d365-expense' "$HOME"
      ;;
    Linux)
      printf '%s/d365-expense' "${XDG_CONFIG_HOME:-$HOME/.config}"
      ;;
    *)
      printf '%s/.config/d365-expense' "$HOME"
      ;;
  esac
}

PORT="${D365_EXPENSE_CDP_PORT:-${MSEXPENSE_CDP_PORT:-9222}}"
ADDRESS="127.0.0.1"
CONFIG_DIR="${D365_EXPENSE_CONFIG_DIR:-${MSEXPENSE_CONFIG_DIR:-$(default_config_dir)}}"
PROFILE_DIR="${D365_EXPENSE_CDP_PROFILE_DIR:-${MSEXPENSE_CDP_PROFILE_DIR:-$CONFIG_DIR/edge-cdp}}"
START_TIMEOUT="${D365_EXPENSE_CDP_START_TIMEOUT:-${MSEXPENSE_CDP_START_TIMEOUT:-15}}"
EDGE_BIN="${D365_EXPENSE_EDGE_BIN:-${MSEXPENSE_EDGE_BIN:-}}"
START_URL="${1:-about:blank}"
CURL_BIN="${CURL_BIN:-curl}"

usage() {
  cat <<EOF_USAGE
Usage:
  scripts/open-edge-cdp.sh [start-url]

Starts a separate Microsoft Edge instance with CDP enabled on loopback.

Environment variables:
  D365_EXPENSE_CONFIG_DIR        Configuration root
                                 (default: ${CONFIG_DIR})
  D365_EXPENSE_CDP_PORT          CDP port (default: 9222)
  D365_EXPENSE_CDP_PROFILE_DIR   Dedicated Edge profile directory
                                 (default: <config-dir>/edge-cdp)
  D365_EXPENSE_CDP_START_TIMEOUT Startup timeout in seconds (default: 15)
  D365_EXPENSE_EDGE_BIN          Edge executable override
  CURL_BIN                       curl executable override

Temporary compatibility aliases:
  MSEXPENSE_CONFIG_DIR
  MSEXPENSE_CDP_PORT
  MSEXPENSE_CDP_PROFILE_DIR
  MSEXPENSE_CDP_START_TIMEOUT
  MSEXPENSE_EDGE_BIN

Canonical D365_EXPENSE_* variables take precedence over legacy aliases.
EOF_USAGE
}

if [[ "$START_URL" == "-h" || "$START_URL" == "--help" ]]; then
  usage
  exit 0
fi

if [[ ! "$PORT" =~ ^[0-9]+$ ]] || ((PORT < 1 || PORT > 65535)); then
  printf 'Invalid D365_EXPENSE_CDP_PORT: %s\n' "$PORT" >&2
  exit 2
fi
if [[ ! "$START_TIMEOUT" =~ ^[0-9]+$ ]] || ((START_TIMEOUT < 1)); then
  printf 'Invalid D365_EXPENSE_CDP_START_TIMEOUT: %s\n' "$START_TIMEOUT" >&2
  exit 2
fi
if ! command -v "$CURL_BIN" >/dev/null 2>&1; then
  printf 'curl executable not found: %s\n' "$CURL_BIN" >&2
  exit 1
fi

ENDPOINT="http://${ADDRESS}:${PORT}"

cdp_ready() {
  local response
  response="$("$CURL_BIN" --noproxy '*' -fsS --max-time 1 "${ENDPOINT}/json/version" 2>/dev/null)" || return 1
  [[ "$response" == *'"webSocketDebuggerUrl"'* ]]
}

if cdp_ready; then
  printf 'A CDP endpoint is already ready: %s\n' "$ENDPOINT"
  printf 'The existing browser profile was not changed or verified.\n'
  exit 0
fi

umask 077
mkdir -p "$PROFILE_DIR"
chmod 700 "$PROFILE_DIR"

edge_args=(
  "--remote-debugging-port=${PORT}"
  "--remote-debugging-address=${ADDRESS}"
  "--user-data-dir=${PROFILE_DIR}"
  "--no-first-run"
  "--no-default-browser-check"
  "$START_URL"
)

case "$(uname -s)" in
  Darwin)
    if [[ -n "$EDGE_BIN" ]]; then
      if [[ ! -x "$EDGE_BIN" ]]; then
        printf 'Microsoft Edge executable is not executable: %s\n' "$EDGE_BIN" >&2
        exit 1
      fi
      "$EDGE_BIN" "${edge_args[@]}" >/dev/null 2>&1 &
    else
      open -na "Microsoft Edge" --args "${edge_args[@]}"
    fi
    ;;
  Linux)
    edge_bin="$EDGE_BIN"
    if [[ -z "$edge_bin" ]]; then
      for candidate in microsoft-edge microsoft-edge-stable microsoft-edge-beta microsoft-edge-dev; do
        if command -v "$candidate" >/dev/null 2>&1; then
          edge_bin="$(command -v "$candidate")"
          break
        fi
      done
    fi
    if [[ -z "$edge_bin" || ! -x "$edge_bin" ]]; then
      printf 'Microsoft Edge was not found; set D365_EXPENSE_EDGE_BIN.\n' >&2
      exit 1
    fi
    nohup "$edge_bin" "${edge_args[@]}" >/dev/null 2>&1 &
    ;;
  *)
    printf 'Unsupported platform: %s\n' "$(uname -s)" >&2
    exit 1
    ;;
esac

for ((attempt = 0; attempt < START_TIMEOUT * 10; attempt++)); do
  if cdp_ready; then
    printf 'Edge CDP ready: %s\n' "$ENDPOINT"
    printf 'Profile: %s\n' "$PROFILE_DIR"
    printf 'Keep this Edge instance open while running d365-expense session import NAME --cdp %s or d365-expense har capture.\n' "$ENDPOINT"
    exit 0
  fi
  sleep 0.1
done

printf 'Edge did not expose CDP at %s within %ss.\n' "$ENDPOINT" "$START_TIMEOUT" >&2
printf 'Check whether the port is in use or Edge remote debugging is disabled by policy.\n' >&2
exit 1
