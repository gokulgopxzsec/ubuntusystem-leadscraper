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

# Go's own version, not Ubuntu's. `apt install golang` ships a release that is
# older than the `go` directive in go.mod, and the build would fail.
readonly GO_VERSION="1.26.4"
readonly GO_ROOT="/usr/local/go"

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

# ---------------------------------------------------------------- go

install_go() {
  step "Go ${GO_VERSION}"

  if have_go_version; then
    ok "go ${GO_VERSION} already installed"
    return
  fi

  if command -v go >/dev/null; then
    warn "found $(go version | awk '{print $3}'), replacing it with go${GO_VERSION}"
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
  export PATH="$PATH:${GO_ROOT}/bin"

  have_go_version || die "Go installed but 'go version' does not report ${GO_VERSION}"
  ok "installed $(go version)"
}

have_go_version() {
  command -v go >/dev/null && [[ "$(go env GOVERSION 2>/dev/null)" == "go${GO_VERSION}" ]]
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
  install_go
  install_docker
  setup_env
  start_deps
  build_and_test
  summary
}

main "$@"
