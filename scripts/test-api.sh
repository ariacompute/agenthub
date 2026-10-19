#!/usr/bin/env sh
# test-api.sh — exercise the whole HTTP surface of a running agenthub.
#
# It is the scripted form of "Tests -> End-to-end smoke test" in README.md:
# discovery, sending a task, streaming and park/resume, plus everything else the
# README describes — the /api control plane (resources CRUD, exports, tasks,
# spans, event replay), the A2A JSON-RPC methods, the error contract and the
# console bundle. Every assertion prints PASS / FAIL / SKIP and the exit status
# is non-zero as soon as one of them fails, so CI can gate on it.
#
#   ./scripts/test-api.sh                       # http://127.0.0.1:3500
#   BASE_URL=https://hub.example.com ./scripts/test-api.sh
#   AGENTHUB_TEST_EMAIL=me@example.com AGENTHUB_TEST_PASSWORD='…' ./scripts/test-api.sh
#   MAX_TIME=60 ./scripts/test-api.sh           # per-request curl timeout
#
# The control plane is behind a session, so the script logs in first and keeps
# the cookie in a jar for everything after it. Credentials come from
# AGENTHUB_TEST_EMAIL / AGENTHUB_TEST_PASSWORD, falling back to the bootstrap
# administrator (AGENTHUB_BOOTSTRAP_ADMIN_*). On a fresh instance without any
# account it registers the first one, which becomes the owner.
#
# Start a stack that carries the demo resources first — they are opt-in, so the
# flag is required:
#
#   AGENTHUB_SEED_DEMO=true make up      # compose
#   AGENTHUB_SEED_DEMO=true make run     # local
#
# The A2A calls below target demo-mcp / demo-agent, so without those resources the
# script stops with exit 2 instead of reporting a wall of failures.
#
# Requires: curl, python3 (used only to pick values out of JSON responses).
set -u

BASE_URL=${BASE_URL:-http://127.0.0.1:${AGENTHUB_HTTP_PORT:-3500}}
BASE_URL=${BASE_URL%/}
MAX_TIME=${MAX_TIME:-20}
READY_TIMEOUT=${READY_TIMEOUT:-30}

# Who the script runs as. AGENTHUB_SESSION_TOKEN bypasses the login entirely,
# which is how CI reuses a session it already has.
TEST_EMAIL=${AGENTHUB_TEST_EMAIL:-${AGENTHUB_BOOTSTRAP_ADMIN_EMAIL:-admin@example.com}}
TEST_PASSWORD=${AGENTHUB_TEST_PASSWORD:-${AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD:-agenthub-admin-pass}}
SESSION_TOKEN=${AGENTHUB_SESSION_TOKEN:-}

TMP=$(mktemp -d)
BODY="$TMP/body"
CURL_ERR="$TMP/curl.err"
COOKIES="$TMP/cookies"
: >"$COOKIES"
trap 'rm -rf "$TMP"' EXIT INT TERM

PASS=0
FAIL=0
SKIP=0
STATUS=000
STREAM=

if ! command -v curl >/dev/null 2>&1; then
	echo "curl is required" >&2
	exit 2
fi
if ! command -v python3 >/dev/null 2>&1; then
	echo "python3 is required (only used to read JSON responses)" >&2
	exit 2
fi

# ---------------------------------------------------------------------------
# tiny assertion kit
# ---------------------------------------------------------------------------

section() { printf '\n== %s ==\n' "$1"; }
pass() {
	PASS=$((PASS + 1))
	printf '  PASS  %s\n' "$1"
}
fail() {
	FAIL=$((FAIL + 1))
	printf '  FAIL  %s\n' "$1"
	if [ "$#" -gt 1 ]; then printf '        %s\n' "$2"; fi
}
skip() {
	SKIP=$((SKIP + 1))
	printf '  SKIP  %s (%s)\n' "$1" "$2"
}
expect() { # expect <name> <want> <got>
	if [ "$2" = "$3" ]; then pass "$1"; else fail "$1" "want [$2], got [$3]"; fi
}
expect_match() { # expect_match <name> <needle> <haystack>
	case "$3" in *"$2"*) pass "$1";; *) fail "$1" "missing [$2]";; esac
}
expect_no() { # expect_no <name> <needle> <haystack>
	case "$3" in *"$2"*) fail "$1" "unexpected [$2]";; *) pass "$1";; esac
}
expect_ere() { # expect_ere <name> <extended regex> <haystack>
	if printf '%s' "$3" | grep -Eq -- "$2"; then
		pass "$1"
	else
		fail "$1" "no match for /$2/"
	fi
}

# ---------------------------------------------------------------------------
# request helpers
# ---------------------------------------------------------------------------

# Every request carries the session: the cookie jar when the script logged in,
# the X-AgentHub-Session header when a token was handed to it. (POSIX `set --`
# only touches the caller's own positional parameters, so these two lines are
# inlined rather than hidden behind a helper.)
# http <method> <path> [body] [extra header]
http() {
	__m=$1
	__p=$2
	__d=${3:-}
	__h=${4:-}
	set -- -s -S --max-time "$MAX_TIME" -X "$__m" -o "$BODY" -w '%{http_code}' "$BASE_URL$__p"
	if [ "${ANON:-0}" != "1" ]; then
		set -- "$@" -b "$COOKIES" -c "$COOKIES"
		[ -n "$SESSION_TOKEN" ] && set -- "$@" -H "X-AgentHub-Session: $SESSION_TOKEN"
	fi
	case "$__m" in
	POST | PUT) set -- "$@" -H 'Content-Type: application/json' ;;
	esac
	[ -n "$__h" ] && set -- "$@" -H "$__h"
	[ -n "$__d" ] && set -- "$@" --data-binary "$__d"
	STATUS=$(curl "$@" 2>"$CURL_ERR") || true
	[ -n "$STATUS" ] || STATUS=000
}

# http_anon <method> <path> — same as http but without any credential, which is
# how the script proves the control plane is actually closed.
http_anon() {
	ANON=1
	http "$@"
	ANON=0
}

# sse <method> <path> [body] — keep the connection open and capture the frames.
sse() {
	__m=$1
	__p=$2
	__d=${3:-}
	set -- -sN --max-time "$MAX_TIME" -X "$__m" "$BASE_URL$__p"
	set -- "$@" -b "$COOKIES" -c "$COOKIES"
	[ -n "$SESSION_TOKEN" ] && set -- "$@" -H "X-AgentHub-Session: $SESSION_TOKEN"
	[ -n "$__d" ] && set -- "$@" -H 'Content-Type: application/json' --data-binary "$__d"
	STREAM=$(curl "$@" 2>/dev/null) || true
}

# jget <dotted.path> — read a value out of the last response body. An empty
# result means "absent"; objects and arrays come back serialised so they can be
# matched as plain text.
jget() {
	python3 - "$BODY" "$1" <<'PY'
import json, sys

try:
    with open(sys.argv[1]) as fh:
        doc = json.load(fh)
except Exception:
    sys.exit(0)

cur = doc
for part in sys.argv[2].split("."):
    if part == "":
        continue
    if isinstance(cur, list):
        i = int(part) if part.isdigit() else 0
        cur = cur[i] if i < len(cur) else None
    elif isinstance(cur, dict):
        cur = cur.get(part)
    else:
        cur = None
    if cur is None:
        sys.exit(0)

if isinstance(cur, str):
    print(cur)
elif isinstance(cur, bool):
    print("true" if cur else "false")
else:
    print(json.dumps(cur) if isinstance(cur, (dict, list)) else cur)
PY
}

# jlen <dotted.path> — length of a JSON array/object field, -1 when absent.
jlen() {
	python3 - "$BODY" "$1" <<'PY'
import json, sys

try:
    with open(sys.argv[1]) as fh:
        doc = json.load(fh)
except Exception:
    print(-1)
    sys.exit(0)

cur = doc
for part in sys.argv[2].split("."):
    if part == "":
        continue
    if isinstance(cur, dict):
        cur = cur.get(part)
    elif isinstance(cur, list) and part.isdigit():
        cur = cur[int(part)]
    else:
        cur = None
    if cur is None:
        print(-1)
        sys.exit(0)

print(len(cur) if isinstance(cur, (list, dict, str)) else -1)
PY
}

# rpc <id> <method> <params-json>
rpc() {
	http POST /a2a "$(printf '{"jsonrpc":"2.0","id":%s,"method":"%s","params":%s}' "$1" "$2" "$3")"
}

# msg <resourceId> [extra-metadata-json-inside-braces] <text>
msg() {
	printf '{"message":{"role":"user","parts":[{"type":"text","text":"%s"}]},"metadata":{"resourceId":"%s"%s}}' \
		"$3" "$1" "${2:-}"
}

SENT_RESOURCE=demo-mcp
PARK_RESOURCE=demo-agent

# ---------------------------------------------------------------------------
# wait for the service
# ---------------------------------------------------------------------------

printf 'agenthub API test against %s\n' "$BASE_URL"
i=0
while [ "$i" -lt "$READY_TIMEOUT" ]; do
	http GET /healthz
	case "$STATUS" in
	200 | 503) break ;;
	esac
	i=$((i + 1))
	sleep 1
done
if [ "$i" -ge "$READY_TIMEOUT" ]; then
	printf '\nservice never answered /healthz on %s\n' "$BASE_URL" >&2
	printf 'start it first:  make up   (compose)  or  make run   (local)\n' >&2
	exit 2
fi

# ---------------------------------------------------------------------------
# identity: log in, then prove the control plane is closed without a session
# ---------------------------------------------------------------------------

section 'identity & session'
http GET /api/auth/config
expect 'auth config returns 200' 200 "$STATUS"

# Without a credential nothing in the control plane answers.
http_anon GET /api/resources
expect 'the control plane is closed without a session' 401 "$STATUS"
expect 'the code on the wire is unauthenticated' unauthenticated "$(jget code)"

# Kept in a variable because the checkpoint section logs in a second time: a
# restore puts the database back, which drops the session it created.
LOGIN_BODY=$(printf '{"email":"%s","password":"%s"}' "$TEST_EMAIL" "$TEST_PASSWORD")
if [ -z "$SESSION_TOKEN" ]; then
	http POST /api/auth/login "$LOGIN_BODY"
	if [ "$STATUS" != "200" ]; then
		# A fresh instance has no account yet: register the first one, which
		# becomes the owner and therefore holds every permission.
		http POST /api/auth/register "$(printf '{"email":"%s","password":"%s","name":"smoke"}' "$TEST_EMAIL" "$TEST_PASSWORD")"
		case "$STATUS" in
		201)
			pass "registered the first account ($TEST_EMAIL)"
			http POST /api/auth/login "$LOGIN_BODY"
			;;
		409) skip 'register' 'the account already exists' ;;
		esac
	fi
	expect 'login returns 200' 200 "$STATUS"
else
	skip 'login' 'AGENTHUB_SESSION_TOKEN was supplied'
fi

http GET /api/auth/me
expect 'me returns 200' 200 "$STATUS"
expect 'me reports the account' "$TEST_EMAIL" "$(jget user.email)"
case "$(jget permissions)" in
*'resources:read'*) pass 'the session carries resources:read' ;;
*) fail 'the session carries resources:read' "$(jget permissions)" ;;
esac
http GET /api/auth/sessions
expect 'my sessions return 200' 200 "$STATUS"

http GET /healthz
expect 'healthz reports a session' session "$(jget auth)"

# The demo resources are opt-in (AGENTHUB_SEED_DEMO=true), and this script talks
# to them. Fail here with the fix instead of drowning in failed assertions.
http GET /api/resources
if [ "$STATUS" != "200" ] || ! grep -q 'demo-mcp' "$BODY" || ! grep -q 'demo-agent' "$BODY"; then
	printf '\nthe demo resources (demo-* ) are not registered on %s\n' "$BASE_URL" >&2
	printf 'start the demo stack first:\n' >&2
	printf '  AGENTHUB_SEED_DEMO=true make up     # compose\n' >&2
	printf '  AGENTHUB_SEED_DEMO=true make run    # local\n\n' >&2
	exit 2
fi

# ---------------------------------------------------------------------------
# health & discovery
# ---------------------------------------------------------------------------

section 'health & discovery'
# Read health explicitly: the preflight above left /api/resources in the buffer.
http GET /healthz
expect 'healthz returns 200' 200 "$STATUS"
expect 'healthz status is ok' ok "$(jget status)"
VERSION=$(jget version)
COMMIT=$(jget commit)
case "$VERSION" in
v* | dev) pass "healthz version is a release or the dev sentinel ($VERSION)" ;;
*) fail 'healthz version' "unexpected value [$VERSION]" ;;
esac
if [ "$COMMIT" = "unknown" ]; then
	skip 'healthz commit' 'unstamped build, expected for a local `make run`'
else
	pass "healthz reports commit $COMMIT"
fi
expect 'healthz reports the database up' up "$(jget database)"
expect 'healthz carries the A2A protocol version' 0.2.0 "$(jget protocolVersion)"
expect_match 'healthz lists the A2A methods' 'message/send' "$(jget methods)"
LEGACY=$(jget legacyMethods)

http GET /.well-known/agent.json
expect 'agent card responds 200' 200 "$STATUS"
expect 'agent card name' agenthub "$(jget name)"
expect 'agent card protocol version' 0.2.0 "$(jget protocolVersion)"
expect_match 'agent card url honours the request host' "$BASE_URL" "$(jget url)"
if [ "$(jlen skills)" -gt 0 ]; then
	pass "agent card exposes $(jlen skills) seeded skills"
else
	fail 'agent card skills' 'no skill advertised'
fi

http GET /a2a/agent-card
expect 'compatibility path /a2a/agent-card responds 200' 200 "$STATUS"
expect 'compatibility path serves the same card' agenthub "$(jget name)"

http GET /.well-known/agent.json '' 'X-Forwarded-Proto: https'
expect_match 'a forwarded proto is honoured' 'https://' "$(jget url)"

# ---------------------------------------------------------------------------
# control plane: resources
# ---------------------------------------------------------------------------

section 'control plane: resources'
http GET /api/resources
expect 'list resources returns 200' 200 "$STATUS"
expect_match 'seeded resources are listed' 'demo-mcp' "$(cat "$BODY")"
expect_match 'counts are reported per kind' '"model"' "$(jget counts)"
expect_match 'health counts are reported' 'healthy' "$(jget health)"

http GET '/api/resources?kind=model&q=model'
expect 'a valid kind filter returns 200' 200 "$STATUS"
http GET '/api/resources?kind=nope'
expect 'an invalid kind filter is rejected' 400 "$STATUS"
expect 'the rejection explains itself' invalid_kind "$(jget code)"

NEW_ID="test-api-$(date +%s)"
http POST /api/resources "$(printf '{"id":"%s","kind":"model","name":"test-api model","version":"9.9.9"}' "$NEW_ID")"
expect 'create resource returns 201' 201 "$STATUS"
expect 'created resource id' "$NEW_ID" "$(jget id)"
expect 'created resource kind' model "$(jget kind)"
expect 'a new resource starts unknown' unknown "$(jget health)"

http POST /api/resources "$(printf '{"id":"%s","kind":"agent","name":"duplicate"}' "$NEW_ID")"
expect 'a duplicate id is rejected' 400 "$STATUS"
http POST /api/resources '{"kind":"nope","name":"x"}'
expect 'an invalid kind is rejected' 400 "$STATUS"
expect 'invalid kind explains itself' invalid_kind "$(jget code)"
http POST /api/resources '{"kind":"skill","name":"   "}'
expect 'an empty name is rejected' 400 "$STATUS"
http POST /api/resources '{"kind":'
expect 'malformed JSON is rejected' 400 "$STATUS"

http GET "/api/resources/$NEW_ID"
expect 'get resource returns 200' 200 "$STATUS"
expect 'get resource id' "$NEW_ID" "$(jget id)"

http PUT "/api/resources/$NEW_ID" '{"kind":"model","name":"test-api renamed","health":"healthy"}'
expect 'update resource returns 200' 200 "$STATUS"
expect 'update keeps the id' "$NEW_ID" "$(jget id)"
expect 'update applies the new name' 'test-api renamed' "$(jget name)"
expect 'update applies health' healthy "$(jget health)"
http GET "/api/resources/$NEW_ID"
expect 'the update persisted' 'test-api renamed' "$(jget name)"

http POST "/api/resources/$NEW_ID/health" '{"health":"degraded"}'
expect 'the health probe endpoint returns 200' 200 "$STATUS"
http POST "/api/resources/$NEW_ID/health" '{"health":"purple"}'
expect 'an unknown health value is rejected' 400 "$STATUS"

http GET /api/resources/nope
expect 'an unknown resource returns 404' 404 "$STATUS"
expect '404 carries not_found' not_found "$(jget code)"

http GET /api/resources
expect 'an authenticated request is accepted' 200 "$STATUS"
http_anon GET /api/resources
expect 'a request without a session is rejected' 401 "$STATUS"
expect 'the rejection carries unauthenticated' unauthenticated "$(jget code)"

# ---------------------------------------------------------------------------
# control plane: registry governance (namespaces, approval, upstream sync)
# ---------------------------------------------------------------------------

section 'control plane: registry governance'

CURATED_NS="test-api-ns-$(date +%s)"

http POST /api/registries "$(printf '{"id":"%s","name":"Curated","description":"test-api","auto_approve":false}' "$CURATED_NS")"
expect 'create namespace returns 201' 201 "$STATUS"
expect 'the namespace keeps its id' "$CURATED_NS" "$(jget id)"
expect 'auto_approve round trips as false' false "$(jget auto_approve)"
http GET /api/registries
expect_match 'the new namespace is listed' "$CURATED_NS" "$(cat "$BODY")"
http POST /api/registries '{"id":"Not A Slug","name":"x"}'
expect 'a namespace id that is not a slug is rejected' 400 "$STATUS"
expect 'the rejection explains itself' invalid_namespace "$(jget code)"
http DELETE "/api/registries/default"
expect 'the default namespace cannot be deleted' 400 "$STATUS"

# A record in a curated namespace starts as a draft: it must not appear in the
# catalogue a consumer sees until a curator approves it.
CURATED_ID="test-api-curated-$(date +%s)"
http POST /api/resources "$(printf '{"id":"%s","kind":"skill","name":"curated skill","version":"1.0.0","namespace":"%s"}' "$CURATED_ID" "$CURATED_NS")"
expect 'register into a curated namespace returns 201' 201 "$STATUS"
expect 'a new record starts as a draft' draft "$(jget status)"
http GET "/api/resources?namespace=$CURATED_NS&status=approved"
expect_no 'a draft is not part of the approved catalogue' "$CURATED_ID" "$(cat "$BODY")"
http POST "/api/resources/$CURATED_ID/status" '{"status":"approved"}'
expect 'draft -> approved is refused' 400 "$STATUS"
expect 'the refusal explains itself' invalid_status_transition "$(jget code)"
http POST "/api/resources/$CURATED_ID/status" '{"status":"pending"}'
expect 'submit for review returns 200' 200 "$STATUS"
expect 'the record is pending' pending "$(jget status)"
http GET /api/registries/approvals
expect_match 'the pending record is in the curator queue' "$CURATED_ID" "$(cat "$BODY")"
http POST "/api/resources/$CURATED_ID/status" '{"status":"approved"}'
expect 'approve returns 200' 200 "$STATUS"
http GET "/api/resources?namespace=$CURATED_NS&status=approved"
expect_match 'the approved record is discoverable' "$CURATED_ID" "$(cat "$BODY")"

# The dedup key is (namespace, name, version): the same name at a new version is
# a new record, the same name at the same version is not.
http POST /api/resources "$(printf '{"kind":"skill","name":"curated skill","version":"2.0.0","namespace":"%s"}' "$CURATED_NS")"
expect 'the same name at a new version is accepted' 201 "$STATUS"
CURATED_V2_ID=$(jget id)
http POST /api/resources "$(printf '{"kind":"skill","name":"curated skill","version":"1.0.0","namespace":"%s"}' "$CURATED_NS")"
expect 'the dedup key rejects a repeat' 400 "$STATUS"
expect_match 'the rejection names the conflict' 'resource_already_exists' "$(jget code)"

# An upstream registry source. The fixture flag keeps it offline, so this
# section never reaches the network.
SRC_ID="test-api-src-$(date +%s)"
http POST /api/registry-sources "$(printf '{"id":"%s","kind":"mcp","name":"test-api source","url":"https://registry.test/servers","namespace":"%s","fixture":true,"enabled":true}' "$SRC_ID" "$CURATED_NS")"
expect 'create source returns 201' 201 "$STATUS"
expect 'a fresh source claims no sync' '' "$(jget last_sync_status)"
http POST "/api/registry-sources/$SRC_ID/sync" '{}'
expect 'sync returns 200' 200 "$STATUS"
expect 'the sync reports success' ok "$(jget status)"
expect_match 'the fixture imported its records' '3' "$(jget imported)"
http POST "/api/registry-sources/$SRC_ID/sync" '{}'
expect 'a second sync returns 200' 200 "$STATUS"
expect 'a second sync imports nothing new' '0' "$(jget imported)"
expect_match 'a second sync refreshes instead' '3' "$(jget updated)"
http GET /api/registry-sources
expect_match 'the sync outcome is recorded' 'ok' "$(jget sources.0.last_sync_status)"

http DELETE "/api/registry-sources/$SRC_ID"
expect 'delete source returns 204' 204 "$STATUS"
http DELETE "/api/resources/$CURATED_ID"
expect 'delete curated resource returns 204' 204 "$STATUS"
http DELETE "/api/resources/$CURATED_V2_ID"
expect 'delete the second version returns 204' 204 "$STATUS"
http DELETE "/api/registries/$CURATED_NS"
expect 'delete namespace returns 204' 204 "$STATUS"

# ---------------------------------------------------------------------------
# exports
# ---------------------------------------------------------------------------

section 'exports'
http GET /api/export/mcp
expect 'MCP export returns 200' 200 "$STATUS"
expect_match 'MCP export carries the resource kind' 'mcp' "$(jget servers)"
http GET /api/export/agent-card
expect 'agent card export returns 200' 200 "$STATUS"
expect_match 'agent card export carries skills' 'resourceKind' "$(jget skills)"

# ---------------------------------------------------------------------------
# A2A data plane
# ---------------------------------------------------------------------------

section 'A2A: send, get, stream'
TEXT='look up last week leave records'
rpc 1 message/send "$(msg "$SENT_RESOURCE" '' "$TEXT")"
expect 'message/send returns 200' 200 "$STATUS"
expect 'message/send carries no rpc error' '' "$(jget error)"
TASK_ID=$(jget result.id)
if [ -n "$TASK_ID" ]; then
	pass "message/send created task $TASK_ID"
else
	fail 'message/send created a task' "$(head -c 200 "$BODY")"
fi
expect 'message/send completes the task' completed "$(jget result.status.state)"

rpc 2 tasks/get "$(printf '{"id":"%s"}' "$TASK_ID")"
expect 'tasks/get returns 200' 200 "$STATUS"
expect 'tasks/get returns the same task' "$TASK_ID" "$(jget result.id)"
if [ "$(jlen result.artifacts)" -gt 0 ]; then
	pass "the run produced $(jlen result.artifacts) artifact(s)"
else
	fail 'task artifacts' 'nothing was produced'
fi

rpc 3 tasks/get "$(printf '{"id":"%s","historyLength":1}' "$TASK_ID")"
expect 'historyLength trims the transcript' 1 "$(jlen result.history)"

sse POST /a2a "$(printf '{"jsonrpc":"2.0","id":7,"method":"message/stream","params":%s}' \
	"$(msg "$SENT_RESOURCE" ', "flow":[{"op":"then","resource":"demo-skill"},{"op":"then","resource":"demo-mcp"}]' 'apply half-day annual leave tomorrow afternoon')")"
expect_match 'message/stream emits status events' 'task_status_update' "$STREAM"
expect_match 'message/stream emits artifacts' 'task_artifact_update' "$STREAM"
expect_match 'message/stream ends in completed' '"state":"completed"' "$STREAM"
expect_match 'every frame echoes the request id' '"id":7' "$STREAM"
expect_no 'streaming has no sentinel frame' 'DONE]' "$STREAM"

# ---------------------------------------------------------------------------
# human in the loop: park, resume, cancel
# ---------------------------------------------------------------------------

section 'A2A: input-required, resume, cancel'
rpc 10 message/send "$(msg "$PARK_RESOURCE" '' 'approve the change')"
expect 'the agent parks on approve/confirm' input-required "$(jget result.status.state)"
PARKED=$(jget result.id)
rpc 11 tasks/resume "$(printf '{"id":"%s","message":{"role":"user","parts":[{"type":"text","text":"approved"}]}}' "$PARKED")"
expect 'tasks/resume returns 200' 200 "$STATUS"
expect 'the resumed task completes' completed "$(jget result.status.state)"
rpc 12 tasks/resume "$(printf '{"id":"%s","message":{"role":"user","parts":[{"type":"text","text":"approved"}]}}' "$PARKED")"
expect_ere 'resuming a terminal task is rejected' 'task_not_resumable|invalid_state_transition' "$(jget error)"

rpc 13 message/send "$(msg "$PARK_RESOURCE" '' 'approve the change')"
PARKED2=$(jget result.id)
expect 'a second task parks as well' input-required "$(jget result.status.state)"
rpc 14 tasks/cancel "$(printf '{"id":"%s"}' "$PARKED2")"
expect 'tasks/cancel returns 200' 200 "$STATUS"
expect 'the cancelled task is terminal' canceled "$(jget result.status.state)"
rpc 15 tasks/cancel "$(printf '{"id":"%s"}' "$PARKED2")"
expect_ere 'a terminal task cannot be cancelled twice' 'task_not_cancelable|invalid_state_transition' "$(jget error)"

# ---------------------------------------------------------------------------
# push notifications
# ---------------------------------------------------------------------------

section 'A2A: push notifications'
rpc 20 tasks/pushNotification/set "$(printf '{"taskId":"%s","pushNotificationConfig":{"url":"http://127.0.0.1:9/callback"}}' "$TASK_ID")"
expect 'a push config is accepted' '' "$(jget error)"
rpc 21 tasks/pushNotification/get "$(printf '{"id":"%s"}' "$TASK_ID")"
expect 'the push config reads back' 200 "$STATUS"
expect 'reading it back answers no error' '' "$(jget error)"
rpc 22 tasks/pushNotification/set "$(printf '{"taskId":"%s","pushNotificationConfig":{}}' "$TASK_ID")"
expect 'an empty push url is rejected' invalid_params "$(jget error.message)"

# ---------------------------------------------------------------------------
# error contract
# ---------------------------------------------------------------------------

section 'error contract'
# The machine readable code is carried in error.message; error.code is its
# JSON-RPC numeric form.
rpc 30 tasks/get '{"id":"task_missing"}'
expect 'an unknown task maps to task_not_found' task_not_found "$(jget error.message)"
rpc 31 tasks/get '{}'
expect 'a missing task id maps to task_not_found' task_not_found "$(jget error.message)"
rpc 32 nope '{}'
expect 'an unknown method maps to method_not_found' method_not_found "$(jget error.message)"
http POST /a2a 'not json'
expect 'malformed JSON returns 400' 400 "$STATUS"
expect 'malformed JSON maps to parse_error' parse_error "$(jget error.message)"
rpc 33 message/send '{"message":{"role":"user","parts":[{"type":"text","text":"hello"}]},"metadata":{"resourceId":"nope"}}'
expect 'an unknown resource maps to resource_not_found' resource_not_found "$(jget error.message)"
http GET /healthz
expect 'the service still answers after the error paths' 200 "$STATUS"

if [ "$LEGACY" = "true" ]; then
	rpc 34 tasks/send "$(msg "$SENT_RESOURCE" '' "$TEXT")"
	expect 'the v0.1 alias tasks/send is accepted' completed "$(jget result.status.state)"
else
	skip 'v0.1 aliases' 'legacy methods are disabled in this deployment'
fi

# ---------------------------------------------------------------------------
# control plane: tasks
# ---------------------------------------------------------------------------

section 'control plane: tasks'
http GET /api/tasks
expect 'list tasks returns 200' 200 "$STATUS"
expect_match 'the finished task is listed' "$TASK_ID" "$(cat "$BODY")"
http GET "/api/tasks/$TASK_ID"
expect 'get task returns 200' 200 "$STATUS"
expect 'get task id' "$TASK_ID" "$(jget id)"
http GET "/api/tasks/$TASK_ID/spans"
expect 'task spans return 200' 200 "$STATUS"
if [ "$(jlen spans)" -gt 0 ]; then
	pass "the timeline carries $(jlen spans) span(s)"
else
	fail 'task spans' 'no span was recorded'
fi
http GET /api/tasks/nope
expect 'an unknown task returns 404' 404 "$STATUS"
http GET /api/tasks/nope/spans
expect 'spans of an unknown task return 404' 404 "$STATUS"

sse GET "/api/tasks/$TASK_ID/events"
expect_match 'event replay streams status frames' 'task_status_update' "$STREAM"
expect_match 'event replay streams span frames' 'task_span' "$STREAM"
http GET /api/tasks/nope/events
expect 'events of an unknown task return 404' 404 "$STATUS"

rpc 40 message/send "$(msg "$PARK_RESOURCE" '' 'approve the change')"
REST_PARKED=$(jget result.id)
http POST "/api/tasks/$REST_PARKED/cancel" ''
expect 'the console can cancel a parked task' 200 "$STATUS"
expect 'the REST cancel is terminal' canceled "$(jget status.state)"
http POST "/api/tasks/$REST_PARKED/resume" '{"message":{"role":"user","parts":[{"type":"text","text":"approved"}]}}'
expect 'resuming a cancelled task is a client error' 400 "$STATUS"
http POST /api/tasks/nope/cancel ''
expect 'cancelling an unknown task returns 404' 404 "$STATUS"

# ---------------------------------------------------------------------------
# administration: users, roles, groups, resource grants
# ---------------------------------------------------------------------------

section 'administration: users'
http GET /api/users
expect 'list users returns 200' 200 "$STATUS"
http POST /api/users '{"email":"smoke-member@example.com","password":"smoke-member-pass","name":"smoke member","roles":[],"groups":[]}'
case "$STATUS" in
201) pass 'the console can create a user' ;;
409) skip 'create user' 'smoke-member@example.com already exists' ;;
*) fail 'create user' "status $STATUS" ;;
esac
MEMBER_ID=$(jget user.id)
if [ -z "$MEMBER_ID" ]; then
	http GET '/api/users?q=smoke-member'
	MEMBER_ID=$(jget users.0.id)
fi
http GET "/api/users/$MEMBER_ID"
expect 'get user returns 200' 200 "$STATUS"
expect 'get user reports the address' smoke-member@example.com "$(jget user.email)"
http PUT "/api/users/$MEMBER_ID" '{"name":"smoke member renamed"}'
expect 'update user returns 200' 200 "$STATUS"
expect 'the new name is stored' 'smoke member renamed' "$(jget user.name)"
http GET '/api/users?q=smoke-member'
expect_match 'searching users finds the account' "$MEMBER_ID" "$(cat "$BODY")"

section 'administration: roles and permissions'
http GET /api/permissions
expect 'permissions are listed' 200 "$STATUS"
expect_match 'the vocabulary carries resources:read' 'resources:read' "$(cat "$BODY")"
http GET /api/roles
expect 'roles are listed' 200 "$STATUS"
ROLE_NAME="smoke-role-$$-$(date +%s)"
http POST /api/roles "$(printf '{"name":"%s","permissions":["resources:read"]}' "$ROLE_NAME")"
expect 'a role can be created' 201 "$STATUS"
ROLE_ID=$(jget role.id)
if [ -z "$ROLE_ID" ]; then
	fail 'a role can be created' "no role id in the response ($(cat "$BODY"))"
else
	http PUT "/api/roles/$ROLE_ID" "$(printf '{"name":"%s","permissions":["resources:read","tasks:read"]}' "$ROLE_NAME")"
	expect 'the permission matrix can be saved' 200 "$STATUS"
	expect_match 'the saved matrix carries tasks:read' 'tasks:read' "$(jget role.permissions)"
	http DELETE "/api/roles/$ROLE_ID"
	expect 'a custom role can be deleted' 204 "$STATUS"
fi
# The seeded roles are the safety net: deleting one must be refused.
http GET /api/roles
BUILTIN_ROLE=$(jget roles.0.id)
http DELETE "/api/roles/$BUILTIN_ROLE"
if [ "$STATUS" = "204" ]; then
	fail 'a built-in role cannot be deleted' 'the delete succeeded'
else
	pass 'a built-in role cannot be deleted'
fi
GROUP_NAME="smoke-group-$$-$(date +%s)"
http POST /api/groups "$(printf '{"name":"%s","roles":[]}' "$GROUP_NAME")"
expect 'a group can be created' 201 "$STATUS"
GROUP_ID=$(jget group.id)
http GET /api/groups
expect 'groups are listed' 200 "$STATUS"
expect_match 'the new group is listed' "$GROUP_NAME" "$(cat "$BODY")"

section 'administration: resource grants'
http POST /api/resources '{"kind":"model","name":"smoke-granted-model"}'
expect 'a resource to grant can be created' 201 "$STATUS"
GRANT_RESOURCE=$(jget id)
GRANT_NAME=$(jget name)
# Before any grant exists the resource is part of the public catalogue.
http_anon GET /.well-known/agent.json
expect_match 'an unrestricted resource is advertised' "$GRANT_NAME" "$(cat "$BODY")"
http POST /api/grants "$(printf '{"resourceId":"%s","principalKind":"user","principalId":"%s","level":"read"}' "$GRANT_RESOURCE" "$MEMBER_ID")"
case "$STATUS" in
201 | 409) pass 'a grant can be created' ;;
*) fail 'create grant' "status $STATUS ($(jget code))" ;;
esac
# The whole point of a grant: discovery stops publishing the resource.
http_anon GET /.well-known/agent.json
expect_no 'a granted resource is hidden from discovery' "$GRANT_NAME" "$(cat "$BODY")"
http GET "/api/grants?resource=$GRANT_RESOURCE"
expect 'grants are listed for a resource' 200 "$STATUS"
GRANT_ID=$(jget grants.0.id)
if [ -z "$GRANT_ID" ]; then
	fail 'the grant is listed' "no grant id in the response ($(cat "$BODY"))"
else
	http DELETE "/api/grants/$GRANT_ID"
	expect 'a grant can be revoked' 204 "$STATUS"
fi
http DELETE "/api/resources/$GRANT_RESOURCE"
expect 'the granted resource is cleaned up' 204 "$STATUS"
http DELETE "/api/users/$MEMBER_ID"
expect 'a user can be disabled' 204 "$STATUS"

# ---------------------------------------------------------------------------
# database checkpoints
#
# Only asserted when the instance has a backup directory: without one the
# feature is off by design, which is a supported configuration rather than a
# failure. A real restore is behind an opt-in of its own — it rewrites every
# table, which would invalidate the session the sections above (and the cleanup
# below) rely on.
# ---------------------------------------------------------------------------

section 'database checkpoints'
http GET /api/backups
if [ "$(jget enabled)" = "true" ]; then
	expect 'the checkpoint catalogue is listed' 200 "$STATUS"
	expect_match 'the overview names the engine' '"engine"' "$(cat "$BODY")"

	http GET /api/backups/schedule
	expect 'the schedule is readable' 200 "$STATUS"
	http PUT /api/backups/schedule '{"mode":"hourly"}'
	expect 'a schedule that cannot run is refused' 400 "$STATUS"
	expect 'the refusal names the reason' backup_schedule_invalid "$(jget code)"

	http POST /api/backups '{"label":"test-api.sh"}'
	expect 'a checkpoint can be taken' 201 "$STATUS"
	CHECKPOINT_ID=$(jget checkpoint.id)
	expect 'the checkpoint completed' ok "$(jget checkpoint.status)"
	if [ -n "$CHECKPOINT_ID" ]; then
		http GET "/api/backups/$CHECKPOINT_ID"
		expect 'the checkpoint can be read back' 200 "$STATUS"
		http GET "/api/backups/$CHECKPOINT_ID/download"
		expect 'the archive can be downloaded' 200 "$STATUS"
		# The last guardrail before every table is rewritten.
		http POST "/api/backups/$CHECKPOINT_ID/restore" '{}'
		expect 'a restore nobody confirmed is refused' 400 "$STATUS"
		expect 'the refusal asks for the id' restore_confirm_required "$(jget code)"

		if [ "${AGENTHUB_TEST_BACKUP_RESTORE:-}" = "true" ]; then
			http POST "/api/backups/$CHECKPOINT_ID/restore" "$(printf '{"confirm":"%s"}' "$CHECKPOINT_ID")"
			expect 'a confirmed restore runs' 200 "$STATUS"
			http GET /api/backups
			expect_match 'the restore left a rollback checkpoint' 'pre-restore' "$(cat "$BODY")"
			# The session was just replaced by an older one: log back in so the
			# cleanup below still talks to a live session.
			http POST /api/auth/login "$LOGIN_BODY"
		else
			skip 'restore' 'set AGENTHUB_TEST_BACKUP_RESTORE=true: it rewrites every table and invalidates the session the other sections use'
		fi

		http DELETE "/api/backups/$CHECKPOINT_ID"
		expect 'a checkpoint can be deleted' 204 "$STATUS"
		http GET "/api/backups/$CHECKPOINT_ID"
		expect 'the deleted checkpoint is gone' 404 "$STATUS"
	else
		fail 'a checkpoint can be taken' "no checkpoint id in the response ($(cat "$BODY"))"
	fi
else
	skip 'database checkpoints' "$(jget reason): set AGENTHUB_BACKUP_DIR and restart to exercise them"
fi

# ---------------------------------------------------------------------------
# cleanup + console bundle
# ---------------------------------------------------------------------------

section 'cleanup & console'
http DELETE "/api/resources/$NEW_ID"
expect 'delete resource returns 204' 204 "$STATUS"
http GET "/api/resources/$NEW_ID"
expect 'the deleted resource is gone' 404 "$STATUS"

http GET /
if [ "$STATUS" = "200" ]; then
	expect_match 'the bundle serves html' '<html' "$(cat "$BODY")"
	http GET /index.html
	# http.FileServer redirects /index.html to ./ before serving it.
	case "$STATUS" in
	200 | 301) pass 'index.html is reachable' ;;
	*) fail 'index.html' "status $STATUS" ;;
	esac
else
	skip 'console bundle' 'none built here; run `make web-build` or test the image'
fi

# ---------------------------------------------------------------------------
# summary
# ---------------------------------------------------------------------------

printf '\n%s passed, %s failed, %s skipped\n' "$PASS" "$FAIL" "$SKIP"
if [ "$FAIL" -gt 0 ]; then
	printf 'agenthub API test FAILED\n' >&2
	exit 1
fi
printf 'agenthub API test passed\n'
