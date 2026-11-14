#!/usr/bin/env bash
# deploy-mcp.sh — serve one of the example MCP servers over Streamable HTTP on
# WSL2 / Ubuntu.
#
# Usage:
#   ./deploy-mcp.sh <server> [command] [options]
#
# Servers (from resources/mcp-<server>-ubuntu.json):
#   filesystem github google-drive google-docs gmail slack stripe wechat dingtalk feishu
#
# Commands (default: start):
#   start    resolve the catalogue's stdio command, wrap it in a stdio →
#            Streamable HTTP bridge (npx supergateway, stateless), and serve it
#            in the background at http://<host>:<port>/mcp
#   stop     TERM the bridge's whole process group (KILL when it ignores TERM)
#   restart  stop, then start
#   status   pid, the resolved stdio command, the bridge command, the HTTP
#            endpoint, the tail of the log and a hint when the last start failed
#            with an npm ENOENT
#   smoke    POST MCP initialize + tools/list to the HTTP endpoint and print tools
#
# Options:
#   --workdir <dir>    default $HOME/.local/share/agenthub-mcp-<server>
#   --host <host>      listen address, default AGENTHUB_EXAMPLE_MCP_HOST, else 127.0.0.1
#   --port <port>      listen port, default AGENTHUB_EXAMPLE_MCP_PORT, else 8002
#                      (the ADK example owns 8000 / 8001); `auto` scans upward
#                      from 8002 for a free port, so several servers can share a host
#   --http-path <p>    Streamable HTTP path on the bridge, default /mcp; set a
#                      distinct path per server when a reverse proxy routes by path
#   --wait <sec>       startup and smoke handshake timeout, default 30
#   --path <dir>       override /home/<user>/... style path arguments (must exist)
#   --project <name>   replaces <project> in the catalogue's paths
#   --dry-run          print the resolved plan and start nothing
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
# Transport: the catalogue still declares stdio — that is what these upstream
# servers speak, and this script does not rewrite the JSON. It wraps the very
# same command with supergateway (`--outputTransport streamableHttp`), which
# starts the stdio child per request and answers POST /mcp with Streamable HTTP
# JSON-RPC. Stateless mode is deliberate: agenthub's mcp connector POSTs
# tools/list directly, with no initialize and no Mcp-Session-Id. The upstream
# server's own "running on stdio" line is still in the log because that is how
# the bridge talks to it; the deployed endpoint is the HTTP one shown by status.
# Command lines in `status` / `--dry-run` are shell-quoted: the escaped single
# quotes around --stdio are one argument, so the printed line can be copied.
#
# Security: the default bind is loopback only. Exposing another interface needs
# an authenticating reverse proxy: the bridge has no client authentication.
#
# Credentials: metadata.env_hint names the variables the server needs. Export
# them before starting; the bridge process inherits them and passes them on.
# This script never writes a value to a generated file, the pid file or the log,
# and never echoes one — the resolved command lines are printed with the app
# secret masked. The Feishu server takes -a/-s as argv, so that one value is
# visible in `ps`; prefer an env-driven server whenever the upstream offers one.
#
# npm / npx on WSL2: when ~/.npmrc sets `prefix=<dir>` and that directory was
# never created, npx dies with "ENOENT ... lstat <dir>" before it can fetch the
# package. `start` creates the missing prefix (and says so), and `status` shows
# the same one-line fix when that failure is already in the log.
#
# This script never talks to agenthub: no login, no load.sh, no /probe. It
# stands up a local Streamable HTTP endpoint to verify the command line and the
# tool list that an MCP client (or an agenthub mcp record pointing at that URL)
# would see.
#
# --- end of usage ---

set -euo pipefail

SELF="$0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RES_DIR="$SCRIPT_DIR/resources"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

SERVERS="filesystem github google-drive google-docs gmail slack stripe wechat dingtalk feishu"
COMMANDS="start stop restart status smoke"

# The bridge is deliberately stateless: see the Transport note above.
GATEWAY_CMD="npx"
GATEWAY_PKG="supergateway@latest"
HTTP_PATH="/mcp"

SERVER=""
COMMAND=""
WORKDIR=""
WAIT="30"
PATH_ARG=""
PROJECT_ARG=""
HOST_ARG=""
PORT_ARG=""
HTTP_PATH_ARG=""
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

# Scan upward from 8002 for a loopback TCP port that nothing is listening on and
# that bind()s cleanly; the chosen port is echoed. Used by `--port auto` so several
# bridges can share one host without a clash. (The ADK example owns 8000 / 8001.)
find_free_port() {
  local p=8002
  while :; do
    if python3 - "$p" <<'PY' 2>/dev/null; then
import socket, sys
s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
try:
    s.bind(("127.0.0.1", int(sys.argv[1])))
    s.close()
    sys.exit(0)
except OSError:
    sys.exit(1)
PY
      echo "$p"
      return 0
    fi
    p=$((p + 1))
    [ "$p" -gt 65535 ] && { echo "no free port found from 8002" >&2; return 1; }
  done
}

# ---- arguments ---------------------------------------------------------------

while [ $# -gt 0 ]; do
  case "$1" in
    -h | --help) print_help; exit 0 ;;
    --workdir) WORKDIR="${2:-}"; shift ;;
    --wait) WAIT="${2:-}"; shift ;;
    --host) HOST_ARG="${2:-}"; shift ;;
    --port) PORT_ARG="${2:-}"; shift ;;
    --http-path) HTTP_PATH_ARG="${2:-}"; shift ;;
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

# Workdir and the files it holds (pid, group-leader marker, log, and the port a
# previous `--port auto` start resolved) are derived from the server name, so
# several MCP bridges can run side by side, each fully isolated.
WORKDIR="${WORKDIR:-${XDG_DATA_HOME:-${HOME:-/tmp}/.local/share}/agenthub-mcp-$SERVER}"
PID_FILE="$WORKDIR/$SERVER.pid"
PGID_FILE="$WORKDIR/$SERVER.pgid"
LOG_FILE="$WORKDIR/$SERVER.log"
PORT_FILE="$WORKDIR/$SERVER.port"
HTTP_PATH_FILE="$WORKDIR/$SERVER.path"
# Files a pre-Streamable-HTTP (FIFO/stdio) deployment may have left behind.
FIFO="$WORKDIR/stdin"
KEEP_PID_FILE="$WORKDIR/$SERVER.keep.pid"

HOST="${HOST_ARG:-${AGENTHUB_EXAMPLE_MCP_HOST:-127.0.0.1}}"
HTTP_PATH="${HTTP_PATH_ARG:-/mcp}"

# Like the port, a non-default --http-path is recorded at start so later
# `status`/`smoke`/`stop` without the flag still describe the right endpoint.
if [ -z "$HTTP_PATH_ARG" ] && [ -f "$HTTP_PATH_FILE" ]; then
  SAVED="$(cat "$HTTP_PATH_FILE" 2>/dev/null || true)"
  case "$SAVED" in
    /*) HTTP_PATH="$SAVED" ;;
  esac
fi

# --port auto scans upward from 8002 for a free loopback port; an explicit port is
# used as given. When no --port is passed, a port recorded by a previous start
# (PORT_FILE) wins, so `status`/`stop` without --port still name the right endpoint.
if [ "$PORT_ARG" = "auto" ]; then
  PORT="$(find_free_port)" || exit 1
else
  PORT="${PORT_ARG:-${AGENTHUB_EXAMPLE_MCP_PORT:-8002}}"
fi
case "$PORT" in
  '' | *[!0-9]*) echo "--port must be a number or 'auto', got: $PORT" >&2; exit 2 ;;
esac
PORT="$((10#$PORT))"
if [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65535 ]; then
  echo "--port must be between 1 and 65535, got: $PORT" >&2
  exit 2
fi
if [ -z "$PORT_ARG" ] && [ -f "$PORT_FILE" ]; then
  RECORDED="$(cat "$PORT_FILE" 2>/dev/null || true)"
  case "$RECORDED" in '' | *[!0-9]*) ;; *) PORT="$RECORDED" ;; esac
fi
case "$HOST" in
  '' | *[!A-Za-z0-9.:_-]*)
    echo "--host must be a hostname or IP address, got: $HOST" >&2
    exit 2
    ;;
esac
case "$HOST" in
  *:*) HOST_URL="[$HOST]" ;;
  *) HOST_URL="$HOST" ;;
esac
MCP_URL="http://$HOST_URL:$PORT$HTTP_PATH"

# ---- catalogue spec ----------------------------------------------------------

RES_FILE="$RES_DIR/mcp-$SERVER-ubuntu.json"
[ -f "$RES_FILE" ] || {
  echo "missing catalogue entry: $RES_FILE" >&2
  exit 2
}
require python3

# The catalogue is the single source of truth for the wrapped command line. Args
# are emitted one per line so spaces survive the trip into bash without eval.
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

# ---- helpers -----------------------------------------------------------------

pid_alive() {
  [ -f "$PID_FILE" ] && kill -0 "$(cat "$PID_FILE")" 2>/dev/null
}

# supergateway takes the wrapped command as ONE shell string. Quote every argv
# element with POSIX single quotes so paths with spaces, quotes or `$` survive
# /bin/sh unchanged; never use eval, and never let the shell expand a credential.
shell_quote() {
  printf "'%s'" "${1//\'/\'\\\'\'}"
}

stdio_command() {
  local part out=""
  for part in "$@"; do
    out="${out:+$out }$(shell_quote "$part")"
  done
  printf '%s' "$out"
}

mask_secret() {
  local value="$1"
  if [ -n "${LARK_APP_SECRET:-}" ]; then
    case "$value" in
      *"$LARK_APP_SECRET"*) value="${value//"$LARK_APP_SECRET"/REDACTED}" ;;
    esac
  fi
  printf '%s' "$value"
}

# The bridge is started with setsid: npx, supergateway and the stdio server all
# live in one process group, so `stop` and a failed start can take the whole
# tree down instead of leaving an orphaned upstream server behind. A pid file
# left by the old FIFO deployment names a plain process instead, so the group
# signal is only used when this script recorded the group leader.
terminate_group() {
  local pid="$1" sig="$2"
  if [ -f "$PGID_FILE" ] && [ "$(cat "$PGID_FILE" 2>/dev/null || true)" = "$pid" ]; then
    kill -s "$sig" -- "-$pid" 2>/dev/null || kill -"$sig" "$pid" 2>/dev/null || true
  else
    kill -"$sig" "$pid" 2>/dev/null || true
  fi
}

# Older versions of this script held a stdio server open through a FIFO and a
# sleepy `tail -f` writer. Closing those too makes `restart` a clean handover.
release_legacy_fifo() {
  if [ -f "$KEEP_PID_FILE" ]; then
    kill "$(cat "$KEEP_PID_FILE")" 2>/dev/null || true
    rm -f "$KEEP_PID_FILE"
  fi
  rm -f "$FIFO"
}

cleanup_failed_start() {
  local pid
  if [ -s "$PID_FILE" ]; then
    pid="$(cat "$PID_FILE")"
    terminate_group "$pid" TERM
  fi
  rm -f "$PID_FILE" "$PGID_FILE" "$PORT_FILE" "$HTTP_PATH_FILE"
  release_legacy_fifo
}

# port_open: something is already listening on the deployed host/port. Used to
# refuse a colliding start, to wait for readiness and to report status.
port_open() {
  python3 - "$HOST" "$PORT" <<'PY'
import socket, sys

host, port = sys.argv[1], int(sys.argv[2])
if host in ("", "0.0.0.0", "::"):
    host = "127.0.0.1"
try:
    with socket.create_connection((host, port), timeout=0.5):
        sys.exit(0)
except OSError:
    sys.exit(1)
PY
}

wait_for_endpoint() {
  local deadline=$(( $(date +%s) + WAIT )) pid
  while :; do
    pid="$(cat "$PID_FILE" 2>/dev/null || true)"
    if [ -z "$pid" ] || ! kill -0 "$pid" 2>/dev/null; then
      echo "$SERVER bridge exited during startup; see $LOG_FILE" >&2
      return 1
    fi
    if grep -q 'npm error code' "$LOG_FILE" 2>/dev/null; then
      echo "$SERVER reported an npm error during startup; see $LOG_FILE" >&2
      return 1
    fi
    if port_open; then
      return 0
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "timed out after ${WAIT}s waiting for $MCP_URL" >&2
      echo "npx may still be downloading $GATEWAY_PKG; retry with a larger --wait" >&2
      return 1
    fi
    sleep 0.5
  done
}

# npm/npx read `prefix` from ~/.npmrc (or NPM_CONFIG_PREFIX). A prefix that was
# configured but never created makes npx abort with ENOENT on lstat, before any
# package is fetched — a stock WSL2 setup step. Repair it once, here, for both
# the bridge and the wrapped catalogue command.
ensure_npm_prefix_for() {
  local cmd="$1" prefix
  case "$cmd" in
    npx | npm | */npx | */npm) ;;
    *) return 0 ;;
  esac
  command -v npm >/dev/null 2>&1 || return 0
  prefix="${NPM_CONFIG_PREFIX:-}"
  if [ -z "$prefix" ]; then
    prefix="$(npm config get prefix 2>/dev/null || true)"
  fi
  prefix="${prefix%$'\r'}"
  [ -n "$prefix" ] || return 0
  [ -d "$prefix" ] && return 0
  case "$prefix" in
    /*) ;;
    *)
      echo "npm prefix is not an absolute path: $prefix" >&2
      return 1
      ;;
  esac
  if mkdir -p "$prefix" 2>/dev/null; then
    echo "created missing npm global prefix: $prefix" >&2
    return 0
  fi
  echo "npm prefix $prefix does not exist and cannot be created here; run:" >&2
  echo "  mkdir -p $prefix" >&2
  return 1
}

ensure_npm_prefix() {
  ensure_npm_prefix_for "$GATEWAY_CMD" || return 1
  ensure_npm_prefix_for "$CMD"
}

# The filesystem server resolves its root directory before serving anything; a
# missing one produces a stack trace that looks like another npm failure.
check_path_args() {
  local arg
  for arg in "${ARGS[@]}"; do
    case "$arg" in
      /*)
        if [ ! -d "$arg" ]; then
          echo "the catalogue points at a directory that does not exist: $arg" >&2
          echo "create it with: mkdir -p $arg" >&2
          echo "or pass --path <existing directory>" >&2
          return 1
        fi
        ;;
    esac
  done
}

# One actionable line for the failure this script sees most often on WSL2.
log_hint() {
  local path
  [ -f "$LOG_FILE" ] || return 0
  if grep -q 'npm error code ENOENT' "$LOG_FILE" 2>/dev/null; then
    path="$(sed -n 's/^npm error path //p' "$LOG_FILE" | head -n 1 || true)"
    echo "npm failed with ENOENT: the global prefix in ~/.npmrc points at a missing directory" >&2
    if [ -n "$path" ]; then
      echo "fix: mkdir -p $path" >&2
    else
      echo 'fix: mkdir -p "$(npm config get prefix)"' >&2
    fi
  fi
}

resolve_command_for() {
  local cmd="$1" hint="$2"
  case "$cmd" in
    */*) [ -x "$cmd" ] && return 0 ;;
    *) command -v "$cmd" >/dev/null 2>&1 && return 0 ;;
  esac
  echo "command is not available: $cmd" >&2
  if [ -n "$hint" ]; then
    echo "install hint: $hint" >&2
  fi
  case "$cmd" in
    npx) echo "npx needs Node.js: https://nodejs.org/" >&2 ;;
    uvx) echo "uvx needs uv: curl -LsSf https://astral.sh/uv/install.sh | sh" >&2 ;;
    docker) echo "docker needs Docker Engine: https://docs.docker.com/engine/install/" >&2 ;;
  esac
  return 1
}

resolve_command() {
  resolve_command_for "$CMD" "$INSTALL_HINT" || return 1
  resolve_command_for "$GATEWAY_CMD" ""
}

# The catalogue command line can carry an app secret (Feishu uses -a/-s). Never
# print that value: mask the exact argv element before %q quotes the rest.
show_command() {
  local arg
  local -a shown=()
  for arg in "${ARGS[@]}"; do
    arg="$(mask_secret "$arg")"
    shown+=("$arg")
  done
  printf 'command:'
  printf ' %q' "$CMD" "${shown[@]}"
  printf '\n'
}

show_bridge() {
  local stdio
  stdio="$(mask_secret "$(stdio_command "$CMD" "${ARGS[@]}")")"
  printf 'bridge:  '
  printf ' %q' "$GATEWAY_CMD" -y "$GATEWAY_PKG" --stdio "$stdio" \
    --outputTransport streamableHttp --host "$HOST" --port "$PORT" \
    --streamableHttpPath "$HTTP_PATH"
  printf '\n'
}

show_plan() {
  show_command
  show_bridge
  echo "id:        $RES_ID"
  echo "transport: streamable-http  endpoint: $MCP_URL"
  echo "upstream:  ${TRANSPORT:-stdio} (catalogue: ${ENDPOINT:-<none>})"
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
  require python3
  if ! command -v setsid >/dev/null 2>&1; then
    echo "missing dependency: setsid (install it with: sudo apt install -y util-linux)" >&2
    return 1
  fi
  resolve_command || return 1
  ensure_npm_prefix || return 1
  check_path_args || return 1
  if [ "$HOST" != "127.0.0.1" ] && [ "$HOST" != "localhost" ] && [ "$HOST" != "::1" ]; then
    echo "warning: $HOST is reachable from the network and the bridge has no client auth;" >&2
    echo "         put it behind an authenticating reverse proxy" >&2
  fi
  if pid_alive; then
    if port_open; then
      echo "$SERVER is already running (pid $(cat "$PID_FILE")) — endpoint: $MCP_URL"
      return 0
    fi
    echo "$SERVER has a live pid $(cat "$PID_FILE"), but $MCP_URL is not listening" >&2
    echo "this looks like a leftover FIFO/stdio deployment; run: $SELF $SERVER restart" >&2
    return 1
  fi
  if port_open; then
    echo "port $PORT is already in use and no $PID_FILE of this script owns it;" >&2
    echo "refusing to touch it. Find the owner with: ss -ltnp 'sport = :$PORT'" >&2
    echo "or pick another port with --port <port>" >&2
    return 1
  fi

  # A previous failed start may have left a pid file and a live process group.
  cleanup_failed_start

  mkdir -p "$WORKDIR"
  : >"$LOG_FILE"
  # Most servers take credentials from the inherited environment. Feishu's
  # catalogue declares -a/-s argv, so that one value is on the command line;
  # warn instead of pretending otherwise, and never echo it.
  if [ -n "${LARK_APP_SECRET:-}" ]; then
    echo "warning: $SERVER takes the app secret as an argv element; it is visible in 'ps'" >&2
  fi

  local stdio
  local -a bridge
  stdio="$(stdio_command "$CMD" "${ARGS[@]}")"
  bridge=("$GATEWAY_CMD" -y "$GATEWAY_PKG" --stdio "$stdio" \
    --outputTransport streamableHttp --host "$HOST" --port "$PORT" \
    --streamableHttpPath "$HTTP_PATH")

  # The wrapper records its own pid (== the new process group id) before exec,
  # so the pid file names supergateway rather than a short-lived setsid parent.
  # stdin is /dev/null: supergateway 4.x exits when a stdin *pipe* closes, but
  # keeps serving when stdin is ignored or /dev/null.
  setsid nohup bash -c '
    printf "%s\n" "$$" >"$1"
    shift
    exec "$@"
  ' _ "$PID_FILE" "${bridge[@]}" </dev/null >>"$LOG_FILE" 2>&1 &

  local waited=0
  while [ ! -s "$PID_FILE" ] && [ "$waited" -lt 40 ]; do
    sleep 0.1
    waited=$((waited + 1))
  done
  # From here on the pid is known to be the process group leader (setsid), so
  # stop/cleanup may signal the whole bridge tree — including a failed startup.
  if [ -s "$PID_FILE" ]; then
    cp "$PID_FILE" "$PGID_FILE"
  fi
  if ! pid_alive; then
    echo "$SERVER bridge exited during startup; see $LOG_FILE" >&2
    tail -n 20 "$LOG_FILE" >&2
    log_hint
    cleanup_failed_start
    return 1
  fi
  # npx resolves and downloads supergateway before the port opens. Watch the
  # pid and the log until the endpoint listens; on failure clean up completely
  # instead of leaving a stale pid, a half-started bridge or an orphaned child.
  if ! wait_for_endpoint; then
    tail -n 20 "$LOG_FILE" >&2
    log_hint
    cleanup_failed_start
    return 1
  fi
  echo "$PORT" > "$PORT_FILE"
  echo "$HTTP_PATH" > "$HTTP_PATH_FILE"
  echo "$SERVER started (pid $(cat "$PID_FILE")) — endpoint: $MCP_URL — log: $LOG_FILE"
  echo "verify the handshake with: $SELF $SERVER smoke"
}

stop_server() {
  local pid waited=0
  if [ -f "$PID_FILE" ]; then
    pid="$(cat "$PID_FILE")"
    if kill -0 "$pid" 2>/dev/null; then
      terminate_group "$pid" TERM
      while [ "$waited" -lt 50 ] && kill -0 "$pid" 2>/dev/null; do
        sleep 0.2
        waited=$((waited + 1))
      done
      if kill -0 "$pid" 2>/dev/null; then
        echo "pid $pid ignored TERM, sending KILL"
        terminate_group "$pid" KILL
      fi
      echo "stopped $SERVER (pid $pid)"
    else
      echo "$SERVER is not running (stale pid $pid)"
    fi
    rm -f "$PID_FILE" "$PGID_FILE" "$PORT_FILE" "$HTTP_PATH_FILE"
  else
    echo "$SERVER is not running (no pid file in $WORKDIR)"
  fi
  release_legacy_fifo
}

status_server() {
  local pid="-"
  if [ -f "$PID_FILE" ]; then
    pid="$(cat "$PID_FILE")"
  fi
  if pid_alive; then
    echo "running       pid $pid"
  elif [ -f "$PID_FILE" ]; then
    echo "stopped       stale pid $pid — clean up with: $SELF $SERVER stop"
  else
    echo "stopped       no pid file"
  fi
  show_plan
  if port_open; then
    echo "listen        $MCP_URL"
  else
    echo "listen        $MCP_URL (not accepting connections)"
    if pid_alive; then
      echo "warning:      a live pid without an HTTP endpoint looks like the old" >&2
      echo "              FIFO/stdio deployment; run: $SELF $SERVER restart" >&2
    fi
  fi
  if [ -f "$LOG_FILE" ]; then
    echo "log           $LOG_FILE"
    tail -n 10 "$LOG_FILE"
    log_hint
  fi
  pid_alive
}

smoke_server() {
  if ! pid_alive; then
    echo "$SERVER is not running; start it first: $SELF $SERVER start" >&2
    return 1
  fi
  if ! port_open; then
    echo "$MCP_URL is not accepting connections; see $LOG_FILE" >&2
    return 1
  fi
  echo "POST          $MCP_URL"
  if ! python3 - "$MCP_URL" "$WAIT" "$ENV_HINT" <<'PY'
import json, sys, time
import urllib.error
import urllib.request

url, wait, env_hint = sys.argv[1], float(sys.argv[2]), sys.argv[3]
deadline = time.time() + wait

INIT = {
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
        "protocolVersion": "2024-11-05",
        "capabilities": {},
        "clientInfo": {"name": "agenthub-example", "version": "1.0.0"},
    },
}


def post(payload, session=None, timeout=5):
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json, text/event-stream",
    }
    if session:
        headers["Mcp-Session-Id"] = session
    req = urllib.request.Request(
        url, data=json.dumps(payload).encode("utf-8"), headers=headers, method="POST"
    )
    try:
        resp = urllib.request.urlopen(req, timeout=timeout)
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", "replace")[:200]
        raise RuntimeError("HTTP %d: %s" % (exc.code, body))
    with resp:
        if "text/event-stream" in resp.headers.get("Content-Type", ""):
            doc = None
            for raw in resp:
                line = raw.decode("utf-8", "replace").strip()
                if not line.startswith("data:"):
                    continue
                try:
                    doc = json.loads(line[5:].strip())
                except ValueError:
                    continue
                if isinstance(doc, dict) and ("result" in doc or "error" in doc):
                    break
            return doc, resp.headers
        body = resp.read()
        if not body:
            # 202 Accepted is the normal answer to a notification.
            return None, resp.headers
        try:
            return json.loads(body), resp.headers
        except ValueError:
            raise RuntimeError("response was not JSON: %s" % body[:200].decode("utf-8", "replace"))


last = ""
while True:
    remaining = deadline - time.time()
    if remaining <= 0:
        break
    timeout = max(1.0, min(5.0, remaining))
    try:
        init, headers = post(INIT, timeout=timeout)
        session = headers.get("Mcp-Session-Id")
        try:
            post(
                {"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}},
                session,
                timeout=timeout,
            )
        except Exception:
            # A stateless bridge answers 202 to the notification; there is no
            # session to keep alive. A refusal here must not hide tools/list.
            pass
        # Deliberately no initialize/session coupling on this one: agenthub's
        # mcp connector POSTs tools/list straight to the URL, and a stateful
        # bridge would reject it.
        doc, _ = post(
            {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}},
            None,
            timeout=timeout,
        )
        result = (init or {}).get("result") or {}
        info = result.get("serverInfo") or {}
        print("  initialize    protocol=%s server=%s" % (
            result.get("protocolVersion") or "-", info.get("name") or "-"))
        if isinstance(doc, dict) and "error" in doc:
            err = doc["error"] or {}
            print("  tools/list    error %s: %s" % (err.get("code"), err.get("message")))
            sys.exit(1)
        tools = ((doc or {}).get("result") or {}).get("tools") or []
        if not tools:
            raise RuntimeError("no tools in the tools/list response")
        print("  tools/list    %d tool(s)" % len(tools))
        for tool in tools[:20]:
            print("    - %s" % (tool.get("name") or "?"))
        sys.exit(0)
    except Exception as exc:
        last = str(exc)
        time.sleep(1)

print("timed out after %ss waiting for tools/list" % sys.argv[2])
if last:
    print("  last error: %s" % last[:200])
if env_hint:
    print("a server waiting on credentials or OAuth stays silent: %s" % env_hint)
sys.exit(1)
PY
  then
    tail -n 20 "$LOG_FILE" >&2
    log_hint
    return 1
  fi
}

case "$COMMAND" in
  start) start_server ;;
  stop) stop_server ;;
  restart) stop_server; start_server ;;
  status) status_server ;;
  smoke) smoke_server ;;
esac
