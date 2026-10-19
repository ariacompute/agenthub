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
#   AGENTHUB_EXAMPLE_EMAIL       fallback: bootstrap admin, then admin@example.com
#   AGENTHUB_EXAMPLE_PASSWORD    fallback: bootstrap admin password, then agenthub-admin-pass
#   AGENTHUB_SESSION_TOKEN       optional: skip login, send X-AgentHub-Session
#   AGENTHUB_EXAMPLE_MODEL_ID    injected into model-gateway.json (no default)
#   ALLOW_REGISTER               default 0; set 1 to create the account if it is missing

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
  # Whatever the caller already exported wins: snapshot it, let the file fill the
  # gaps, then restore. This is how the server reads its own .env
  # (internal/config/dotenv.go only fills unset keys), so an explicit
  # `AGENTHUB_EXAMPLE_ENABLED=false ./load.sh` really does switch the catalogue
  # off even when the file says otherwise.
  caller_env="$(export -p)"
  # shellcheck disable=SC1090
  set -a && . "$ENV_FILE" && set +a
  eval "$caller_env"
fi

if [ "${AGENTHUB_EXAMPLE_ENABLED:-false}" != "true" ]; then
  echo "example catalogue is disabled: set AGENTHUB_EXAMPLE_ENABLED=true in $ENV_FILE" >&2
  exit 1
fi

BASE_URL="${AGENTHUB_EXAMPLE_BASE_URL:-${BASE_URL:-http://127.0.0.1:${AGENTHUB_HTTP_PORT:-3500}}}"
BASE_URL="${BASE_URL%/}"
# The bootstrap administrator is the instance owner and therefore the only
# account guaranteed to hold resources:write, so it is the fallback before the
# generic default — otherwise the loader can end up registering a second-class
# account whose every write answers 403.
EMAIL="${AGENTHUB_EXAMPLE_EMAIL:-${EMAIL:-${AGENTHUB_BOOTSTRAP_ADMIN_EMAIL:-admin@example.com}}}"
PASSWORD="${AGENTHUB_EXAMPLE_PASSWORD:-${PASSWORD:-${AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD:-agenthub-admin-pass}}}"
SESSION_TOKEN="${AGENTHUB_SESSION_TOKEN:-}"
MODEL_ID="${AGENTHUB_EXAMPLE_MODEL_ID:-}"
# Creating the account is opt-in: on a fresh instance no one is signed up yet,
# but silently registering one against an instance that already has an owner
# produces a second-class account with no resources:write, which turns into 41
# identical 403s later. Set ALLOW_REGISTER=1 for the first run instead.
ALLOW_REGISTER="${ALLOW_REGISTER:-0}"

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

# http <method> <path> [json] prints the status code. When curl never gets an
# HTTP response it prints 000 and exits nonzero — `|| true` keeps that value
# instead of letting the nonzero exit ride a command substitution into `set -e`,
# which used to abort the whole script with nothing but curl's status.
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
  # shellcheck disable=SC2091
  curl "${args[@]}" 2>/dev/null || true
}

# unreachable <code> <what> aborts when the previous call got no HTTP response.
# That is always "the server is not there", never a resource-level rejection, so
# it replaces what would otherwise be 41 identical "FAILED … (status 000)" lines.
unreachable() {
  case "$1" in
    "" | 000)
      echo "cannot reach $BASE_URL during $2 — is agenthub still running?" >&2
      echo "start it before loading, e.g. 'make run' or 'docker compose up -d'." >&2
      exit 1
      ;;
  esac
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
  unreachable "$code" "registration of $id"
  if [ "$code" = "201" ]; then
    echo "  created  $id (201)"
    return 0
  fi
  if [ "$code" = "400" ] && grep -q 'resource_already_exists' "$BODY"; then
    code="$(http PUT "/api/resources/$id" "$payload")"
    unreachable "$code" "update of $id"
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
  unreachable "$code" "login as $EMAIL"
  if [ "$code" != "200" ] && [ "$ALLOW_REGISTER" = "1" ]; then
    reg_body="$(printf '{"email":"%s","password":"%s","name":"example-loader"}' "$EMAIL" "$PASSWORD")"
    reg_code="$(http POST /api/auth/register "$reg_body")"
    unreachable "$reg_code" "registration of $EMAIL"
    if [ "$reg_code" = "201" ] || [ "$reg_code" = "409" ]; then
      code="$(http POST /api/auth/login "$login_body")"
      unreachable "$code" "login as $EMAIL"
    fi
  fi
  if [ "$code" != "200" ]; then
    echo "login failed (status $code) as $EMAIL against $BASE_URL" >&2
    sed -n '1,5p' "$BODY" >&2
    if [ "$ALLOW_REGISTER" != "1" ]; then
      echo "the loader does not create accounts unless ALLOW_REGISTER=1;" >&2
      echo "otherwise set AGENTHUB_EXAMPLE_EMAIL / AGENTHUB_EXAMPLE_PASSWORD to an" >&2
      echo "account that exists — the instance owner is the one that can write." >&2
    fi
    exit 1
  fi
  echo "logged in as $EMAIL"
fi

# ---- permission preflight ----------------------------------------------------

# Registering a resource needs resources:write. Ask /api/auth/me once before the
# first payload: otherwise every file answers 403 and the real cause (the wrong
# account) hides behind 41 identical failures. An auto-registered account is the
# usual culprit — Register() sets isOwner only when the users table was empty,
# so once a bootstrap administrator exists it lands with no permissions at all.
code="$(http GET /api/auth/me)"
unreachable "$code" "the permission preflight"
if [ "$code" != "200" ] || ! grep -q 'resources:write' "$BODY"; then
  echo "$EMAIL cannot register resources against $BASE_URL (no resources:write)" >&2
  echo "POST /api/resources needs the instance owner — the bootstrap" >&2
  echo "administrator configured with AGENTHUB_BOOTSTRAP_ADMIN_EMAIL/PASSWORD" >&2
  echo "— or a role that grants it. Point the loader at that account with" >&2
  echo "AGENTHUB_EXAMPLE_EMAIL / AGENTHUB_EXAMPLE_PASSWORD." >&2
  sed -n '1,5p' "$BODY" >&2
  exit 1
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
