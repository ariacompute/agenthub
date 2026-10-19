#!/usr/bin/env bash
# Loads the registry resource examples into a running agenthub.
#
# Every example is a complete registry.Entry payload with a fixed id, so the
# script is safe to re-run: it POSTs first (201) and falls back to PUT (200)
# when the (namespace, name, version) dedup key says the record already exists.
# PUT is *not* an upsert in agenthub (a missing id answers 404), hence the
# POST-then-PUT dance.
#
# Usage:
#   ./load.sh                 # picks the platform from uname -s
#   ./load.sh --platform windows | --platform ubuntu
#   ./load.sh --all           # loads both platform namespaces
#
# Environment: read from the **repository-root** .env (gitignored; see the root
# .env.example) or export any of these in the shell. The catalogue is opt-in —
# the loader exits unless AGENTHUB_EXAMPLE_ENABLED=true.
#   AGENTHUB_EXAMPLE_ENABLED     required: anything but "true" refuses to load
#   AGENTHUB_EXAMPLE_BASE_URL    default http://127.0.0.1:$AGENTHUB_HTTP_PORT
#   AGENTHUB_EXAMPLE_EMAIL       default admin@example.com
#   AGENTHUB_EXAMPLE_PASSWORD    default agenthub-admin-pass
#   AGENTHUB_SESSION_TOKEN       optional: skip login, send X-AgentHub-Session
#   AGENTHUB_EXAMPLE_MODEL_ID    injected into model-gateway.json (no default)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
RES_DIR="$SCRIPT_DIR/resources"

# The root .env is the single place for agenthub configuration now; a legacy
# example/.env is still honoured when present so existing local files keep working.
ENV_FILE="$ROOT_DIR/.env"
if [ ! -f "$ENV_FILE" ] && [ -f "$SCRIPT_DIR/.env" ]; then
  ENV_FILE="$SCRIPT_DIR/.env"
fi

# ---- configuration -----------------------------------------------------------

if [ -f "$ENV_FILE" ]; then
  # shellcheck disable=SC1090
  set -a && . "$ENV_FILE" && set +a
fi

if [ "${AGENTHUB_EXAMPLE_ENABLED:-false}" != "true" ]; then
  echo "example catalogue is disabled: set AGENTHUB_EXAMPLE_ENABLED=true in $ENV_FILE" >&2
  exit 1
fi

BASE_URL="${AGENTHUB_EXAMPLE_BASE_URL:-${BASE_URL:-http://127.0.0.1:${AGENTHUB_HTTP_PORT:-3500}}}"
BASE_URL="${BASE_URL%/}"
EMAIL="${AGENTHUB_EXAMPLE_EMAIL:-${EMAIL:-admin@example.com}}"
PASSWORD="${AGENTHUB_EXAMPLE_PASSWORD:-${PASSWORD:-agenthub-admin-pass}}"
SESSION_TOKEN="${AGENTHUB_SESSION_TOKEN:-}"
MODEL_ID="${AGENTHUB_EXAMPLE_MODEL_ID:-}"
ALLOW_REGISTER="${ALLOW_REGISTER:-1}"

PLATFORM=""
ALL=0
while [ $# -gt 0 ]; do
  case "$1" in
    --all) ALL=1 ;;
    --platform)
      PLATFORM="${2:-}"
      shift
      ;;
    -h | --help)
      sed -n '2,25p' "$0"
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      exit 2
      ;;
  esac
  shift
done

if [ "$ALL" = "0" ] && [ -z "$PLATFORM" ]; then
  case "$(uname -s)" in
    Linux* | Darwin*) PLATFORM="ubuntu" ;;
    MINGW* | MSYS* | CYGWIN* | Windows_NT) PLATFORM="windows" ;;
    *)
      echo "cannot detect platform from uname -s; pass --platform" >&2
      exit 2
      ;;
  esac
fi

if [ "$ALL" = "1" ]; then
  PLATFORMS="ubuntu windows"
else
  PLATFORMS="$PLATFORM"
fi

# ---- helpers -----------------------------------------------------------------

COOKIES="$(mktemp)"
BODY="$(mktemp)"
trap 'rm -f "$COOKIES" "$BODY"' EXIT

# http <method> <path> [json]
http() {
  local method="$1" path="$2" data="${3:-}"
  local args=(-s -o "$BODY" -w '%{http_code}' -X "$method" "$BASE_URL$path"
    -H 'Content-Type: application/json' -b "$COOKIES" -c "$COOKIES")
  if [ -n "$SESSION_TOKEN" ]; then
    args+=(-H "X-AgentHub-Session: $SESSION_TOKEN")
  fi
  if [ -n "$data" ]; then
    args+=(--data "$data")
  fi
  curl "${args[@]}"
}

require() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing dependency: $1" >&2
    exit 2
  }
}

# render <file> prints the payload with placeholders substituted.
render() {
  local file="$1"
  if grep -q '__AGENTHUB_EXAMPLE_MODEL_ID__' "$file"; then
    if [ -z "$MODEL_ID" ]; then
      echo "AGENTHUB_EXAMPLE_MODEL_ID is not set: put it in the repository-root .env (see .env.example)" >&2
      exit 2
    fi
  fi
  sed "s|__AGENTHUB_EXAMPLE_MODEL_ID__|${MODEL_ID}|g" "$file"
}

# entry_id <file> reads the fixed id from the payload.
entry_id() {
  sed -n 's/^[[:space:]]*"id": "\(.*\)",\{0,1\}$/\1/p' "$1" | head -n 1
}

# load_one <file>
load_one() {
  local file="$1" id payload code
  id="$(entry_id "$file")"
  if [ -z "$id" ]; then
    echo "  ! no id found in $file" >&2
    return 1
  fi
  payload="$(render "$file")"

  code="$(http POST /api/resources "$payload")"
  if [ "$code" = "201" ]; then
    echo "  created  $id (201)"
    return 0
  fi
  if [ "$code" = "400" ] && grep -q 'resource_already_exists' "$BODY"; then
    code="$(http PUT "/api/resources/$id" "$payload")"
    if [ "$code" = "200" ]; then
      echo "  updated  $id (200)"
      return 0
    fi
  fi
  echo "  FAILED   $id (status $code)" >&2
  sed -n '1,5p' "$BODY" >&2
  return 1
}

# ---- login -------------------------------------------------------------------

require curl

if [ -z "$SESSION_TOKEN" ]; then
  login_body="$(printf '{"email":"%s","password":"%s"}' "$EMAIL" "$PASSWORD")"
  code="$(http POST /api/auth/login "$login_body")"
  if [ "$code" != "200" ] && [ "$ALLOW_REGISTER" = "1" ]; then
    reg_body="$(printf '{"email":"%s","password":"%s","name":"example-loader"}' "$EMAIL" "$PASSWORD")"
    reg_code="$(http POST /api/auth/register "$reg_body")"
    if [ "$reg_code" = "201" ] || [ "$reg_code" = "409" ]; then
      code="$(http POST /api/auth/login "$login_body")"
    fi
  fi
  if [ "$code" != "200" ]; then
    echo "login failed (status $code) as $EMAIL against $BASE_URL" >&2
    sed -n '1,5p' "$BODY" >&2
    exit 1
  fi
  echo "logged in as $EMAIL"
fi

# ---- load --------------------------------------------------------------------

failures=0
loaded=0

echo "loading model (namespace: example)"
if load_one "$RES_DIR/model-gateway.json"; then
  loaded=$((loaded + 1))
else
  failures=$((failures + 1))
fi

for platform in $PLATFORMS; do
  echo "loading $platform (namespace: example-$platform)"
  for file in "$RES_DIR"/*-"$platform".json; do
    [ -e "$file" ] || continue
    if load_one "$file"; then
      loaded=$((loaded + 1))
    else
      failures=$((failures + 1))
    fi
  done
done

echo "summary: $loaded ok, $failures failed"
if [ "$failures" -ne 0 ]; then
  exit 1
fi
