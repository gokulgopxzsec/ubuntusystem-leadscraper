#!/usr/bin/env bash
#
# The whole app, one command.
#
#   ./run.sh
#
# Starts Postgres and Redis, builds, runs the API + worker + dashboard in a
# single process, and opens the browser. Ctrl-C stops everything.
#
# Run ./scripts/setup.sh first if Go or Docker are not installed yet.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")"
export PATH="$PATH:/usr/local/go/bin"

PORT="${PORT:-8080}"
API="http://localhost:${PORT}"

G=$'\e[32m'; Y=$'\e[33m'; B=$'\e[34m'; D=$'\e[2m'; R=$'\e[0m'
step() { printf '\n%s==>%s %s\n' "$B" "$R" "$1"; }
ok()   { printf '  %s✓%s %s\n' "$G" "$R" "$1"; }
die()  { printf '\n%serror:%s %s\n' $'\e[31m' "$R" "$1" >&2; exit 1; }

cleanup() {
  printf '\n%s==>%s Stopping\n' "$B" "$R"
  # The server drains in-flight jobs on SIGTERM, so give it a moment rather
  # than killing it outright.
  [[ -n "${SRV_PID:-}" ]] && kill -TERM "$SRV_PID" 2>/dev/null || true
  wait 2>/dev/null || true
  ok "stopped. Postgres and Redis are still up (docker compose down to stop them)."
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------- checks

command -v go >/dev/null || die "go is not on PATH. Run ./scripts/setup.sh, then: source ~/.profile"
docker info >/dev/null 2>&1 || die "cannot reach the Docker daemon. Try 'newgrp docker', or re-login."

if ss -lnt 2>/dev/null | grep -q ":${PORT} " || lsof -i ":${PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
  die "port ${PORT} is already in use. Try: PORT=8099 ./run.sh"
fi

[[ -f .env ]] || { cp .env.example .env; ok "created .env"; }

# ---------------------------------------------------------------- deps

step "Postgres and Redis"
docker compose up -d postgres redis >/dev/null
for _ in $(seq 1 30); do
  [[ "$(docker compose ps --format '{{.Status}}' postgres redis 2>/dev/null | grep -c healthy)" == "2" ]] && break
  sleep 1
done
ok "up"

# The Maps scraper runs as a sibling container. Pulling it now means the first
# scrape does not silently stall on a large download.
if ! docker image inspect gosom/google-maps-scraper:latest-rod >/dev/null 2>&1; then
  step "Pulling the Google Maps scraper (one time, it bundles Chromium)"
  docker pull gosom/google-maps-scraper:latest-rod >/dev/null && ok "pulled"
fi

# ---------------------------------------------------------------- build

step "Building"
go build -o build/server ./cmd/server
ok "built"

# ---------------------------------------------------------------- run

step "Starting leadscraper"

# No flags: one process runs the API, the worker, and the dashboard. On a
# two-core machine that is meaningfully cheaper than two processes.
PORT="$PORT" ./build/server &
SRV_PID=$!

for _ in $(seq 1 30); do
  curl -sf "${API}/api/v1/ready" >/dev/null 2>&1 && break
  kill -0 "$SRV_PID" 2>/dev/null || die "the server exited during startup"
  sleep 1
done
curl -sf "${API}/api/v1/ready" >/dev/null 2>&1 || die "the server never became ready"

printf '\n%s────────────────────────────────────────────%s\n' "$G" "$R"
printf '%s  leadscraper is running%s\n' "$G" "$R"
printf '%s────────────────────────────────────────────%s\n\n' "$G" "$R"
printf '  Dashboard:  %s%s%s\n' "$B" "$API" "$R"
printf '  %sClick "Find leads" and try: bakery in Kochi%s\n\n' "$D" "$R"
printf '  %sCtrl-C to stop.%s\n\n' "$D" "$R"

# Open a browser if there is a desktop session to open it in.
if command -v xdg-open >/dev/null && [[ -n "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]]; then
  (xdg-open "$API" >/dev/null 2>&1 &)
fi

wait "$SRV_PID"
