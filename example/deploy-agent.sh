#!/usr/bin/env bash
# deploy-agent.sh — deploy one of the example A2A agents on WSL2 / Ubuntu.
#
# Usage:
#   ./deploy-agent.sh <agent> [command] [options]
#
# Agents:
#   google-adk   Google ADK, wrapped with to_a2a(...) and served by uvicorn.
#                Port 8001 is not a free choice: `adk api_server` (8000) exposes
#                ADK's own REST API (/run, /run_sse) and is *not* A2A. The
#                a2a-sdk publishes the Agent Card at
#                http://127.0.0.1:8001/.well-known/agent-card.json.
#
# Commands (default: start):
#   start    install into a venv, generate the agent, serve it in the background
#   stop     TERM the recorded pid (KILL when it ignores TERM)
#   restart  stop, then start
#   status   pid, Agent Card reachability and the tail of the log
#            (exit 1 when nothing is running)
#   smoke    print the card, send one message/send and print the reply
#
# Options:
#   --llm                serve the gateway-backed agent instead of the offline one
#   --model-id <id>      model id, default ariacompute/ariamodel
#   --api-base <url>     OpenAI-compatible base url, default
#                        https://gateway.ariacompute.com/v1
#   --api-key <key>      gateway key, overrides AGENTHUB_EXAMPLE_API_KEY
#   --host <host>        listen address, default from the example JSON (127.0.0.1)
#   --port <port>        listen port, default from the example JSON (8001 for adk)
#   --workdir <dir>      default $HOME/.local/share/agenthub-<agent>
#   --wait <sec>         how long to wait for the Agent Card, default 60
#                        (smoke allows up to 120s for the reply; an agent backed
#                        by a reasoning gateway is slow, not missing)
#   -h | --help          this text
#
# Credentials: the gateway key comes from --api-key or AGENTHUB_EXAMPLE_API_KEY
# and only ever travels through the server process environment — it is not
# written into the generated agent, the pid file or the log, and is never
# echoed. Prefer the environment variable: a value on the command line is
# visible to other users through `ps`.
#
# This script never talks to agenthub: it does not log in, does not load the
# catalogue and does not call /probe. Run example/load.sh separately.
#
# --- end of usage ---

set -euo pipefail

SELF="$0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RES_DIR="$SCRIPT_DIR/resources"

SUPPORTED_AGENTS="google-adk"
COMMANDS="start stop restart status smoke"

AGENT=""
COMMAND=""
USE_LLM=0
MODEL_ID="ariacompute/ariamodel"
API_BASE="https://gateway.ariacompute.com/v1"
API_KEY=""
API_KEY_FROM_CLI=0
HOST_ARG=""
PORT_ARG=""
WORKDIR=""
WAIT="60"
SMOKE_TEXT="${AGENTHUB_AGENT_SMOKE_TEXT:-ping from deploy-agent.sh}"

print_help() {
  sed -n '2,/^# --- end of usage ---$/p' "$SELF" |
    sed -e 's/^# \{0,1\}//' -e '/^--- end of usage ---$/d'
}

usage_error() {
  echo "$1" >&2
  echo "supported agents: $SUPPORTED_AGENTS" >&2
  echo "run '$SELF --help' for the full usage" >&2
  exit 2
}

# ---- arguments ---------------------------------------------------------------

while [ $# -gt 0 ]; do
  case "$1" in
    -h | --help)
      print_help
      exit 0
      ;;
    --llm) USE_LLM=1 ;;
    --model-id)
      MODEL_ID="${2:-}"
      shift
      ;;
    --api-base)
      API_BASE="${2:-}"
      shift
      ;;
    --api-key)
      API_KEY="${2:-}"
      API_KEY_FROM_CLI=1
      shift
      ;;
    --host)
      HOST_ARG="${2:-}"
      shift
      ;;
    --port)
      PORT_ARG="${2:-}"
      shift
      ;;
    --workdir)
      WORKDIR="${2:-}"
      shift
      ;;
    --wait)
      WAIT="${2:-}"
      shift
      ;;
    --agent)
      AGENT="${2:-}"
      shift
      ;;
    start | stop | restart | status | smoke)
      COMMAND="$1"
      ;;
    -*)
      echo "unknown option: $1" >&2
      exit 2
      ;;
    *)
      if [ -n "$AGENT" ]; then
        echo "unexpected argument: $1 (agent is already '$AGENT')" >&2
        exit 2
      fi
      AGENT="$1"
      ;;
  esac
  shift
done

[ -n "$AGENT" ] || usage_error "no agent given"
case " $SUPPORTED_AGENTS " in
  *" $AGENT "*) ;;
  *) usage_error "unsupported agent: $AGENT" ;;
esac

COMMAND="${COMMAND:-start}"
case " $COMMANDS " in
  *" $COMMAND "*) ;;
  *) usage_error "unsupported command: $COMMAND" ;;
esac

if [ -z "$MODEL_ID" ] || [ -z "$API_BASE" ]; then
  echo "--model-id and --api-base cannot be empty" >&2
  exit 2
fi
case "$WAIT" in
  '' | *[!0-9]*)
    echo "--wait must be a number of seconds, got: $WAIT" >&2
    exit 2
    ;;
esac
if [ -n "$PORT_ARG" ]; then
  case "$PORT_ARG" in
    '' | *[!0-9]*)
      echo "--port must be a number, got: $PORT_ARG" >&2
      exit 2
      ;;
  esac
fi

# ---- per-agent spec ---------------------------------------------------------

# Adding an agent is one case block: what to install, which module holds the
# app, how to run it, and the fallbacks used when the example JSON is missing.
PY_DEPS=()
PY_IMPORTS=()
MODULE=""
APP_VAR=""
DEFAULT_HOST=""
DEFAULT_PORT=""
DEFAULT_CARD_PATH=""
RES_FILE=""

case "$AGENT" in
  google-adk)
    PY_DEPS=("google-adk[a2a]" "uvicorn")
    PY_IMPORTS=("google.adk" "uvicorn")
    MODULE="a2a_agent"
    APP_VAR="a2a_app"
    DEFAULT_HOST="127.0.0.1"
    DEFAULT_PORT="8001"
    DEFAULT_CARD_PATH="/.well-known/agent-card.json"
    RES_FILE="$RES_DIR/agent-google-adk-ubuntu.json"
    ;;
esac

# The catalogue is the single source of truth for where an agent lives: read the
# endpoint (host/port) and the card path out of the example payload instead of
# duplicating them here. python3 is already a hard dependency, so no jq needed.
json_spec() {
  python3 - "$RES_FILE" <<'PY' 2>/dev/null || true
import json, sys
from urllib.parse import urlparse

host = port = card = ""
try:
    with open(sys.argv[1]) as fh:
        doc = json.load(fh)
except Exception:
    doc = {}
endpoint = doc.get("endpoint") or ""
parsed = urlparse(endpoint)
if parsed.hostname:
    host = parsed.hostname
if parsed.port:
    port = str(parsed.port)
card = ((doc.get("kind_config") or {}).get("agent") or {}).get("card_url") or ""
print(host)
print(port)
print(card)
PY
}

SPEC="$(json_spec)"
JSON_HOST="$(printf '%s\n' "$SPEC" | sed -n '1p')"
JSON_PORT="$(printf '%s\n' "$SPEC" | sed -n '2p')"
JSON_CARD="$(printf '%s\n' "$SPEC" | sed -n '3p')"

HOST="${HOST_ARG:-${JSON_HOST:-$DEFAULT_HOST}}"
PORT="${PORT_ARG:-${JSON_PORT:-$DEFAULT_PORT}}"
CARD_PATH="$DEFAULT_CARD_PATH"
if [ -n "$JSON_CARD" ]; then
  # Keep only the path: the host/port in force win, the catalogue only decides
  # where the card hangs.
  DERIVED="$(printf '%s' "$JSON_CARD" | sed -e 's|^[a-zA-Z][a-zA-Z0-9+.-]*://[^/]*||')"
  if [ -n "$DERIVED" ]; then
    CARD_PATH="$DERIVED"
  fi
fi

if [ -z "$JSON_HOST" ] || [ "$HOST" != "$JSON_HOST" ] || [ "$PORT" != "${JSON_PORT:-$PORT}" ]; then
  echo "note: serving on $HOST:$PORT while $RES_FILE registers ${JSON_HOST:-<none>}:${JSON_PORT:-<none>}" >&2
  echo "      the published card url will differ from the registered card_url" >&2
fi

WORKDIR="${WORKDIR:-${XDG_DATA_HOME:-${HOME:-/tmp}/.local/share}/agenthub-$AGENT}"
VENV="$WORKDIR/.venv"
APP_FILE="$WORKDIR/$MODULE.py"
PID_FILE="$WORKDIR/$AGENT.pid"
LOG_FILE="$WORKDIR/$AGENT.log"
ORIGIN="http://$HOST:$PORT"
CARD_URL="$ORIGIN$CARD_PATH"

BODY="$(mktemp)"
trap 'rm -f "$BODY"' EXIT

# ---- helpers -----------------------------------------------------------------

require() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing dependency: $1" >&2
    exit 2
  }
}

# port_open <host> <port>: something is already listening there.
port_open() {
  local host="$1" port="$2" code
  if [ "$host" = "0.0.0.0" ]; then
    host="127.0.0.1"
  fi
  if (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null; then
    return 0
  fi
  # /dev/tcp is a bash build option; fall back to asking curl.
  code="$(curl -s -o /dev/null -m 2 -w '%{http_code}' "http://$host:$port/" || true)"
  if [ "${code:-000}" != "000" ]; then
    return 0
  fi
  return 1
}

pid_alive() {
  [ -f "$PID_FILE" ] || return 1
  kill -0 "$(cat "$PID_FILE")" 2>/dev/null
}

tail_log() {
  if [ -f "$LOG_FILE" ]; then
    echo "last lines of $LOG_FILE:" >&2
    tail -n "${1:-20}" "$LOG_FILE" >&2
  fi
}

deps_present() {
  "$VENV/bin/python" - "$@" <<'PY' >/dev/null 2>&1
import importlib, sys

for name in sys.argv[1:]:
    importlib.import_module(name)
PY
}

check_python() {
  require python3
  if ! python3 -c 'import venv' >/dev/null 2>&1; then
    echo "python3 cannot create a virtualenv — on Ubuntu/Debian install it with:" >&2
    echo "  sudo apt install -y python3-venv python3-pip" >&2
    exit 2
  fi
}

ensure_venv() {
  if [ ! -x "$VENV/bin/python" ]; then
    echo "creating venv in $VENV"
    python3 -m venv "$VENV"
  fi
}

install_deps() {
  local -a imports=("${PY_IMPORTS[@]}") deps=("${PY_DEPS[@]}")
  if [ "$USE_LLM" = "1" ]; then
    imports+=("litellm")
    deps+=("litellm")
  fi
  if deps_present "${imports[@]}"; then
    echo "dependencies already present: ${deps[*]}"
    return 0
  fi
  echo "installing ${deps[*]} — first run takes a while"
  "$VENV/bin/python" -m pip install --disable-pip-version-check -q --upgrade pip
  "$VENV/bin/python" -m pip install --disable-pip-version-check -q "${deps[@]}"
}

# The key is resolved once and only ever handed to the server process: nothing
# here writes it to a file and nothing here prints it.
resolve_api_key() {
  if [ "$USE_LLM" != "1" ]; then
    return 0
  fi
  if [ -z "$API_KEY" ]; then
    API_KEY="${AGENTHUB_EXAMPLE_API_KEY:-}"
  fi
  if [ -z "$API_KEY" ]; then
    echo "--llm needs a gateway key: pass --api-key or export AGENTHUB_EXAMPLE_API_KEY" >&2
    echo "the value only reaches the server process environment; it is never written to a file" >&2
    exit 2
  fi
  if [ "$API_KEY_FROM_CLI" = "1" ]; then
    echo "note: a key given on the command line is visible in 'ps'; prefer the environment variable" >&2
  fi
}

# ---- generated agent ---------------------------------------------------------

write_app() {
  if [ "$USE_LLM" = "1" ]; then
    cat >"$APP_FILE" <<PY
"""Generated by example/deploy-agent.sh -- agent $AGENT, gateway variant.

Do not edit by hand: rerun the script. The gateway key is read from the
environment only, so it never lands in this file, in the pid file or in the log.
"""
import os

from google.adk.agents import Agent
from google.adk.models.lite_llm import LiteLlm

# 1.x exported to_a2a from the package root; 2.x moved it under utils.
try:
    from google.adk.a2a import to_a2a
except ImportError:
    from google.adk.a2a.utils.agent_to_a2a import to_a2a

HOST = "$HOST"
PORT = $PORT
MODEL_ID = "$MODEL_ID"
API_BASE = "$API_BASE"
API_KEY_ENV = "AGENTHUB_EXAMPLE_API_KEY"

api_key = os.environ.get(API_KEY_ENV)
if not api_key:
    raise SystemExit(API_KEY_ENV + " is empty: export it before starting the agent")

root_agent = Agent(
    name="aria_adk_llm_agent",
    model=LiteLlm(model="openai/" + MODEL_ID, api_base=API_BASE, api_key=api_key),
    description="ADK agent served over A2A and backed by the Aria gateway.",
    instruction="You are a concise assistant. Answer the user's question directly.",
)

try:
    a2a_app = to_a2a(root_agent, host=HOST, port=PORT)
except TypeError:
    # Older releases only take port; the bind address then comes from uvicorn.
    a2a_app = to_a2a(root_agent, port=PORT)
PY
  else
    cat >"$APP_FILE" <<PY
"""Generated by example/deploy-agent.sh -- agent $AGENT, offline variant.

Do not edit by hand: rerun the script. This agent never calls an LLM, so the
deployment can be verified end to end without a credential, and its reply is
deterministic enough to tell apart from a mock.
"""
from typing import AsyncGenerator

from google.adk.agents import BaseAgent
from google.adk.agents.invocation_context import InvocationContext
from google.adk.events import Event
from google.genai import types

# 1.x exported to_a2a from the package root; 2.x moved it under utils.
try:
    from google.adk.a2a import to_a2a
except ImportError:
    from google.adk.a2a.utils.agent_to_a2a import to_a2a

HOST = "$HOST"
PORT = $PORT
PREFIX = "adk-offline:"


class OfflineEchoAgent(BaseAgent):
    """Echoes the prompt back with a fixed prefix, so the reply is unmistakable."""

    async def _run_async_impl(self, ctx: InvocationContext) -> AsyncGenerator[Event, None]:
        text = ""
        if ctx.user_content is not None and ctx.user_content.parts:
            raw = "".join(part.text or "" for part in ctx.user_content.parts)
            text = " ".join(raw.split())
        if text:
            reply = PREFIX + " received %d chars: %r" % (len(text), text[:200])
        else:
            reply = PREFIX + " the message carried no text part"
        yield Event(
            author=self.name,
            content=types.Content(role="model", parts=[types.Part(text=reply)]),
        )


root_agent = OfflineEchoAgent(
    name="aria_adk_offline_agent",
    description="Deterministic offline ADK agent used to verify an A2A deployment.",
)

try:
    a2a_app = to_a2a(root_agent, host=HOST, port=PORT)
except TypeError:
    a2a_app = to_a2a(root_agent, port=PORT)
PY
  fi
}

# ---- commands ----------------------------------------------------------------

wait_for_card() {
  local deadline code
  deadline=$(( $(date +%s) + WAIT ))
  while :; do
    code="$(curl -s -o "$BODY" -m 3 -w '%{http_code}' "$CARD_URL" || true)"
    if [ "${code:-000}" = "200" ]; then
      return 0
    fi
    if ! pid_alive; then
      echo "the server exited before it served a card" >&2
      tail_log 20
      echo "google-adk moves fast: pin a known good release with" >&2
      echo "  $VENV/bin/python -m pip install \"google-adk[a2a]==<version>\"" >&2
      rm -f "$PID_FILE"
      return 1
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "timed out after ${WAIT}s waiting for $CARD_URL" >&2
      tail_log 20
      return 1
    fi
    sleep 1
  done
}

start_agent() {
  check_python
  require curl
  resolve_api_key

  if pid_alive; then
    echo "$AGENT is already running (pid $(cat "$PID_FILE")) — card: $CARD_URL"
    return 0
  fi
  if port_open "$HOST" "$PORT"; then
    echo "port $PORT is already in use and no $PID_FILE of this script owns it;" >&2
    echo "refusing to touch it. Find the owner with: ss -ltnp 'sport = :$PORT'" >&2
    return 1
  fi

  mkdir -p "$WORKDIR"
  ensure_venv
  install_deps
  write_app
  : >"$LOG_FILE"

  (
    cd "$WORKDIR"
    if [ -n "$API_KEY" ]; then
      export AGENTHUB_EXAMPLE_API_KEY="$API_KEY"
    fi
    # shellcheck disable=SC2086
    nohup "$VENV/bin/uvicorn" "$MODULE:$APP_VAR" --host "$HOST" --port "$PORT" >>"$LOG_FILE" 2>&1 &
    echo $! >"$PID_FILE"
  )

  if ! wait_for_card; then
    return 1
  fi
  echo "$AGENT is serving at $CARD_URL (pid $(cat "$PID_FILE"), log $LOG_FILE)"
  echo "verify the round trip with: $SELF $AGENT smoke"
}

stop_agent() {
  local pid waited=0
  if [ ! -f "$PID_FILE" ]; then
    echo "$AGENT is not running (no pid file in $WORKDIR)"
    return 0
  fi
  pid="$(cat "$PID_FILE")"
  if ! kill -0 "$pid" 2>/dev/null; then
    echo "$AGENT is not running (stale pid $pid)"
    rm -f "$PID_FILE"
    return 0
  fi
  kill -TERM "$pid" 2>/dev/null || true
  while [ "$waited" -lt 50 ] && kill -0 "$pid" 2>/dev/null; do
    sleep 0.2
    waited=$((waited + 1))
  done
  if kill -0 "$pid" 2>/dev/null; then
    echo "pid $pid ignored TERM, sending KILL"
    kill -KILL "$pid" 2>/dev/null || true
    sleep 0.5
  fi
  rm -f "$PID_FILE"
  echo "stopped $AGENT (pid $pid)"
}

print_card() {
  python3 - "$BODY" <<'PY'
import json, sys

try:
    with open(sys.argv[1]) as fh:
        card = json.load(fh)
except Exception:
    print("  ! the card was not JSON")
    sys.exit(0)
# A2A 1.0 cards carry the endpoint under supportedInterfaces instead of a
# top-level url, and older ones do the opposite; show whichever is present.
interfaces = card.get("supportedInterfaces") or []
if interfaces:
    card.setdefault("url", interfaces[0].get("url"))
    card.setdefault("protocolVersion", interfaces[0].get("protocolVersion"))
for key in ("name", "url", "protocolVersion", "version", "description"):
    value = card.get(key)
    if isinstance(value, str) and value:
        print("  %-16s %s" % (key, value[:120]))
PY
}

status_agent() {
  local pid="-" code
  if [ -f "$PID_FILE" ]; then
    pid="$(cat "$PID_FILE")"
  fi
  if pid_alive; then
    echo "running       pid $pid"
  else
    echo "stopped       pid file: $pid"
  fi
  code="$(curl -s -o "$BODY" -m 5 -w '%{http_code}' "$CARD_URL" || true)"
  echo "card          $CARD_URL -> ${code:-000}"
  if [ "${code:-000}" = "200" ]; then
    print_card
  fi
  if [ -f "$LOG_FILE" ]; then
    echo "log           $LOG_FILE"
  fi
  pid_alive
}

smoke_agent() {
  local code payload
  code="$(curl -s -o "$BODY" -m 5 -w '%{http_code}' "$CARD_URL" || true)"
  if [ "${code:-000}" != "200" ]; then
    echo "no Agent Card at $CARD_URL (status ${code:-000})" >&2
    echo "start it first: $SELF $AGENT start" >&2
    return 1
  fi
  echo "card          $CARD_URL -> 200"
  print_card

  # Same shape internal/a2a builds for message/send, carrying both the spec's
  # "kind" and the older "type" so either generation of server accepts it.
  payload="$(SMOKE_TEXT="$SMOKE_TEXT" python3 - <<'PY'
import json, os, uuid

print(json.dumps({
    "jsonrpc": "2.0",
    "id": "deploy-agent-smoke-1",
    "method": "message/send",
    "params": {
        "message": {
            "kind": "message",
            "role": "user",
            "messageId": uuid.uuid4().hex,
            "parts": [{"kind": "text", "type": "text", "text": os.environ["SMOKE_TEXT"]}],
        }
    },
}))
PY
)"

  echo "message/send  $ORIGIN/"
  # A reasoning gateway can take ~40s for one turn, so the budget here is wider
  # than a plain liveness check would be.
  code="$(curl -s -o "$BODY" -m 120 -w '%{http_code}' -X POST "$ORIGIN/" \
    -H 'Content-Type: application/json' --data "$payload" || true)"
  echo "status        ${code:-000}"
  python3 - "$BODY" <<'PY'
import json, sys

try:
    with open(sys.argv[1]) as fh:
        reply = json.load(fh)
except Exception:
    with open(sys.argv[1]) as fh:
        print("  ! the reply was not JSON: " + fh.read()[:400])
    sys.exit(1)

if "error" in reply:
    err = reply["error"]
    print("  ! rpc error %s: %s" % (err.get("code"), err.get("message")))
    print("    an old a2a-sdk only answers tasks/send: %s" % json.dumps(err)[:200])
    sys.exit(1)

result = reply.get("result") or {}
texts = []


def collect(parts):
    for part in parts or []:
        if isinstance(part, dict) and (part.get("kind") or part.get("type")) == "text":
            texts.append(part.get("text") or "")


collect(result.get("parts"))
for artifact in result.get("artifacts") or []:
    collect(artifact.get("parts"))
status = result.get("status") or {}
collect((status.get("message") or {}).get("parts"))

print("  state        %s" % (status.get("state") or result.get("state") or "completed"))
if texts:
    print("  reply        " + " ".join("".join(texts).split()))
else:
    print("  ! no text part came back; raw result:")
    print(json.dumps(result)[:600])
    sys.exit(1)
PY
}

# The offline agent calls nothing, so a key would only be a temptation to log
# it somewhere. Say so and drop it before it can reach any file.
if [ "$USE_LLM" != "1" ] && [ -n "$API_KEY" ]; then
  echo "note: --api-key is ignored without --llm (the offline agent calls nothing)" >&2
  API_KEY=""
fi

case "$COMMAND" in
  start) start_agent ;;
  stop) stop_agent ;;
  restart)
    stop_agent
    start_agent
    ;;
  status) status_agent ;;
  smoke) smoke_agent ;;
esac
