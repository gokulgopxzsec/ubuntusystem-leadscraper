#!/usr/bin/env bash
#
# Starts the API and the worker, imports the sample CSV, and prints the scored
# leads. Stops everything again on exit.
#
#   ./scripts/demo.sh
#
# Run ./scripts/setup.sh first if you have not already.

set -euo pipefail

readonly REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly PORT="${PORT:-8080}"
readonly API="http://localhost:${PORT}"
readonly LOG_DIR="/tmp/leadscraper"

readonly GREEN=$'\e[32m' YELLOW=$'\e[33m' BLUE=$'\e[34m' DIM=$'\e[2m' RESET=$'\e[0m'

step() { printf '\n%s==>%s %s\n' "$BLUE" "$RESET" "$1"; }
ok()   { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$1"; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$1"; }
die()  { printf '\nerror: %s\n' "$1" >&2; exit 1; }

API_PID=""
WORKER_PID=""

cleanup() {
  printf '\n%s==>%s Stopping\n' "$BLUE" "$RESET"
  # SIGTERM, not SIGKILL: the whole point is that both processes now shut down
  # gracefully and the worker drains its in-flight jobs first.
  [[ -n "$WORKER_PID" ]] && kill -TERM "$WORKER_PID" 2>/dev/null || true
  [[ -n "$API_PID" ]]    && kill -TERM "$API_PID"    2>/dev/null || true
  wait 2>/dev/null || true
  ok "stopped (Postgres and Redis are still up; 'docker compose down' to stop those too)"
}
trap cleanup EXIT

cd "$REPO_DIR"
export PATH="$PATH:/usr/local/go/bin"
mkdir -p "$LOG_DIR"

# ------------------------------------------------------------------ checks

step "Checking dependencies"

command -v go >/dev/null || die "go is not on PATH; run ./scripts/setup.sh (then: source ~/.profile)"
docker info >/dev/null 2>&1 || die "cannot reach the Docker daemon; try 'newgrp docker' or re-login"

if ! docker compose ps --format '{{.Service}} {{.Status}}' | grep -q 'postgres.*healthy'; then
  warn "Postgres is not up; starting it"
  docker compose up -d postgres redis
  printf '  %swaiting for health checks...%s\n' "$DIM" "$RESET"
  sleep 8
fi
ok "postgres and redis are up"

if lsof -i ":${PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
  die "port ${PORT} is already in use; try: PORT=8099 ./scripts/demo.sh"
fi

# ------------------------------------------------------------------ build

step "Building"
go build -o build/server ./cmd/server
ok "built build/server"

# ------------------------------------------------------------------ start

step "Starting the API on :${PORT}"

PORT="$PORT" ./build/server > "${LOG_DIR}/api.log" 2>&1 &
API_PID=$!

# Poll readiness rather than sleeping a guessed interval: /ready actually pings
# Postgres and Redis, so a 200 means the API can really serve.
for _ in $(seq 1 30); do
  if curl -sf "${API}/api/v1/ready" >/dev/null 2>&1; then break; fi
  kill -0 "$API_PID" 2>/dev/null || { cat "${LOG_DIR}/api.log"; die "the API exited during startup"; }
  sleep 1
done

curl -sf "${API}/api/v1/ready" >/dev/null 2>&1 || { cat "${LOG_DIR}/api.log"; die "the API never became ready"; }
ok "API ready ${DIM}(logs: ${LOG_DIR}/api.log)${RESET}"

step "Starting the worker"

AUTO_MIGRATE=false ./build/server --worker > "${LOG_DIR}/worker.log" 2>&1 &
WORKER_PID=$!

for _ in $(seq 1 20); do
  grep -q "worker started" "${LOG_DIR}/worker.log" 2>/dev/null && break
  kill -0 "$WORKER_PID" 2>/dev/null || { cat "${LOG_DIR}/worker.log"; die "the worker exited during startup"; }
  sleep 1
done
ok "worker ready ${DIM}(logs: ${LOG_DIR}/worker.log)${RESET}"

# ------------------------------------------------------------------ run

CATEGORY="${CATEGORY:-bakery}"
LOCATION="${LOCATION:-Kochi}"
LIMIT="${LIMIT:-10}"

step "Scraping Google Maps: ${CATEGORY} in ${LOCATION} (limit ${LIMIT})"

if ! curl -sf "${API}/api/v1/scrape/sources" | grep -q google_maps; then
  die "the google_maps source is not registered; check the API log: ${LOG_DIR}/api.log"
fi

curl -sf -X POST "${API}/api/v1/scrape" \
  -H 'Content-Type: application/json' \
  -d "{\"source\":\"google_maps\",\"category\":\"${CATEGORY}\",\"location\":\"${LOCATION}\",\"limit\":${LIMIT}}" >/dev/null \
  || die "could not queue the scrape job"
ok "job queued"

step "Waiting for the pipeline"
printf '  %scollect_business -> website_crawl -> rule_scoring -> gen_recommendation%s\n' "$DIM" "$RESET"
printf '  %sThe Maps scrape drives a headless browser, so the first stage is slow.%s\n\n' "$DIM" "$RESET"

# A browser-driven Maps scrape takes minutes, and each business is then crawled.
# Wait for the pipeline to go quiet rather than for a fixed number of jobs.
idle=0
for _ in $(seq 1 180); do
  depth="$(curl -sf "${API}/api/v1/ready" | sed -E 's/.*"queue_depth":([0-9]+).*/\1/')"
  scored="$(grep -c 'lead scored' "${LOG_DIR}/worker.log" 2>/dev/null || echo 0)"

  if [[ "${depth:-1}" == "0" && "$scored" -gt 0 ]]; then
    idle=$((idle + 1))
    # Two consecutive empty polls means the worker really is done, not just
    # between jobs.
    [[ $idle -ge 2 ]] && break
  else
    idle=0
  fi

  printf '.'
  sleep 5
done
printf '\n'

grep -E 'lead scored|collection finished|unreachable' "${LOG_DIR}/worker.log" \
  | sed -E 's/time=[^ ]+ level=[A-Z]+ source=[^ ]+ /  /' || true

# ------------------------------------------------------------------ results

step "Scored leads"
printf '\n'

curl -sf "${API}/api/v1/leads" | python3 -c '
import sys, json
leads = json.load(sys.stdin)["data"]
if not leads:
    print("  (none yet, the worker may still be running)")
for i, l in enumerate(leads, 1):
    b = l["business"]
    print("  %d. %s  [%s, score %d]" % (i, b["name"], l["priority"].upper(), l["total_score"]))
    print("     website: %s" % (b.get("website") or "(none)"))
    gaps = {k: v for k, v in l["breakdown"].items() if v}
    print("     gaps   : %s" % ", ".join("%s +%d" % (k, v) for k, v in sorted(gaps.items(), key=lambda x: -x[1])))
    print("     pitch  : %s" % l["sales_suggestion"])
    print()
' || curl -sf "${API}/api/v1/leads"

printf '\n%sThe API is still running.%s Try:\n\n' "$GREEN" "$RESET"
printf '    curl %s/api/v1/leads\n' "$API"
printf '    curl %s/api/v1/businesses\n\n' "$API"
printf '  %sPress Ctrl-C to stop.%s\n' "$DIM" "$RESET"

# Hold the processes open so the reader can poke at the API.
wait
