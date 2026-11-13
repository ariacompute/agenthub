#!/usr/bin/env bash
# deploy-skill.sh — materialise one of the example skills on WSL2 / Ubuntu.
#
# Usage:
#   ./deploy-skill.sh <skill> [command] [options]
#
# Skills (from resources/skill-<skill>-ubuntu.json):
#   anthropics-pdf        git clone anthropics/skills, take the pdf/ directory
#   skillssh-find-skills  npx skills add vercel-labs/skills into the project
#
# Commands (default: start):
#   start    acquire the directory at the registered file:// endpoint and publish
#            a stable symlink in the workdir
#   stop     unpublish: remove that symlink and the state file; never delete the
#            acquired checkout (a skill is files, not a process)
#   restart  stop, then start
#   status   resolved path, SKILL.md presence, log tail (exit 1 when not deployed)
#   smoke    read SKILL.md, print its front matter and verify the declared shape
#
# There is no server to start, no pid and no port: a skill is a directory holding
# SKILL.md. `start` never backgrounds anything, and `stop` only unpublishes.
#
# Options:
#   --path <dir>       override the file:// directory from the catalogue
#   --project <name>   replaces <project> in the catalogue's paths
#   --workdir <dir>    default $HOME/.local/share/agenthub-skill-<skill>
#   --wait <sec>       timeout for the acquisition command, default 120
#   --dry-run          print the resolved plan and change nothing
#   -h | --help        this text
#
# Configuration: repository-root .env first, then .env.example for the keys .env
# leaves out; an already exported variable beats both, a flag beats everything.
# The files are parsed, not sourced. Placeholders in the catalogue are expanded:
#   <user>     -> current username
#   <project>  -> --project, else the current directory's name
# Anything still unresolved is a hard error: no placeholder becomes a path.
#
# Acquisition is an argv array in this script (git clone --depth 1, or
# npx skills add): the catalogue supplies the repository and package, never a
# shell string to eval. metadata.install is printed as a hint, never executed.
#
# Skills need no credentials. This script never talks to agenthub: no login, no
# load.sh, no /probe. The server-side skill connector is still TODO(real), so
# this verifies the local manual only.
#
# --- end of usage ---

set -euo pipefail

SELF="$0"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RES_DIR="$SCRIPT_DIR/resources"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

SKILLS="anthropics-pdf skillssh-find-skills"
COMMANDS="start stop restart status smoke"

SKILL=""
COMMAND=""
WORKDIR=""
WAIT="120"
PATH_ARG=""
PROJECT_ARG=""
DRY_RUN=0

print_help() {
  sed -n '2,/^# --- end of usage ---$/p' "$SELF" |
    sed -e 's/^# \{0,1\}//' -e '/^--- end of usage ---$/d'
}

usage_error() {
  echo "$1" >&2
  echo "supported skills: $SKILLS" >&2
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
    --path) PATH_ARG="${2:-}"; shift ;;
    --project) PROJECT_ARG="${2:-}"; shift ;;
    --workdir) WORKDIR="${2:-}"; shift ;;
    --wait) WAIT="${2:-}"; shift ;;
    --dry-run) DRY_RUN=1 ;;
    start | stop | restart | status | smoke) COMMAND="$1" ;;
    -*) echo "unknown option: $1" >&2; exit 2 ;;
    *)
      if [ -n "$SKILL" ]; then
        echo "unexpected argument: $1 (skill is already '$SKILL')" >&2
        exit 2
      fi
      SKILL="$1"
      ;;
  esac
  shift
done

[ -n "$SKILL" ] || usage_error "no skill given"
case " $SKILLS " in
  *" $SKILL "*) ;;
  *) usage_error "unsupported skill: $SKILL" ;;
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

RES_FILE="$RES_DIR/skill-$SKILL-ubuntu.json"
[ -f "$RES_FILE" ] || {
  echo "missing catalogue entry: $RES_FILE" >&2
  exit 2
}
require python3

# The catalogue is the single source of truth for the endpoint, the repository
# and the package name. The shape of each acquisition stays in this script.
json_spec() {
  python3 - "$RES_FILE" <<'PY' 2>/dev/null || true
import json, sys

with open(sys.argv[1]) as fh:
    doc = json.load(fh)
cfg = (doc.get("kind_config") or {}).get("skill") or {}
md = doc.get("metadata") or {}


def emit(key, value):
    print("%s=%s" % (key, str(value or "").replace("\n", " ")))


emit("ID", doc.get("id"))
emit("ENDPOINT", doc.get("endpoint"))
emit("REPOSITORY", cfg.get("repository") or doc.get("repository"))
emit("PACKAGE", cfg.get("package"))
emit("INSTALL_HINT", md.get("install"))
emit("DISCOVER_HINT", md.get("discover"))
emit("PATH_HINT", md.get("path_hint"))
emit("DOCS", cfg.get("docs") or md.get("docs"))
emit("TRIGGERS", ",".join(cfg.get("triggers") or []))
emit("TOOLS", ",".join(cfg.get("tools") or []))
PY
}

SPEC="$(json_spec)"
spec() {
  printf '%s\n' "$SPEC" | sed -n "s/^$1=//p" | head -n 1 || true
}

USER_NAME="$(id -un)"
PROJECT="${PROJECT_ARG:-$(basename "$PWD")}"

expand() {
  local value="$1"
  value="${value//<user>/$USER_NAME}"
  value="${value//<project>/$PROJECT}"
  printf '%s' "$value"
}

assert_resolved() {
  case "$2" in
    *'<'*'>'*)
      echo "unresolved placeholder in $1: $2" >&2
      echo "pass --path / --project to resolve it" >&2
      exit 2
      ;;
  esac
}

ENDPOINT="$(spec ENDPOINT)"
case "$ENDPOINT" in
  file://*) TARGET="$(expand "${ENDPOINT#file://}")" ;;
  *)
    echo "$RES_FILE endpoint is not a file:// URL: ${ENDPOINT:-<none>}" >&2
    exit 2
    ;;
esac
if [ -n "$PATH_ARG" ]; then
  TARGET="$(expand "$PATH_ARG")"
fi
assert_resolved "endpoint" "$TARGET"
case "$TARGET" in
  /*) ;;
  *) echo "the skill directory must be an absolute path: $TARGET" >&2; exit 2 ;;
esac

MANUAL="$(spec DOCS)"
case "$MANUAL" in
  '' | *://*) MANUAL="SKILL.md" ;;
esac
MANUAL_FILE="$TARGET/$MANUAL"

# skills.sh installs into <project>/.agents/skills/<name>. Derive the project
# root from the registered endpoint so `npx skills add` writes to the same place
# the catalogue points at; a --path outside that layout falls back to $PWD.
case "$TARGET" in
  */.agents/skills/*) PROJECT_ROOT="${TARGET%/.agents/skills/*}" ;;
  *) PROJECT_ROOT="$PWD" ;;
esac

RES_ID="$(spec ID)"
REPOSITORY="$(spec REPOSITORY)"
PACKAGE="$(spec PACKAGE)"
INSTALL_HINT="$(spec INSTALL_HINT)"
DISCOVER_HINT="$(spec DISCOVER_HINT)"
PATH_HINT="$(spec PATH_HINT)"
TRIGGERS="$(spec TRIGGERS)"
TOOLS="$(spec TOOLS)"

case "$SKILL" in
  anthropics-pdf) RECIPE="git" ;;
  skillssh-find-skills) RECIPE="skills-sh" ;;
esac

WORKDIR="${WORKDIR:-${XDG_DATA_HOME:-${HOME:-/tmp}/.local/share}/agenthub-skill-$SKILL}"
LINK="$WORKDIR/current"
STATE="$WORKDIR/path"
LOG_FILE="$WORKDIR/$SKILL.log"

# ---- helpers -----------------------------------------------------------------

show_plan() {
  echo "id:        $RES_ID"
  echo "endpoint:  $ENDPOINT"
  echo "directory: $TARGET"
  echo "manual:    $MANUAL_FILE"
  echo "recipe:    $RECIPE (repository: ${REPOSITORY:-<none>}, package: ${PACKAGE:-<none>})"
  echo "triggers:  ${TRIGGERS:-<none>}"
  echo "tools:     ${TOOLS:-<none>}"
  echo "workdir:   $WORKDIR"
  echo "install:   ${INSTALL_HINT:-<none>}"
  if [ -n "$DISCOVER_HINT" ]; then
    echo "discover:  $DISCOVER_HINT"
  fi
  if [ -n "$PATH_HINT" ]; then
    echo "path hint: $PATH_HINT"
  fi
}

# Acquisition uses argv arrays, never a shell string from the JSON. A checkout
# that already holds SKILL.md is left alone, so `start` is safe to re-run.
acquire_skill() {
  local parent found
  if [ -f "$MANUAL_FILE" ]; then
    echo "skill already present: $MANUAL_FILE"
    return 0
  fi
  case "$RECIPE" in
    git)
      require git
      require timeout
      parent="$(dirname "$TARGET")"
      if [ -e "$parent" ] && [ -n "$(ls -A "$parent" 2>/dev/null || true)" ]; then
        echo "$parent already exists and is not empty; refusing to clone into it" >&2
        echo "remove it, or point --path at a free directory" >&2
        return 1
      fi
      mkdir -p "$(dirname "$parent")"
      echo "cloning $REPOSITORY into $parent"
      if ! timeout "$WAIT" git clone --depth 1 "$REPOSITORY" "$parent" >>"$LOG_FILE" 2>&1; then
        echo "git clone failed; see $LOG_FILE" >&2
        return 1
      fi
      ;;
    skills-sh)
      require npx
      require timeout
      mkdir -p "$PROJECT_ROOT"
      echo "installing $PACKAGE into $PROJECT_ROOT (npx skills add)"
      if ! (cd "$PROJECT_ROOT" && timeout "$WAIT" npx -y skills add "$PACKAGE") >>"$LOG_FILE" 2>&1; then
        echo "npx skills add failed; see $LOG_FILE" >&2
        return 1
      fi
      ;;
  esac
  if [ -f "$MANUAL_FILE" ]; then
    return 0
  fi
  echo "the acquisition finished but $MANUAL_FILE is still missing" >&2
  if [ -n "$INSTALL_HINT" ]; then
    echo "catalogue install hint: $INSTALL_HINT" >&2
  fi
  if [ "$RECIPE" = "skills-sh" ]; then
    found="$(find "$PROJECT_ROOT/.agents/skills" -maxdepth 2 -name SKILL.md -print 2>/dev/null | head -n 5 || true)"
    if [ -n "$found" ]; then
      echo "SKILL.md files actually installed:" >&2
      printf '  %s\n' "$found" >&2
      echo "pass --path <that directory> to deploy one of them" >&2
    fi
  fi
  return 1
}

# ---- commands ----------------------------------------------------------------

start_skill() {
  if [ "$DRY_RUN" = "1" ]; then
    show_plan
    return 0
  fi
  if [ -L "$LINK" ] && [ -f "$STATE" ] && [ -f "$MANUAL_FILE" ]; then
    echo "$SKILL is already deployed at $TARGET — manual: $MANUAL_FILE"
    return 0
  fi
  mkdir -p "$WORKDIR"
  : >"$LOG_FILE"
  if ! acquire_skill; then
    tail -n 15 "$LOG_FILE" >&2 || true
    return 1
  fi
  ln -sfn "$TARGET" "$LINK"
  printf '%s\n' "$TARGET" >"$STATE"
  echo "$SKILL deployed at $TARGET — manual: $MANUAL_FILE"
  echo "verify it with: $SELF $SKILL smoke"
}

# `rm -f` on a symlink removes the link only, never the checkout behind it.
stop_skill() {
  local previous="-"
  if [ -f "$STATE" ]; then
    previous="$(cat "$STATE")"
  fi
  rm -f "$LINK" "$STATE"
  echo "$SKILL unpublished (no process to stop)"
  echo "the acquired files are kept at $previous"
}

status_skill() {
  local deployed="no" size="-"
  if [ -f "$STATE" ] && [ -L "$LINK" ]; then
    deployed="yes"
  fi
  echo "deployed      $deployed"
  echo "registered    $ENDPOINT"
  echo "directory     $TARGET"
  if [ -f "$MANUAL_FILE" ]; then
    size="$(wc -c <"$MANUAL_FILE" | tr -d '[:space:]')"
    echo "manual        $MANUAL_FILE ($size bytes)"
  else
    echo "manual        missing: $MANUAL_FILE"
  fi
  echo "link          $LINK -> $(readlink "$LINK" 2>/dev/null || echo '<none>')"
  echo "workdir       $WORKDIR"
  if [ -f "$LOG_FILE" ]; then
    echo "log           $LOG_FILE"
    tail -n 10 "$LOG_FILE"
  fi
  if [ -f "$STATE" ] && [ -L "$LINK" ] && [ -f "$MANUAL_FILE" ]; then
    return 0
  fi
  return 1
}

smoke_skill() {
  if [ "$DRY_RUN" = "1" ]; then
    echo "would read $MANUAL_FILE and print its front matter"
    return 0
  fi
  if [ ! -f "$MANUAL_FILE" ]; then
    echo "no manual at $MANUAL_FILE; deploy first: $SELF $SKILL start" >&2
    return 1
  fi
  python3 - "$MANUAL_FILE" "$SKILL" "$TRIGGERS" "$TOOLS" <<'PY'
import os, sys

path, name, triggers, tools = sys.argv[1:5]
size = os.path.getsize(path)
if size == 0:
    print("  ! %s is empty" % path)
    sys.exit(1)
with open(path, encoding="utf-8", errors="replace") as fh:
    text = fh.read()
print("  skill        %s" % name)
print("  manual       %s (%d bytes, %d lines)" % (path, size, len(text.splitlines())))
front = {}
if text.startswith("---"):
    head = text.split("---", 2)
    if len(head) > 2:
        for line in head[1].splitlines():
            if ":" in line:
                key, value = line.split(":", 1)
                front[key.strip()] = value.strip()
for key in ("name", "description"):
    if front.get(key):
        print("  %-13s %s" % (key, front[key][:160]))
print("  triggers     %s" % (triggers or "<none>"))
print("  tools        %s" % (tools or "<none>"))
print("  first lines:")
for line in text.splitlines()[:8]:
    print("    " + line[:160])
PY
}

case "$COMMAND" in
  start) start_skill ;;
  stop) stop_skill ;;
  restart) stop_skill; start_skill ;;
  status) status_skill ;;
  smoke) smoke_skill ;;
esac
