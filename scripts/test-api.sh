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
#   ./scripts/test-api.sh                       # http://127.0.0.1:3500, open mode
#   BASE_URL=https://hub.example.com ./scripts/test-api.sh
#   AGENTHUB_ADMIN_TOKEN=<token> ./scripts/test-api.sh
#   MAX_TIME=60 ./scripts/test-api.sh           # per-request curl timeout
#
# Start the stack first: `make up` (compose) or `make run` (local). The seeded
# resources (demo-model / demo-agent / demo-mcp / demo-skill / demo-cli) are what
# the A2A calls target, so run it against a freshly seeded instance.
#
# Requires: curl, python3 (used only to pick values out of JSON responses).
set -u

BASE_URL=${BASE_URL:-http://127.0.0.1:${AGENTHUB_HTTP_PORT:-3500}}
BASE_URL=${BASE_URL%/}
ADMIN_TOKEN=${AGENTHUB_ADMIN_TOKEN:-}
MAX_TIME=${MAX_TIME:-20}
READY_TIMEOUT=${READY_TIMEOUT:-30}

TMP=$(mktemp -d)
BODY="$TMP/body"
CURL_ERR="$TMP/curl.err"
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

# http <method> <path> [body] [extra header]
http() {
	__m=$1
	__p=$2
	__d=${3:-}
	__h=${4:-}
	set -- -s -S --max-time "$MAX_TIME" -X "$__m" -o "$BODY" -w '%{http_code}' "$BASE_URL$__p"
	[ -n "$ADMIN_TOKEN" ] && set -- "$@" -H "Authorization: Bearer $ADMIN_TOKEN"
	case "$__m" in
	POST | PUT) set -- "$@" -H 'Content-Type: application/json' ;;
	esac
	[ -n "$__h" ] && set -- "$@" -H "$__h"
	[ -n "$__d" ] && set -- "$@" --data-binary "$__d"
	STATUS=$(curl "$@" 2>"$CURL_ERR") || true
	[ -n "$STATUS" ] || STATUS=000
}

# sse <method> <path> [body] — keep the connection open and capture the frames.
sse() {
	__m=$1
	__p=$2
	__d=${3:-}
	set -- -sN --max-time "$MAX_TIME" -X "$__m" "$BASE_URL$__p"
	[ -n "$ADMIN_TOKEN" ] && set -- "$@" -H "Authorization: Bearer $ADMIN_TOKEN"
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
# health & discovery
# ---------------------------------------------------------------------------

section 'health & discovery'
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

if [ -n "$ADMIN_TOKEN" ]; then
	http GET /api/resources
	expect 'an authenticated request is accepted' 200 "$STATUS"
	__token=$ADMIN_TOKEN
	ADMIN_TOKEN=
	http GET /api/resources
	expect 'a request without the token is rejected' 401 "$STATUS"
	expect 'the rejection carries unauthorized' unauthorized "$(jget code)"
	ADMIN_TOKEN=$__token
fi

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
