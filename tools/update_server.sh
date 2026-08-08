#!/usr/bin/env bash
#
# Updates a server-side ck3-index deployment: pull, build, rebuild the index
# only when the index rules actually changed, then swap. Safe to run from cron.
#
# Why it is shaped this way
# ------------------------
# The expensive step is the index rebuild, and it is only required when
# indexRuleVersion changes — roughly six times in the last three weeks, far
# less often than commits land. So the script reads that constant before and
# after the pull and skips the rebuild entirely when it did not move.
#
# When a rebuild IS needed, it happens into a separate database file while the
# running server keeps serving the old one. The publication lock is per
# database path, so the two never contend. Only after the new index verifies
# ready does the script stop the agent, swap binary and database, and start it
# again — seconds of downtime instead of the length of a full scan.
#
# Nothing is destroyed until the replacement is proven: the previous binary and
# database are kept as .bak until the next successful run.
#
# Usage
#   tools/update_server.sh --config /path/to/ck3-index.toml [options]
#
#   --config PATH     ck3-index.toml to update against (required)
#   --check           report whether an update is available, change nothing
#   --force-rebuild   rebuild the index even if the rule version is unchanged
#   --keep-going      do not stop when the agent hooks are missing
#
# Agent hooks — the script cannot guess how your agent runs, so it asks:
#   CK3_STOP_CMD    shell command that stops the agent (and its MCP child)
#   CK3_START_CMD   shell command that starts it again
# Without them the script pauses at the swap and prompts you to do it by hand.

set -euo pipefail

CONFIG=""
CHECK_ONLY=0
FORCE_REBUILD=0
KEEP_GOING=0

while [ $# -gt 0 ]; do
    case "$1" in
        --config) CONFIG="${2:-}"; shift 2 ;;
        --check) CHECK_ONLY=1; shift ;;
        --force-rebuild) FORCE_REBUILD=1; shift ;;
        --keep-going) KEEP_GOING=1; shift ;;
        -h|--help) sed -n '2,40p' "$0"; exit 0 ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
done

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

log()  { printf '[ck3-update] %s\n' "$*"; }
fail() { printf '[ck3-update] ERROR: %s\n' "$*" >&2; exit 1; }

[ -n "$CONFIG" ] || fail "--config is required"
[ -f "$CONFIG" ] || fail "config not found: $CONFIG"
CONFIG="$(cd "$(dirname "$CONFIG")" && pwd)/$(basename "$CONFIG")"

command -v go  >/dev/null 2>&1 || fail "go is not on PATH"
command -v git >/dev/null 2>&1 || fail "git is not on PATH"

# ---------------------------------------------------------------- config read
# Paths in ck3-index.toml may be relative to the config file, matching the Go
# loader. Only the two keys this script needs are parsed; everything else is
# left to ck3-index itself.
read_toml_key() {
    sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$CONFIG" | head -n 1
}
resolve_path() {
    case "$1" in
        /*) printf '%s\n' "$1" ;;
        *)  printf '%s\n' "$(cd "$(dirname "$CONFIG")" && pwd)/$1" ;;
    esac
}

DB_RAW="$(read_toml_key database)"
[ -n "$DB_RAW" ] || fail "no database key in $CONFIG"
DB="$(resolve_path "$DB_RAW")"
DB_DIR="$(dirname "$DB")"
DB_NEXT="$DB.next"

RULE_FILE="internal/indexer/scan.go"
read_rule_version() {
    sed -n 's/^const indexRuleVersion = "\(.*\)"/\1/p' "$RULE_FILE" | head -n 1
}

BIN="$REPO/bin/ck3-index"
BIN_NEW="$BIN.new"

# ------------------------------------------------------------- update check
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || fail "$REPO is not a git repository"
if [ -n "$(git status --porcelain)" ]; then
    fail "working tree has local changes; commit or stash them before updating"
fi

BRANCH="$(git rev-parse --abbrev-ref HEAD)"
log "fetching origin/$BRANCH"
git fetch --quiet origin "$BRANCH"

LOCAL="$(git rev-parse HEAD)"
REMOTE="$(git rev-parse "origin/$BRANCH")"

if [ "$LOCAL" = "$REMOTE" ] && [ "$FORCE_REBUILD" -eq 0 ]; then
    log "already at ${LOCAL:0:7}, nothing to do"
    exit 0
fi

OLD_RULE="$(read_rule_version)"
[ -n "$OLD_RULE" ] || fail "could not read indexRuleVersion from $RULE_FILE"

if [ "$CHECK_ONLY" -eq 1 ]; then
    log "update available: ${LOCAL:0:7} -> ${REMOTE:0:7}"
    log "$(git log --oneline "$LOCAL..$REMOTE" | wc -l) commit(s) pending"
    git log --oneline "$LOCAL..$REMOTE" | sed 's/^/[ck3-update]   /'
    NEW_RULE_PREVIEW="$(git show "$REMOTE:$RULE_FILE" | sed -n 's/^const indexRuleVersion = "\(.*\)"/\1/p' | head -n 1)"
    if [ "$OLD_RULE" != "$NEW_RULE_PREVIEW" ]; then
        log "index rules change: $OLD_RULE -> $NEW_RULE_PREVIEW (full rebuild required)"
    else
        log "index rules unchanged (binary swap only, seconds of downtime)"
    fi
    exit 0
fi

# --------------------------------------------------------------------- pull
log "updating ${LOCAL:0:7} -> ${REMOTE:0:7}"
git merge --ff-only "origin/$BRANCH" >/dev/null || fail "cannot fast-forward; resolve $BRANCH by hand"

NEW_RULE="$(read_rule_version)"
[ -n "$NEW_RULE" ] || fail "could not read indexRuleVersion after pull"

REBUILD=0
if [ "$OLD_RULE" != "$NEW_RULE" ]; then
    log "index rules changed: $OLD_RULE -> $NEW_RULE"
    REBUILD=1
elif [ "$FORCE_REBUILD" -eq 1 ]; then
    log "index rules unchanged, but --force-rebuild was given"
    REBUILD=1
else
    log "index rules unchanged; skipping rebuild"
fi

rollback_git() {
    log "rolling source back to ${LOCAL:0:7}"
    git reset --hard "$LOCAL" >/dev/null 2>&1 || true
}

# -------------------------------------------------------------------- build
VERSION="$(tr -d '[:space:]' < VERSION)"
REVISION="$(git rev-parse --short HEAD)"
log "building $VERSION ($REVISION)"
mkdir -p "$(dirname "$BIN_NEW")"
if ! go build -trimpath -buildvcs=false \
        -ldflags "-s -w -X ck3-index/internal/buildinfo.Version=$VERSION -X ck3-index/internal/buildinfo.Revision=$REVISION" \
        -o "$BIN_NEW" . ; then
    rollback_git
    fail "build failed; source rolled back, nothing was swapped"
fi

# A binary that cannot even print its own usage must never reach the swap.
if ! "$BIN_NEW" >/dev/null 2>&1 && [ ! -x "$BIN_NEW" ]; then
    rm -f "$BIN_NEW"
    rollback_git
    fail "new binary is not executable"
fi
log "built $(du -h "$BIN_NEW" | cut -f1)"

# ------------------------------------------------------------------ rebuild
if [ "$REBUILD" -eq 1 ]; then
    # The rebuild needs room for a second copy of the index alongside the live
    # one. Running the disk out mid-scan would leave the staging cache behind
    # and still not produce a usable index.
    if [ -f "$DB" ]; then
        NEED_KB=$(( $(du -k "$DB" | cut -f1) * 2 ))
        FREE_KB=$(df -Pk "$DB_DIR" | awk 'NR==2 {print $4}')
        if [ "$FREE_KB" -lt "$NEED_KB" ]; then
            rm -f "$BIN_NEW"; rollback_git
            fail "need ~$((NEED_KB/1024))MB free in $DB_DIR for the rebuild, have $((FREE_KB/1024))MB"
        fi
    fi

    # Build into a sibling database so the running server keeps reading the
    # live one. The publication lock is keyed on the database path, so the scan
    # and the live server never contend.
    # The temporary config must live beside the real one. Relative source and
    # base_database paths resolve against the config file's own directory, so a
    # copy in /tmp would silently repoint every source at /tmp.
    TMP_CONFIG="$(mktemp "$(dirname "$CONFIG")/.ck3-index-update.XXXXXX.toml")"
    trap 'rm -f "$TMP_CONFIG"' EXIT
    # database is rewritten to an absolute path; base_database is left alone so
    # the rebuild still gets its upstream seed.
    sed "s|^\([[:space:]]*database[[:space:]]*=[[:space:]]*\).*|\1\"$DB_NEXT\"|" "$CONFIG" > "$TMP_CONFIG"
    if ! grep -q "^[[:space:]]*database[[:space:]]*=[[:space:]]*\"$DB_NEXT\"" "$TMP_CONFIG"; then
        rm -f "$BIN_NEW"; rollback_git
        fail "could not repoint database in a temporary config; aborting before any change"
    fi

    rm -f "$DB_NEXT" "$DB_NEXT-wal" "$DB_NEXT-shm"
    log "rebuilding index into $(basename "$DB_NEXT") (live server unaffected)"
    if ! "$BIN_NEW" --config "$TMP_CONFIG" scan --clean; then
        rm -f "$BIN_NEW" "$DB_NEXT" "$DB_NEXT-wal" "$DB_NEXT-shm"
        rollback_git
        fail "rebuild failed; live index and binary untouched"
    fi

    log "verifying new index"
    if ! "$BIN_NEW" --config "$TMP_CONFIG" health >/dev/null; then
        rm -f "$BIN_NEW" "$DB_NEXT" "$DB_NEXT-wal" "$DB_NEXT-shm"
        rollback_git
        fail "new index failed its health check; nothing was swapped"
    fi
    log "new index verified"
fi

# --------------------------------------------------------------------- swap
STOP_CMD="${CK3_STOP_CMD:-}"
START_CMD="${CK3_START_CMD:-}"

if [ -z "$STOP_CMD" ] || [ -z "$START_CMD" ]; then
    if [ "$KEEP_GOING" -eq 0 ]; then
        log ""
        log "CK3_STOP_CMD / CK3_START_CMD are not set, so the agent cannot be"
        log "cycled automatically. Everything is built and verified; finish with:"
        log ""
        log "  <stop your agent>"
        log "  mv '$BIN_NEW' '$BIN'"
        if [ "$REBUILD" -eq 1 ]; then log "  mv '$DB_NEXT' '$DB'"; fi
        log "  <start your agent>"
        log ""
        log "Or set both variables and re-run to have this done for you."
        exit 3
    fi
    log "no agent hooks set and --keep-going given; swapping without cycling"
else
    log "stopping agent"
    eval "$STOP_CMD" || fail "stop command failed; nothing was swapped"
fi

# From here the window is intentionally tiny: two renames, no scanning.
# Note the deliberate `if` blocks rather than `[ x ] && y`: under set -e a false
# test would end the script here, after the agent has already been stopped.
if [ -f "$BIN" ]; then
    cp -p "$BIN" "$BIN.bak"
fi
mv "$BIN_NEW" "$BIN"

if [ "$REBUILD" -eq 1 ]; then
    if [ -f "$DB" ]; then
        mv "$DB" "$DB.bak"
        # Sidecars belong to the old database; leaving them next to the new one
        # would make SQLite read a WAL that does not match it.
        rm -f "$DB-wal" "$DB-shm"
    fi
    mv "$DB_NEXT" "$DB"
    if [ -f "$DB_NEXT-wal" ]; then mv "$DB_NEXT-wal" "$DB-wal"; fi
    if [ -f "$DB_NEXT-shm" ]; then mv "$DB_NEXT-shm" "$DB-shm"; fi
fi

if [ -n "$START_CMD" ]; then
    log "starting agent"
    eval "$START_CMD" || fail "start command failed; binary is $REVISION, start the agent by hand"
fi

log "updated to $VERSION ($REVISION)"
log "previous binary kept at $(basename "$BIN").bak"
if [ "$REBUILD" -eq 1 ]; then
    log "index rebuilt for rules $NEW_RULE"
    log "previous index kept at $(basename "$DB").bak - delete it once the new one looks right"
fi
exit 0
