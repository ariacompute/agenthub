#!/usr/bin/env bash
# deploy-cli.sh — make one of the example CLIs executable on WSL2 / Ubuntu.
#
# Usage:
#   ./deploy-cli.sh <cli> [command] [options]
#
# CLIs (from resources/cli-<cli>-ubuntu.json):
#   gh huggingface modelscope
#
# Commands (default: start):
#   start    resolve the command from the catalogue, look it up on PATH, check it
#            against AGENTHUB_CLI_ALLOWLIST and record the deployment
#   stop     remove the state file; the binary itself is never touched
#   restart  stop, then start
#   status   PATH resolution, allowlist verdict, declared subcommands, log
#            (exit 1 when nothing is deployed)
#   smoke    run a read-only --version / --help probe and print the output
#
# A CLI is a binary on PATH, not a service: no process, no pid and no port.
# `start` never installs anything and never backgrounds anything; it verifies the
# two gates the server itself uses (`entryCommand` + the whole-string allowlist)
# and records the result. `smoke` runs only --version / --help / version — never
# login, upload, download or any other subcommand from the catalogue.
#
# Options:
#   --allowlist <csv>  override AGENTHUB_CLI_ALLOWLIST for the preflight
#   --workdir <dir>    default $HOME/.local/share/agenthub-cli-<cli>
#   --wait <sec>       timeout for each smoke probe, default 20
#   --skip-allowlist   record the deployment even when the server would deny it
#   --dry-run          print the resolved plan and change nothing
#   -h | --help        this text
#
# Configuration: repository-root .env first, then .env.example for the keys .env
# leaves out; an already exported variable beats both, a flag beats everything.
# The files are parsed, not sourced. AGENTHUB_CLI_ALLOWLIST defaults to the same
# list as internal/config (git,ls,cat,echo,date,aria-memo). The server reads that
# variable once at startup: after changing it, restart the server, not this script.
#
# This script never talks to agenthub: no login, no load.sh, no /probe. CLI
# resources have no endpoint, and the server-side connector is still TODO(real).
#
# --- end of usage ---

set -euo pipefail

SELF="$0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RES_DIR="$SCRIPT_DIR/resources"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

CLIS="gh huggingface modelscope"
COMMANDS="start stop restart status smoke"
DEFAULT_ALLOWLIST="git,ls,cat,echo,date,aria-memo"

CLI=""
ACTION=""
WORKDIR=""
WAIT="20"
ALLOWLIST_ARG=""
SKIP_ALLOWLIST=0
DRY_RUN=0

BODY="$(mktemp)"
trap 'rm -f "$BODY"' EXIT

print_help() {
  sed -n '2,/^# --- end of usage ---$/p' "$SELF" |
    sed -e 's/^# \{0,1\}//' -e '/^--- end of usage ---$/d'
}

usage_error() {
  echo "$1" >&2
  echo "supported CLIs: $CLIS" >&2
  echo "run '$SELF --help' for the full usage" >&2
  exit 2
}

# Parse KEY=VALUE instead of sourcing: a .env is data, and this script runs with
# `set -euo pipefail`. Only unset keys are filled, so an exported value wins.
load_env_file() {
  local file="$1" line key value
  [ -f "$file" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    case "$line" in '' | '#'*) continue ;; esac
    case "$line" in export\ *) line="${line#export }" ;; esac
    case "$line" in *=*) ;; *) continue ;; esac
    key="${line%%=*}"
    value="${line#*=}"
    key="${key#"${key%%[![:space:]]*}"}"
    key="${key%"${key##*[![:space:]]}"}"
    case "$key" in [A-Za-z_][A-Za-z0-9_]*) ;; *) continue ;; esac
    case "$value" in
      \"*\") value="${value#\"}" && value="${value%\"}" ;;
      \'*\') value="${value#\'}" && value="${value%\'}" ;;
    esac
    if ! declare -p "$key" >/dev/null 2>&1; then
      export "$key=$value"
    fi
  done <"$file"
}

require() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing dependency: $1" >&2
    exit 2
  }
}

# ---- arguments ---------------------------------------------------------------

while [ $# -gt 0 ]; do
  case "$1" in
    -h | --help) print_help; exit 0 ;;
    --allowlist) ALLOWLIST_ARG="${2:-}"; shift ;;
    --workdir) WORKDIR="${2:-}"; shift ;;
    --wait) WAIT="${2:-}"; shift ;;
    --skip-allowlist) SKIP_ALLOWLIST=1 ;;
    --dry-run) DRY_RUN=1 ;;
    start | stop | restart | status | smoke) ACTION="$1" ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *)
      if [ -n "$CLI" ]; then
        echo "unexpected argument: $1 (cli is already '$CLI')" >&2
        exit 2
      fi
      CLI="$1"
      ;;
  esac
  shift
done

[ -n "$CLI" ] || usage_error "no cli given"
case " $CLIS " in
  *" $CLI "*) ;;
  *) usage_error "unsupported cli: $CLI" ;;
esac
ACTION="${ACTION:-start}"
case " $COMMANDS " in
  *" $ACTION "*) ;;
  *) usage_error "unsupported command: $ACTION" ;;
esac
case "$WAIT" in
  '' | *[!0-9]*) echo "--wait must be a number of seconds, got: $WAIT" >&2; exit 2 ;;
esac

load_env_file "$ROOT_DIR/.env"
load_env_file "$ROOT_DIR/.env.example"

ALLOWLIST="${ALLOWLIST_ARG:-${AGENTHUB_CLI_ALLOWLIST:-$DEFAULT_ALLOWLIST}}"

# ---- catalogue spec ----------------------------------------------------------

RES_FILE="$RES_DIR/cli-$CLI-ubuntu.json"
[ -f "$RES_FILE" ] || {
  echo "missing catalogue entry: $RES_FILE" >&2
  exit 2
}
require python3

# entryCommand in internal/adapter/cli.go prefers kind_config.cli.command and
# falls back to metadata.command. Resolve in the same order, and say so when the
# two declarations disagree: the allowlist has to match the string actually used.
json_spec() {
  python3 - "$RES_FILE" <<'PY' 2>/dev/null || true
import json, sys

with open(sys.argv[1]) as fh:
    doc = json.load(fh)
cfg = (doc.get("kind_config") or {}).get("cli") or {}
md = doc.get("metadata") or {}


def emit(key, value):
    print("%s=%s" % (key, str(value or "").replace("\n", " ")))


emit("ID", doc.get("id"))
emit("ENDPOINT", doc.get("endpoint"))
emit("COMMAND", (cfg.get("command") or md.get("command") or "").strip())
emit("META_COMMAND", (md.get("command") or "").strip())
emit("DECLARED", ",".join(cfg.get("allowlist") or []))
emit("TIMEOUT_MS", cfg.get("timeout_ms"))
emit("MAX_OUTPUT_BYTES", cfg.get("max_output_bytes"))
emit("INSTALL_HINT", md.get("install"))
emit("PATH_HINT", md.get("path_hint"))
emit("ENV_HINT", md.get("env_hint"))
emit("DOCS", md.get("docs"))
PY
}

SPEC="$(json_spec)"
spec() {
  printf '%s\n' "$SPEC" | sed -n "s/^$1=//p" | head -n 1 || true
}

CMD="$(spec COMMAND)"
META_CMD="$(spec META_COMMAND)"
[ -n "$CMD" ] || {
  echo "$RES_FILE declares no cli command (kind_config.cli.command / metadata.command)" >&2
  exit 2
}
case "$CMD" in
  *$'\n'*)
    echo "the catalogue command contains a newline: $CMD" >&2
    exit 2
    ;;
esac

RES_ID="$(spec ID)"
ENDPOINT="$(spec ENDPOINT)"
DECLARED="$(spec DECLARED)"
TIMEOUT_MS="$(spec TIMEOUT_MS)"
MAX_OUTPUT_BYTES="$(spec MAX_OUTPUT_BYTES)"
INSTALL_HINT="$(spec INSTALL_HINT)"
PATH_HINT="$(spec PATH_HINT)"
ENV_HINT="$(spec ENV_HINT)"
DOCS="$(spec DOCS)"

if [ -n "$META_CMD" ] && [ "$META_CMD" != "$CMD" ]; then
  echo "note: kind_config.cli.command ($CMD) differs from metadata.command ($META_CMD);" >&2
  echo "      the server uses $CMD, so the allowlist must contain that exact string" >&2
fi

WORKDIR="${WORKDIR:-${XDG_DATA_HOME:-${HOME:-/tmp}/.local/share}/agenthub-cli-$CLI}"
STATE="$WORKDIR/path"
LOG_FILE="$WORKDIR/$CLI.log"

# ---- helpers -----------------------------------------------------------------

resolved_path() {
  command -v "$CMD" 2>/dev/null || true
}

# The server compares strings.EqualFold against the WHOLE string, after trimming
# each comma-separated entry. Mirror that exactly here.
allowlisted() {
  local want item
  want="${1,,}"
  local IFS=','
  for item in $ALLOWLIST; do
    item="$(printf '%s' "$item" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
    item="${item,,}"
    if [ "$item" = "$want" ]; then
      return 0
    fi
  done
  return 1
}

CMD_PATH="$(resolved_path)"
VERDICT="denied"
if allowlisted "$CMD"; then
  VERDICT="allowed"
fi

show_plan() {
  echo "id:        $RES_ID"
  echo "command:   $CMD"
  echo "on PATH:   ${CMD_PATH:-<not found>}"
  echo "allowlist: $ALLOWLIST"
  echo "verdict:   $VERDICT"
  echo "declared:  ${DECLARED:-<none>} (registration-time subcommands)"
  echo "limits:    timeout=${TIMEOUT_MS:-<none>}ms max_output=${MAX_OUTPUT_BYTES:-<none>}B"
  echo "workdir:   $WORKDIR"
  echo "endpoint:  ${ENDPOINT:-<none, CLI resources have no endpoint>}"
  echo "install:   ${INSTALL_HINT:-<none>}"
  if [ -n "$PATH_HINT" ]; then
    echo "path hint: $PATH_HINT"
  fi
  if [ -n "$ENV_HINT" ]; then
    echo "env hint:  $ENV_HINT (export these; values are never written to disk)"
  fi
  if [ -n "$DOCS" ]; then
    echo "docs:      $DOCS"
  fi
}

# ---- commands ----------------------------------------------------------------

start_cli() {
  if [ "$DRY_RUN" = "1" ]; then
    show_plan
    return 0
  fi
  if [ -z "$CMD_PATH" ]; then
    echo "$CMD is not on PATH; nothing to deploy" >&2
    if [ -n "$INSTALL_HINT" ]; then
      echo "install hint: $INSTALL_HINT" >&2
    fi
    if [ -n "$PATH_HINT" ]; then
      echo "expected path: $PATH_HINT" >&2
    fi
    return 1
  fi
  if [ "$VERDICT" != "allowed" ] && [ "$SKIP_ALLOWLIST" != "1" ]; then
    echo "$CMD is on PATH but AGENTHUB_CLI_ALLOWLIST does not contain it" >&2
    echo "the server compares the whole string (EqualFold); add the exact token and restart the server:" >&2
    echo "  AGENTHUB_CLI_ALLOWLIST=$ALLOWLIST,$CMD" >&2
    echo "or pass --allowlist <csv>, or --skip-allowlist to record it anyway" >&2
    return 1
  fi
  mkdir -p "$WORKDIR"
  printf '%s\n' "$CMD_PATH" >"$STATE"
  printf 'command=%s\npath=%s\nallowlist=%s\nverdict=%s\n' \
    "$CMD" "$CMD_PATH" "$ALLOWLIST" "$VERDICT" >"$LOG_FILE"
  echo "$CLI deployed: $CMD -> $CMD_PATH (allowlist: $VERDICT)"
  if [ "$VERDICT" != "allowed" ]; then
    echo "warning: the server would currently deny $CMD; --skip-allowlist only records the deployment" >&2
  fi
  echo "verify it with: $SELF $CLI smoke"
}

stop_cli() {
  rm -f "$STATE"
  echo "$CLI unpublished (no process to stop; $CMD is left on PATH)"
}

status_cli() {
  local deployed="no"
  if [ -f "$STATE" ]; then
    deployed="yes"
  fi
  echo "deployed      $deployed"
  echo "registered    ${ENDPOINT:-<none, CLI resources have no endpoint>}"
  echo "command       $CMD -> ${CMD_PATH:-<not found>}"
  echo "allowlist     $ALLOWLIST"
  echo "verdict       $VERDICT"
  echo "declared      ${DECLARED:-<none>}"
  echo "workdir       $WORKDIR"
  if [ -f "$STATE" ]; then
    echo "state         $STATE ($(cat "$STATE"))"
  fi
  if [ -f "$LOG_FILE" ]; then
    echo "log           $LOG_FILE"
    tail -n 10 "$LOG_FILE"
  fi
  if [ -f "$STATE" ] && [ -n "$CMD_PATH" ]; then
    return 0
  fi
  return 1
}

# Only read-only probes ever run. Each is a single argv token, so nothing from
# the catalogue can turn into a shell command.
smoke_cli() {
  local probes probe status
  case "$CLI" in
    gh) probes="--version|--help" ;;
    huggingface) probes="version|--version|--help" ;;
    modelscope) probes="--help|--version" ;;
  esac
  if [ "$DRY_RUN" = "1" ]; then
    show_plan
    echo "would try (read-only): $probes"
    return 0
  fi
  if [ -z "$CMD_PATH" ]; then
    echo "$CMD is not on PATH; deploy first: $SELF $CLI start" >&2
    return 1
  fi
  require timeout
  mkdir -p "$WORKDIR"
  while IFS= read -r probe; do
    [ -n "$probe" ] || continue
    : >"$BODY"
    status=0
    timeout "$WAIT" "$CMD" "$probe" >"$BODY" 2>&1 || status=$?
    cat "$BODY" >>"$LOG_FILE"
    if [ "$status" = "0" ]; then
      echo "probe         $CMD $probe -> exit 0"
      head -n 12 "$BODY"
      return 0
    fi
    echo "  $CMD $probe exited $status, trying the next read-only probe" >&2
  done < <(printf '%s\n' "$probes" | tr '|' '\n')
  echo "every read-only probe failed for $CMD; see $LOG_FILE" >&2
  tail -n 12 "$LOG_FILE" >&2 || true
  return 1
}

case "$ACTION" in
  start) start_cli ;;
  stop) stop_cli ;;
  restart) stop_cli; start_cli ;;
  status) status_cli ;;
  smoke) smoke_cli ;;
esac
