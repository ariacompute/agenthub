#!/usr/bin/env sh
# compose-ports.sh — print every port the agenthub compose stack publishes on the
# host, together with what the port is for.
#
# Compose has no post-up hook, so `make up` runs this right after
# `docker compose up -d`; `make ports` prints the same table for a stack that is
# already running. Host ports honour the same overrides as docker-compose.yml
# (AGENTHUB_HTTP_PORT, POSTGRES_PORT) when nothing is up yet.
#
# The endpoint column mirrors internal/handlers.Listeners()
# (internal/handlers/listen.go) — keep both in sync when a route changes.
set -eu

cd "$(dirname "$0")/.."

AH_CONTAINER_PORT=3500
PG_CONTAINER_PORT=5432
WEB_CONTAINER_PORT=5173

AH_HOST_PORT="${AGENTHUB_HTTP_PORT:-3500}"
PG_HOST_PORT="${POSTGRES_PORT:-5434}"
WEB_HOST_PORT=5173

# The mapping of a running container wins over the default above.
published() {
	docker compose port "$1" "$2" 2>/dev/null | sed -n '1s/.*://p' | tr -d '\r\n'
}

# Health first, plain container state when the image defines no healthcheck.
# docker inspect prints a stray newline when the container does not exist, so
# the value is trimmed and an empty result means "down".
state() {
	s=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$1" 2>/dev/null | tr -d '\r\n' || true)
	if [ -n "$s" ]; then echo "$s"; else echo down; fi
}

# `docker compose up -d` returns long before the healthchecks do, so give each
# service its own grace period before the table is printed.
wait_healthy() {
	i=0
	while [ "$i" -lt "${PORTS_HEALTH_TIMEOUT:-30}" ]; do
		if [ "$(state "$1")" = "healthy" ]; then
			return 0
		fi
		i=$((i + 1))
		sleep 1
	done
	return 1
}

ah=$(published agenthub "$AH_CONTAINER_PORT" || true)
pg=$(published postgres "$PG_CONTAINER_PORT" || true)
web=$(published web "$WEB_CONTAINER_PORT" || true)

[ -n "$ah" ] || ah="$AH_HOST_PORT"
[ -n "$pg" ] || pg="$PG_HOST_PORT"
[ -n "$web" ] || web="$WEB_HOST_PORT"

ahState=$(state agenthub-server)
pgState=$(state agenthub-postgres)

if [ "$ahState" = "down" ] && [ "$pgState" = "down" ]; then
	echo "no running agenthub stack detected"
	echo
	echo "  start it with:  make up        # docker compose up -d, then prints this table"
	exit 0
fi

wait_healthy agenthub-server || true
wait_healthy agenthub-postgres || true
ahState=$(state agenthub-server)
pgState=$(state agenthub-postgres)
webState=$(state agenthub-web)

printf '\nagenthub listening ports\n\n'
printf '  %-16s %-16s %-12s %s\n' SERVICE 'HOST -> CTR' STATE PURPOSE
printf '  %-16s %-16s %-12s %s\n' \
	agenthub-server "$ah -> $AH_CONTAINER_PORT" "$ahState" \
	'the only HTTP listener: console + A2A + control plane + healthz'
printf '  %-16s %-16s %-12s %s\n' \
	postgres "$pg -> $PG_CONTAINER_PORT" "$pgState" \
	'the only state: resources, tasks, spans, events, push records'
if [ "$webState" != "down" ]; then
	printf '  %-16s %-16s %-12s %s\n' \
		'web (dev)' "$web -> $WEB_CONTAINER_PORT" "$webState" \
		'console dev server with hot reload (--profile dev)'
fi

printf '\n  %-52s %s\n' ENDPOINT USE
printf '  %-52s %s\n' "http://127.0.0.1:${ah}/" 'Vue 3 console (bundle baked into the image)'
printf '  %-52s %s\n' "http://127.0.0.1:${ah}/.well-known/agent.json" 'A2A Agent Card (discovery)'
printf '  %-52s %s\n' "POST http://127.0.0.1:${ah}/a2a" 'A2A JSON-RPC: message/send, message/stream, tasks/*'
printf '  %-52s %s\n' "http://127.0.0.1:${ah}/api/*" 'control plane: resources, tasks, events, push records'
printf '  %-52s %s\n' "http://127.0.0.1:${ah}/healthz" 'health probe, also the container healthcheck'
printf '  %-52s %s\n' "postgres://agenthub@localhost:${pg}/agenthub" 'host DSN (inside compose it is postgres:5432)'
if [ "$webState" != "down" ]; then
	printf '  %-52s %s\n' "http://127.0.0.1:${web}/" 'console dev server (proxies /api, /a2a, /healthz)'
fi

printf '\n  next:  curl -fsS http://127.0.0.1:%s/healthz\n' "$ah"
printf '         docker compose logs -f agenthub\n'
printf '         docker compose down      # state survives in the agenthub-pgdata volume\n\n'
