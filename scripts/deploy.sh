#!/usr/bin/env bash
#
# Pull, verify, restart — and roll back if the new build does not come up.
#
#   ./scripts/deploy.sh          # deploy if there is anything new
#   ./scripts/deploy.sh --force  # rebuild and restart even with no new commits
#
# THIS IS MANUAL. Nothing runs it on a timer, and nothing should: a bad migration
# cannot be rolled back by restoring a binary, so no script can honestly promise
# to undo one. A deploy that touches migrations/ needs a human who has taken a
# backup and is watching.
#
# What it does guarantee: a build that fails, or tests that fail, never reach the
# running service; and a new binary that does not come up healthy is replaced by
# the one that was working.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
export PATH="$PATH:/usr/local/go/bin"

readonly SERVICE="leadscraper"
readonly BINARY="build/server"
readonly PREVIOUS="build/server.previous"
readonly HEALTH="http://localhost:${PORT:-8080}/api/v1/ready"
readonly LOCK="/tmp/leadscraper-deploy.lock"

log() { printf '%s  %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"; }
die() { log "FAILED: $*"; exit 1; }

# The timer and a human can fire this at the same time. Two deploys racing would
# leave the binary half-written, so only one may run.
exec 9>"$LOCK"
flock -n 9 || { log "another deploy is already running; skipping"; exit 0; }

FORCE=0
[[ "${1:-}" == "--force" ]] && FORCE=1

# ---------------------------------------------------------------- is there anything to do?

git fetch --quiet origin

readonly LOCAL="$(git rev-parse @)"
readonly REMOTE="$(git rev-parse '@{u}')"

if [[ "$LOCAL" == "$REMOTE" && $FORCE -eq 0 ]]; then
  exit 0    # nothing new; stay quiet so the timer does not spam the journal
fi

log "=== deploying ${LOCAL:0:8} -> ${REMOTE:0:8} ==="

# Refuse to clobber uncommitted work on the box. Somebody edited something here,
# and silently throwing it away would be worse than not deploying.
if ! git diff --quiet || ! git diff --cached --quiet; then
  die "the working tree is dirty; commit or stash on the server first"
fi

git merge --ff-only origin/"$(git rev-parse --abbrev-ref HEAD)" \
  || die "cannot fast-forward; the local branch has diverged from origin"

log "pulled $(git log -1 --pretty='%h %s')"

# ---------------------------------------------------------------- gate: it must build and pass

# Build to a temporary path. Writing straight over the running binary would leave
# nothing to roll back to if the compile failed halfway.
log "building..."
if ! go build -o "${BINARY}.new" ./cmd/server 2>&1 | sed 's/^/    /'; then
  die "build failed — the running version is untouched"
fi

log "testing..."
if ! go test ./... -count=1 > /tmp/leadscraper-deploy-test.log 2>&1; then
  rm -f "${BINARY}.new"
  tail -20 /tmp/leadscraper-deploy-test.log | sed 's/^/    /'
  die "tests failed — not deploying (full log: /tmp/leadscraper-deploy-test.log)"
fi
log "tests pass"

# ---------------------------------------------------------------- swap and restart

# Keep the binary that is currently working. This is what rollback restores.
if [[ -f "$BINARY" ]]; then
  cp -f "$BINARY" "$PREVIOUS"
fi

mv -f "${BINARY}.new" "$BINARY"

log "restarting ${SERVICE}..."
sudo systemctl restart "$SERVICE"

# ---------------------------------------------------------------- gate: it must come up healthy

# /ready actually pings Postgres and Redis, so a 200 here means the app can
# really serve — not merely that the process started.
healthy() {
  local i
  for i in $(seq 1 30); do
    if curl -sf --max-time 3 "$HEALTH" >/dev/null 2>&1; then
      return 0
    fi
    # A process that has already exited will not become healthy by waiting.
    systemctl is-active --quiet "$SERVICE" || return 1
    sleep 2
  done
  return 1
}

if healthy; then
  log "healthy — deployed $(git log -1 --pretty='%h %s')"
  exit 0
fi

# ---------------------------------------------------------------- roll back

log "the new build did not become healthy; rolling back"

if [[ ! -f "$PREVIOUS" ]]; then
  journalctl -u "$SERVICE" -n 25 --no-pager | sed 's/^/    /'
  die "no previous binary to roll back to; the service is DOWN"
fi

mv -f "$PREVIOUS" "$BINARY"
sudo systemctl restart "$SERVICE"

if healthy; then
  log "ROLLED BACK to the previous binary; the service is up again"
  log "the bad commit is still checked out — inspect it, then fix or 'git reset --hard HEAD~1'"
  journalctl -u "$SERVICE" -n 25 --no-pager | sed 's/^/    /'
  exit 1
fi

journalctl -u "$SERVICE" -n 40 --no-pager | sed 's/^/    /'
die "rollback did not come up either; the service is DOWN and needs a human"
