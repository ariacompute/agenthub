#!/usr/bin/env bash
# deploy-mcp.sh — stand up one of the example stdio MCP servers on WSL2 / Ubuntu.
#
# Usage:
#   ./deploy-mcp.sh <server> [command] [options]
#
# Servers (from resources/mcp-<server>-ubuntu.json):
#   filesystem github google-drive google-docs gmail slack stripe wechat dingtalk feishu
#
# Commands (default: start):
#   start    resolve the command from the catalogue, check its dependencies and
#            serve it in the background with a FIFO held open on stdin
#   stop     TERM the recorded pid (KILL when it ignores TERM), then close the FIFO
#   restart  stop, then start
#   status   pid, the resolved command line and the tail of the log
#   smoke    send MCP initialize + tools/list over the FIFO and print the tools
#
# Options:
#   --workdir <dir>    default $HOME/.local/share/agenthub-mcp-<server>
#   --wait <sec>       timeout for the smoke handshake, default 30
#   --path <dir>       override /home/<user>/... style path arguments
#   --project <name>   replaces <project> in the catalogue's paths
#   --dry-run          print the resolved command and start nothing
#   -h | --help        this text
#
# Configuration: repository-root .env first, then .env.example for the keys .env
# leaves out; an already exported variable beats both, a flag beats everything.
# The files are parsed, not sourced. Placeholders in the catalogue are expanded:
#   <user>     -> current username
#   <project>  -> --project, else the current directory's name
#   <app_id> / <app_secret> -> LARK_APP_ID / LARK_APP_SECRET
# Anything still unresolved is a hard error: no placeholder is ever executed.
#
# Credentials: metadata.env_hint names the variables the server needs. Export
# them before starting; the process inherits them. This script never writes a
# value to a generated file, the pid file or the log, and never echoes one —
# the resolved command line is printed with the app secret masked. The Feishu
# server takes -a/-s as argv, so that one value is visible in `ps`; prefer an
# env-driven server whenever the upstream offers one.
#
# This script never talks to agenthub: no login, no load.sh, no /probe. The
# server-side MCP connector is still TODO(real), so this stands the stdio
# process up to verify its command line and tool list locally.
#
# --- end of usage ---

set -euo pipefail

SELF="$0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RES_DIR="$SCRIPT_DIR/resources"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

SERVERS="filesystem github google-drive google-docs gmail slack stripe wechat dingtalk feishu"
COMMANDS="start stop restart status smoke"

SERVER=""
COMMAND=""
WORKDIR=""
WAIT="30"
PATH_ARG=""
PROJECT_ARG=""
DRY_RUN=0

print_help() {
  sed -n '2,/^# --- end of usage ---$/p' "$SELF" |
    sed -e 's/^# \{0,1\}//' -e '/^--- end of usage ---$/d'
}

usage_error() {
  echo "$1" >&2
  echo "supported servers: $SERVERS" >&2
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
    --workdir) WORKDIR="${2:-}"; shift ;;
    --wait) WAIT="${2:-}"; shift ;;
    --path) PATH_ARG="${2:-}"; shift ;;
    --project) PROJECT_ARG="${2:-}"; shift ;;
    --dry-run) DRY_RUN=1 ;;
    start | stop | restart | status | smoke) COMMAND="$1" ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *)
      if [ -n "$SERVER" ]; then
        echo "unexpected argument: $1 (server is already '$SERVER')" >&2
        exit 2
      fi
      SERVER="$1"
      ;;
  esac
  shift
done

[ -n "$SERVER" ] || usage_error "no server given"
case " $SERVERS " in
  *" $SERVER "*) ;;
  *) usage_error "unsupported server: $SERVER" ;;
esac
COMMAND="${COMMAND:-start}"
case " $COMMANDS " in
  *" $COMMAND "*) ;;
  *) usage_error "unsupported command: $COMMAND" ;;
esac
case "$WAIT" in
  '' | *[!0-9]*) echo "--wait must be a number of seconds, got: $WAIT" >&2; exit 2 ;;
esac

load_env_file "$ROOT_DIR/.env"
load_env_file "$ROOT_DIR/.env.example"

# ---- catalogue spec ----------------------------------------------------------

RES_FILE="$RES_DIR/mcp-$SERVER-ubuntu.json"
[ -f "$RES_FILE" ] || {
  echo "missing catalogue entry: $RES_FILE" >&2
  exit 2
}
require python3

# The catalogue is the single source of truth for the command line. Args are
# emitted one per line so spaces survive the trip into bash without eval.
json_spec() {
  python3 - "$RES_FILE" <<'PY' 2>/dev/null || true
import json, sys

with open(sys.argv[1]) as fh:
    doc = json.load(fh)
mcp = (doc.get("kind_config") or {}).get("mcp") or {}
md = doc.get("metadata") or {}


def emit(key, value):
    print("%s=%s" % (key, str(value or "").replace("\n", " ")))


emit("ID", doc.get("id"))
emit("ENDPOINT", doc.get("endpoint"))
emit("TRANSPORT", mcp.get("transport") or doc.get("transport"))
emit("COMMAND", mcp.get("command"))
emit("ENV_HINT", md.get("env_hint"))
emit("INSTALL", md.get("install"))
emit("PATH_HINT", md.get("path_hint"))
emit("TOOLS", ",".join(mcp.get("tool_allowlist") or []))
for arg in mcp.get("args") or []:
    print("__ARG__=" + str(arg).replace("\n", " "))
PY
}

SPEC="$(json_spec)"
spec() {
  printf '%s\n' "$SPEC" | sed -n "s/^$1=//p" | head -n 1 || true
}

mapfile -t RAW_ARGS < <(printf '%s\n' "$SPEC" | sed -n 's/^__ARG__=//p')

USER_NAME="$(id -un)"
PROJECT="${PROJECT_ARG:-$(basename "$PWD")}"

expand() {
  local value="$1"
  value="${value//<user>/$USER_NAME}"
  value="${value//<project>/$PROJECT}"
  # Only substitute a credential when it is actually present. Replacing it with
  # an empty string would slip past assert_resolved and launch `-a '' -s ''`.
  if [ -n "${LARK_APP_ID:-}" ]; then
    value="${value//<app_id>/$LARK_APP_ID}"
  fi
  if [ -n "${LARK_APP_SECRET:-}" ]; then
    value="${value//<app_secret>/$LARK_APP_SECRET}"
  fi
  printf '%s' "$value"
}

assert_resolved() {
  case "$2" in
    *'<'*'>'*)
      echo "unresolved placeholder in $1: $2" >&2
      echo "pass --path / --project, or export LARK_APP_ID / LARK_APP_SECRET" >&2
      exit 2
      ;;
  esac
}

CMD="$(expand "$(spec COMMAND)")"
assert_resolved "command" "$CMD"
[ -n "$CMD" ] || {
  echo "$RES_FILE declares no kind_config.mcp.command" >&2
  exit 2
}

ARGS=()
for i in "${!RAW_ARGS[@]}"; do
  arg="$(expand "${RAW_ARGS[$i]}")"
  assert_resolved "argv[$i]" "$arg"
  ARGS+=("$arg")
done

# --path only replaces path-looking arguments (the filesystem server's root);
# a binary path in `command` is never rewritten.
if [ -n "$PATH_ARG" ]; then
  for i in "${!ARGS[@]}"; do
    case "${ARGS[$i]}" in
      /home/* | file://*) ARGS[$i]="$PATH_ARG" ;;
    esac
  done
fi

RES_ID="$(spec ID)"
ENDPOINT="$(spec ENDPOINT)"
TRANSPORT="$(spec TRANSPORT)"
ENV_HINT="$(spec ENV_HINT)"
INSTALL_HINT="$(spec INSTALL)"
TOOLS="$(spec TOOLS)"

WORKDIR="${WORKDIR:-${XDG_DATA_HOME:-${HOME:-/tmp}/.local/share}/agenthub-mcp-$SERVER}"
FIFO="$WORKDIR/stdin"
PID_FILE="$WORKDIR/$SERVER.pid"
KEEP_PID_FILE="$WORKDIR/$SERVER.keep.pid"
LOG_FILE="$WORKDIR/$SERVER.log"

# ---- helpers -----------------------------------------------------------------

pid_alive() {
  [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null
}

resolve_command() {
  case "$CMD" in
    */*) [ -x "$CMD" ] && return 0 ;;
    *) command -v "$CMD" >/dev/null 2>&1 && return 0 ;;
  esac
  echo "command is not available: $CMD" >&2
  if [ -n "$INSTALL_HINT" ]; then
    echo "install hint: $INSTALL_HINT" >&2
  fi
  case "$CMD" in
    npx) echo "npx needs Node.js: https://nodejs.org/" >&2 ;;
    uvx) echo "uvx needs uv: curl -LsSf https://astral.sh/uv/install.sh | sh" >&2 ;;
    docker) echo "docker needs Docker Engine: https://docs.docker.com/engine/install/" >&2 ;;
  esac
  return 1
}

# The catalogue command line can carry an app secret (Feishu uses -a/-s). Never
# print that value: mask the exact argv element before %q quotes the rest.
show_command() {
  local arg
  local -a shown=()
  for arg in "${ARGS[@]}"; do
    if [ -n "${LARK_APP_SECRET:-}" ]; then
      case "$arg" in
        *"$LARK_APP_SECRET"*) arg="${arg//"$LARK_APP_SECRET"/REDACTED}" ;;
      esac
    fi
    shown+=("$arg")
  done
  printf 'command:'
  printf ' %q' "$CMD" "${shown[@]}"
  printf '\n'
}

show_plan() {
  show_command
  echo "id:        $RES_ID"
  echo "transport: ${TRANSPORT:-stdio}  endpoint: ${ENDPOINT:-<none>}"
  echo "tools:     ${TOOLS:-<none>}"
  echo "workdir:   $WORKDIR"
  if [ -n "$ENV_HINT" ]; then
    echo "env hint:  $ENV_HINT (export these; values are never written to disk)" >&2
  fi
}

# ---- commands ----------------------------------------------------------------

start_server() {
  if [ "$DRY_RUN" = "1" ]; then
    show_plan
    return 0
  fi
  require mkfifo
  resolve_command
  if pid_alive; then
    echo "$SERVER is already running (pid $(cat "$PID_FILE")) — log: $LOG_FILE"
    return 0
  fi

  mkdir -p "$WORKDIR"
  rm -f "$FIFO"
  mkfifo "$FIFO"
  # A stdio MCP server exits the moment stdin sees EOF. Hold the write end open
  # with a sleepy writer, so `smoke` can append JSON-RPC lines to the same FIFO
  # without ever terminating the server.
  nohup tail -f /dev/null >"$FIFO" 2>/dev/null &
  echo $! >"$KEEP_PID_FILE"
  : >"$LOG_FILE"
  # Most servers take credentials from the inherited environment. Feishu's
  # catalogue declares -a/-s argv, so that one value is on the command line;
  # warn instead of pretending otherwise, and never echo it.
  if [ -n "${LARK_APP_SECRET:-}" ]; then
    echo "warning: $SERVER takes the app secret as an argv element; it is visible in 'ps'" >&2
  fi
  nohup "$CMD" "${ARGS[@]}" <"$FIFO" >>"$LOG_FILE" 2>&1 &
  echo $! >"$PID_FILE"
  sleep 0.3
  if ! pid_alive; then
    echo "$SERVER exited immediately; see $LOG_FILE" >&2
    tail -n 20 "$LOG_FILE" >&2
    rm -f "$PID_FILE"
    return 1
  fi
  echo "$SERVER started (pid $(cat "$PID_FILE")) — log: $LOG_FILE"
  echo "verify the handshake with: $SELF $SERVER smoke"
}

stop_server() {
  local pid waited=0
  if [ -f "$PID_FILE" ]; then
    pid="$(cat "$PID_FILE")"
    if kill -0 "$pid" 2>/dev/null; then
      kill -TERM "$pid" 2>/dev/null || true
      while [ "$waited" -lt 50 ] && kill -0 "$pid" 2>/dev/null; do
        sleep 0.2
        waited=$((waited + 1))
      done
      if kill -0 "$pid" 2>/dev/null; then
        echo "pid $pid ignored TERM, sending KILL"
        kill -KILL "$pid" 2>/dev/null || true
      fi
      echo "stopped $SERVER (pid $pid)"
    else
      echo "$SERVER is not running (stale pid $pid)"
    fi
    rm -f "$PID_FILE"
  else
    echo "$SERVER is not running (no pid file in $WORKDIR)"
  fi
  if [ -f "$KEEP_PID_FILE" ]; then
    kill "$(cat "$KEEP_PID_FILE")" 2>/dev/null || true
    rm -f "$KEEP_PID_FILE"
  fi
  rm -f "$FIFO"
}

status_server() {
  local pid="-"
  if [ -f "$PID_FILE" ]; then
    pid="$(cat "$PID_FILE")"
  fi
  if pid_alive; then
    echo "running       pid $pid"
  else
    echo "stopped       pid file: $pid"
  fi
  show_plan
  if [ -f "$LOG_FILE" ]; then
    echo "log           $LOG_FILE"
    tail -n 10 "$LOG_FILE"
  fi
  pid_alive
}

response_seen() {
  python3 - "$LOG_FILE" <<'PY'
import json, sys

try:
    with open(sys.argv[1]) as fh:
        lines = fh.read().splitlines()
except OSError:
    sys.exit(1)
for line in lines:
    try:
        doc = json.loads(line)
    except ValueError:
        continue
    if doc.get("id") == 2 and ("result" in doc or "error" in doc):
        sys.exit(0)
sys.exit(1)
PY
}

report_smoke() {
  python3 - "$LOG_FILE" <<'PY'
import json, sys

docs = []
with open(sys.argv[1]) as fh:
    for line in fh:
        try:
            docs.append(json.loads(line))
        except ValueError:
            continue
for doc in docs:
    if doc.get("id") == 1:
        result = doc.get("result") or {}
        info = result.get("serverInfo") or {}
        print("  initialize    protocol=%s server=%s" % (
            result.get("protocolVersion") or "-", info.get("name") or "-"))
    if doc.get("id") == 2:
        if "error" in doc:
            err = doc["error"]
            print("  tools/list    error %s: %s" % (err.get("code"), err.get("message")))
            sys.exit(1)
        tools = (doc.get("result") or {}).get("tools") or []
        print("  tools/list    %d tool(s)" % len(tools))
        for tool in tools[:20]:
            print("    - %s" % (tool.get("name") or "?"))
        sys.exit(0 if tools else 1)
print("  ! no tools/list response in the log")
sys.exit(1)
PY
}

smoke_server() {
  local deadline
  if ! pid_alive; then
    echo "$SERVER is not running; start it first: $SELF $SERVER start" >&2
    return 1
  fi
  if [ ! -p "$FIFO" ]; then
    echo "missing stdin FIFO: $FIFO" >&2
    return 1
  fi

  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"agenthub-example","version":"1.0.0"}}}' >"$FIFO"
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}' >"$FIFO"
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' >"$FIFO"

  deadline=$(( $(date +%s) + WAIT ))
  until response_seen; do
    if ! pid_alive; then
      echo "the server exited during the handshake" >&2
      tail -n 20 "$LOG_FILE" >&2
      return 1
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "timed out after ${WAIT}s waiting for tools/list" >&2
      if [ -n "$ENV_HINT" ]; then
        echo "a server waiting on credentials or OAuth stays silent: $ENV_HINT" >&2
      fi
      tail -n 20 "$LOG_FILE" >&2
      return 1
    fi
    sleep 1
  done
  report_smoke
}

case "$COMMAND" in
  start) start_server ;;
  stop) stop_server ;;
  restart) stop_server; start_server ;;
  status) status_server ;;
  smoke) smoke_server ;;
esac
