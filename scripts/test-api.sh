#!/usr/bin/env sh
# test-api.sh — compatibility entry point for the user-facing E2E API suite.
#
# The implementation now lives in ../e2e (bun 1.4.4+ and built-in fetch, so it
# runs the same way on Windows, macOS and Linux without curl/python3). This shim
# keeps the existing references in README.md, AGENTS.md, docker-compose.yml and
# the Go comments working. See e2e/README.md for switches and report format.
#
#   ./scripts/test-api.sh                       # http://127.0.0.1:3500
#   BASE_URL=https://hub.example.com ./scripts/test-api.sh
#   AGENTHUB_TEST_EMAIL=me@example.com AGENTHUB_TEST_PASSWORD='…' ./scripts/test-api.sh
#   AGENTHUB_API_KEY=ahk_xxx ./scripts/test-api.sh
#   ./scripts/test-api.sh --coverage-only       # endpoint coverage guard only
#
# The service must already be running; this script never starts or stops it.
# An owner-equivalent identity is required (bootstrap administrator or a
# full-scope API key) because the run writes to every domain.
#
set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if ! command -v bun >/dev/null 2>&1; then
  echo "test-api.sh: bun >= 1.4.4 is required (see e2e/README.md)" >&2
  exit 2
fi

cd "$here/../e2e"
exec bun run api/index.js "$@"
