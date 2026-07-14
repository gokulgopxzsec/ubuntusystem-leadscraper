#!/usr/bin/env bash
#
# One-shot setup for leadscraper on Ubuntu.
#
#   ./scripts/setup.sh
#
# Installs Go and Docker if they are missing, writes a .env, starts Postgres
# and Redis, builds the binary, and runs the tests. Safe to re-run: every step
# checks before it acts.

set -euo pipefail

# The minimum Go the module needs (the `go` directive in go.mod). Anything at
# or above this is fine and is left alone; `apt install golang` ships older.
readonly GO_MIN_VERSION="1.26.4"
# What we install if Go is missing or too old.
readonly GO_VERSION="1.26.4"
readonly GO_ROOT="/usr/local/go"

# gosom/google-maps-scraper: scrapes Google Maps with a headless Chromium, no API key.
# The -rod tag, not :latest. Every Playwright-based tag pins a driver version
# that Microsoft's retired CDN no longer serves, so those images die on startup
# with "could not install driver ... 404". The -rod build needs no driver.
readonly GMAPS_IMAGE="gosom/google-maps-scraper:latest-rod"

readonly REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

readonly RED=$'\e[31m' GREEN=$'\e[32m' YELLOW=$'\e[33m' BLUE=$'\e[34m' DIM=$'\e[2m' RESET=$'\e[0m'

step() { printf '\n%s==>%s %s\n' "$BLUE" "$RESET" "$1"; }
ok()   { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$1"; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$1"; }
die()  { printf '\n%serror:%s %s\n' "$RED" "$RESET" "$1" >&2; exit 1; }

# ---------------------------------------------------------------- preflight

preflight() {
  step "Checking the machine"

  [[ "$(uname -s)" == "Linux" ]] || die "this script is for Linux; on Windows or macOS use Docker Desktop and 'make dev-deps'"
  [[ $EUID -ne 0 ]] || die "do not run this as root; it uses sudo only where it must"

  command -v sudo >/dev/null || die "sudo is required"
  command -v curl >/dev/null || sudo apt-get install -y curl

  if [[ -r /etc/os-release ]]; then
    . /etc/os-release
    ok "${PRETTY_NAME:-unknown Linux}"
  fi

  local cores mem_mb
  cores="$(nproc)"
  mem_mb="$(awk '/MemTotal/ {printf "%d", $2/1024}' /proc/meminfo)"
  ok "${cores} cores, ${mem_mb} MB RAM"

  # This is the Athlon 3050U case. The stack fits, but building the Go binary
  # inside Docker on two cores is genuinely painful, so say so up front.
  if (( cores <= 2 )); then
    warn "only ${cores} cores: use 'make dev-deps' + 'make run' rather than 'make docker-up'"
  fi
  if (( mem_mb < 4096 )); then
    warn "under 4 GB RAM: close other applications before running the full stack"
  fi

  if ! swapon --show --noheadings 2>/dev/null | grep -q .; then
    warn "no swap is active; a Go build on a small machine may be OOM-killed"
  fi
}

# detect reports what is already present before anything is installed, so it is
# obvious what this script is about to touch and what it will leave alone.
detect() {
  step "What is already installed"

  local go_v docker_v compose_v

  go_v="$(go_version)"
  if [[ -z "$go_v" ]]; then
    printf '  %s✗%s go           %snot installed, will install go%s%s\n' "$YELLOW" "$RESET" "$DIM" "$GO_VERSION" "$RESET"
  elif version_ge "$go_v" "$GO_MIN_VERSION"; then
    printf '  %s✓%s go           %s %s(>= %s, will keep)%s\n' "$GREEN" "$RESET" "$go_v" "$DIM" "$GO_MIN_VERSION" "$RESET"
  else
    printf '  %s✗%s go           %s %s(too old, need >= %s, will upgrade)%s\n' "$YELLOW" "$RESET" "$go_v" "$DIM" "$GO_MIN_VERSION" "$RESET"
  fi

  if docker_v="$(docker --version 2>/dev/null)"; then
    printf '  %s✓%s docker       %s %s(will keep)%s\n' "$GREEN" "$RESET" "${docker_v#Docker version }" "$DIM" "$RESET"
  else
    printf '  %s✗%s docker       %snot installed, will install%s\n' "$YELLOW" "$RESET" "$DIM" "$RESET"
  fi

  if compose_v="$(docker compose version --short 2>/dev/null)"; then
    printf '  %s✓%s compose      %s %s(will keep)%s\n' "$GREEN" "$RESET" "$compose_v" "$DIM" "$RESET"
  else
    printf '  %s✗%s compose      %snot installed, will install the plugin%s\n' "$YELLOW" "$RESET" "$DIM" "$RESET"
  fi

  if docker image inspect "$GMAPS_IMAGE" >/dev/null 2>&1; then
    printf '  %s✓%s maps-scraper %spulled (will keep)%s\n' "$GREEN" "$RESET" "$DIM" "$RESET"
  else
    printf '  %s✗%s maps-scraper %swill pull %s%s\n' "$YELLOW" "$RESET" "$DIM" "$GMAPS_IMAGE" "$RESET"
  fi

  if [[ -f "${REPO_DIR}/.env" ]]; then
    printf '  %s✓%s .env         %spresent (will not overwrite)%s\n' "$GREEN" "$RESET" "$DIM" "$RESET"
  else
    printf '  %s✗%s .env         %swill create from .env.example%s\n' "$YELLOW" "$RESET" "$DIM" "$RESET"
  fi

  printf '\n  %sNothing marked "will keep" is touched.%s\n' "$DIM" "$RESET"
}

# ---------------------------------------------------------------- go

install_go() {
  step "Go"

  local current
  current="$(go_version)"

  if [[ -n "$current" ]] && version_ge "$current" "$GO_MIN_VERSION"; then
    ok "go${current} already installed and new enough (need >= ${GO_MIN_VERSION}); leaving it alone"
    return
  fi

  if [[ -n "$current" ]]; then
    warn "go${current} is older than the required go${GO_MIN_VERSION}; upgrading to go${GO_VERSION}"
  else
    printf '  %snot installed%s\n' "$DIM" "$RESET"
  fi

  local arch tarball
  case "$(uname -m)" in
    x86_64)  arch="amd64" ;;
    aarch64) arch="arm64" ;;
    *)       die "unsupported architecture: $(uname -m)" ;;
  esac
  tarball="go${GO_VERSION}.linux-${arch}.tar.gz"

  printf '  %sdownloading %s...%s\n' "$DIM" "$tarball" "$RESET"
  curl -fsSL --retry 3 -o "/tmp/${tarball}" "https://go.dev/dl/${tarball}" \
    || die "could not download Go ${GO_VERSION}"

  # Removing the old tree first is what the Go docs require; untarring over an
  # existing install leaves stale files behind.
  sudo rm -rf "$GO_ROOT"
  sudo tar -C /usr/local -xzf "/tmp/${tarball}"
  rm -f "/tmp/${tarball}"

  add_go_to_path
  export PATH="${GO_ROOT}/bin:$PATH"
  hash -r

  version_ge "$(go_version)" "$GO_MIN_VERSION" \
    || die "Go was installed but 'go version' still reports $(go_version)"
  ok "installed $(go version | awk '{print $3}')"
}

# go_version prints the bare version ("1.26.4"), or nothing if Go is absent.
go_version() {
  command -v go >/dev/null || return 0
  go env GOVERSION 2>/dev/null | sed 's/^go//'
}

# version_ge is a numeric compare, so 1.26.4 correctly beats 1.9.0 (which a
# plain string comparison gets backwards).
version_ge() {
  [[ -n "$1" ]] || return 1
  [[ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" == "$2" ]]
}

add_go_to_path() {
  local profile="$HOME/.profile"
  local line="export PATH=\$PATH:${GO_ROOT}/bin"

  if [[ -f "$profile" ]] && grep -qF "${GO_ROOT}/bin" "$profile"; then
    return
  fi

  printf '\n# Go\n%s\n' "$line" >> "$profile"
  ok "added ${GO_ROOT}/bin to PATH in ${profile}"
  warn "run 'source ~/.profile' (or open a new terminal) to pick it up"
}

# ---------------------------------------------------------------- docker

install_docker() {
  step "Docker"

  if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
    ok "$(docker --version | cut -d, -f1)"
  else
    printf '  %sinstalling from the official Docker repository...%s\n' "$DIM" "$RESET"

    # Ubuntu's own docker.io package lags and does not ship the compose v2
    # plugin, which this project's Makefile depends on.
    sudo apt-get update -qq
    sudo apt-get install -y ca-certificates curl gnupg

    sudo install -m 0755 -d /etc/apt/keyrings
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
      | sudo gpg --batch --yes --dearmor -o /etc/apt/keyrings/docker.gpg
    sudo chmod a+r /etc/apt/keyrings/docker.gpg

    # Ubuntu 26.04 (resolute) may not have a Docker repo yet. Fall back to the
    # most recent LTS codename, which is compatible.
    local codename
    codename="$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")"
    if ! curl -fsI "https://download.docker.com/linux/ubuntu/dists/${codename}/Release" >/dev/null 2>&1; then
      warn "Docker has no repo for '${codename}' yet; using 'noble' (24.04), which is compatible"
      codename="noble"
    fi

    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu ${codename} stable" \
      | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null

    sudo apt-get update -qq
    sudo apt-get install -y \
      docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

    ok "installed $(docker --version | cut -d, -f1)"
  fi

  sudo systemctl enable --now docker >/dev/null 2>&1 || true

  # Without this every docker command needs sudo.
  if ! id -nG "$USER" | tr ' ' '\n' | grep -qx docker; then
    sudo usermod -aG docker "$USER"
    warn "added ${USER} to the 'docker' group"
    warn "this only takes effect on a new login: run 'newgrp docker' or log out and back in"
  fi

  docker info >/dev/null 2>&1 \
    || warn "cannot talk to the Docker daemon yet (expected until you re-login)"
}

# ---------------------------------------------------------------- project

# google-maps-scraper is gosom/google-maps-scraper. It drives a headless
# Chromium, so we pull the published image rather than building it and letting
# Playwright download a browser toolchain onto this machine.
install_gmaps() {
  step "google-maps-scraper (gosom)"

  if ! docker info >/dev/null 2>&1; then
    warn "skipping: the Docker daemon is not reachable from this shell yet"
    warn "after re-login, run: docker pull ${GMAPS_IMAGE}"
    return
  fi

  if docker image inspect "$GMAPS_IMAGE" >/dev/null 2>&1; then
    ok "${GMAPS_IMAGE} already pulled"
    return
  fi

  printf '  %spulling %s (this includes Chromium, so it is a large image)...%s\n' \
    "$DIM" "$GMAPS_IMAGE" "$RESET"

  if docker pull "$GMAPS_IMAGE" >/dev/null 2>&1; then
    ok "pulled ${GMAPS_IMAGE}"
  else
    warn "could not pull ${GMAPS_IMAGE}; the google_maps source will be disabled"
    warn "retry later with: docker pull ${GMAPS_IMAGE}"
  fi

  # The headless browser is the heaviest thing this project runs.
  local mem_mb
  mem_mb="$(awk '/MemTotal/ {printf "%d", $2/1024}' /proc/meminfo)"
  if (( mem_mb < 8192 )); then
    warn "with ${mem_mb} MB RAM, keep GMAPS_CONCURRENCY=1 and close your browser while scraping"
  fi
}

setup_env() {
  step "Configuration"

  cd "$REPO_DIR"

  if [[ -f .env ]]; then
    ok ".env already exists, leaving it alone"
  else
    cp .env.example .env
    ok "wrote .env from .env.example"
  fi

  mkdir -p data
  ok "data/ ready for CSV imports"

  printf '\n  %sOptional, edit .env to enable:%s\n' "$DIM" "$RESET"
  printf '    %sGOOGLE_PLACES_API_KEY%s  the google_maps source (CSV works without it)\n' "$DIM" "$RESET"
  printf '    %sAI_PROVIDER=gemini%s     the written site audit (rule scoring works without it)\n' "$DIM" "$RESET"
}

start_deps() {
  step "Postgres and Redis"

  cd "$REPO_DIR"

  if ! docker info >/dev/null 2>&1; then
    warn "skipping: the Docker daemon is not reachable from this shell yet"
    warn "run 'newgrp docker' (or re-login), then: docker compose up -d postgres redis"
    return
  fi

  docker compose up -d postgres redis

  printf '  %swaiting for health checks...%s\n' "$DIM" "$RESET"
  local i
  for i in $(seq 1 30); do
    if docker compose ps --format '{{.Service}} {{.Status}}' 2>/dev/null \
        | grep -c healthy | grep -qx 2; then
      ok "postgres and redis are healthy"
      return
    fi
    sleep 2
  done

  warn "they did not report healthy within 60s; check 'docker compose logs'"
}

build_and_test() {
  step "Build and test"

  cd "$REPO_DIR"
  export PATH="$PATH:${GO_ROOT}/bin"

  go build -o build/server ./cmd/server
  ok "built build/server"

  if go test ./... >/tmp/leadscraper-test.log 2>&1; then
    ok "all tests pass"
  else
    warn "some tests failed; see /tmp/leadscraper-test.log"
  fi
}

# ---------------------------------------------------------------- done

summary() {
  printf '\n%s────────────────────────────────────────────────────────%s\n' "$GREEN" "$RESET"
  printf '%s  Setup complete%s\n' "$GREEN" "$RESET"
  printf '%s────────────────────────────────────────────────────────%s\n\n' "$GREEN" "$RESET"

  if ! docker info >/dev/null 2>&1; then
    printf '  %sFirst, pick up your new docker group membership:%s\n\n' "$YELLOW" "$RESET"
    printf '    newgrp docker\n'
    printf '    docker compose up -d postgres redis\n\n'
  fi

  printf '  Run it (three terminals):\n\n'
  printf '    make run            %s# API on :8080%s\n' "$DIM" "$RESET"
  printf '    make run-worker     %s# the pipeline%s\n' "$DIM" "$RESET"
  printf '    make smoke          %s# import the sample CSV, print scored leads%s\n\n' "$DIM" "$RESET"
  printf '  Then:\n\n'
  printf '    curl localhost:8080/api/v1/leads\n\n'
}

main() {
  printf '%sleadscraper setup%s\n' "$BLUE" "$RESET"

  preflight
  detect
  install_go
  install_docker
  install_gmaps
  setup_env
  start_deps
  build_and_test
  summary
}

main "$@"
