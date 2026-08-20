#!/usr/bin/env bash
#
# Hamlaneh installer v0 (Phase 0).
#
# Boots the Hamlaneh stack on a fresh Linux host:
#   1. verifies root and a supported OS (Ubuntu, Debian, Fedora, RHEL clones)
#      — anything else exits with a clear error before touching the system
#   2. installs Docker via get.docker.com only if it is missing
#   3. resolves the domain/IP (prompt, --domain flag, or existing .env),
#      generates a random Postgres password into deploy/.env (chmod 600)
#   4. builds and starts the stack: docker compose up -d --build
#
# Idempotent: a second run keeps the existing deploy/.env untouched and
# converges the already-running stack without destructive changes.
#
# Usage:
#   sudo ./install.sh [--domain <domain-or-ip>] [--non-interactive]

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/.env"
COMPOSE_FILE="${SCRIPT_DIR}/docker-compose.yml"

DOMAIN=""
NON_INTERACTIVE=0

log() { printf '[hamlaneh] %s\n' "$*"; }

fail() {
  printf '[hamlaneh] ERROR: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Hamlaneh installer v0

Usage: sudo ./install.sh [options]

Options:
  --domain <domain-or-ip>  Domain or IP to serve Hamlaneh on (skips the prompt)
  --non-interactive        Never prompt; use --domain, the existing deploy/.env
                           value, or "localhost" (in that order)
  -h, --help               Show this help and exit
EOF
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --domain)
        [ $# -ge 2 ] || fail "--domain requires a value"
        DOMAIN="$2"
        shift 2
        ;;
      --non-interactive)
        NON_INTERACTIVE=1
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        usage >&2
        fail "unknown argument: $1"
        ;;
    esac
  done
}

require_root() {
  [ "$(id -u)" -eq 0 ] || fail "this installer must run as root (try: sudo $0)"
}

# Supported: Ubuntu, Debian, Fedora, RHEL and its clones (CentOS Stream,
# Rocky, AlmaLinux). Anything else: clear error, exit 1, nothing installed.
require_supported_os() {
  [ -r /etc/os-release ] || fail "cannot read /etc/os-release — unsupported OS. Supported: Ubuntu, Debian, Fedora, RHEL and clones. Nothing was installed."
  # shellcheck disable=SC1091
  . /etc/os-release
  local os_ids
  os_ids=" ${ID:-} ${ID_LIKE:-} "
  case "$os_ids" in
    *" ubuntu "*|*" debian "*|*" fedora "*|*" rhel "*|*" centos "*|*" rocky "*|*" almalinux "*)
      log "detected supported OS: ${PRETTY_NAME:-${ID:-unknown}}"
      ;;
    *)
      fail "unsupported OS: ${PRETTY_NAME:-${ID:-unknown}}. Supported: Ubuntu, Debian, Fedora, RHEL and clones (CentOS Stream, Rocky, AlmaLinux). Nothing was installed."
      ;;
  esac
}

require_checkout() {
  [ -f "$COMPOSE_FILE" ] || fail "docker-compose.yml not found next to install.sh — run from a full Hamlaneh checkout"
  [ -d "${SCRIPT_DIR}/../server" ] || fail "server/ source directory not found — run from a full Hamlaneh checkout"
  command -v openssl >/dev/null 2>&1 || fail "openssl is required to generate secrets; install it and re-run"
}

ensure_docker() {
  if command -v docker >/dev/null 2>&1; then
    docker compose version >/dev/null 2>&1 || fail "Docker is installed but the 'docker compose' plugin is missing; install the docker-compose-plugin package for your distribution and re-run"
    log "Docker already installed: $(docker --version)"
    return
  fi

  log "Docker not found — installing via get.docker.com ..."
  command -v curl >/dev/null 2>&1 || fail "curl is required to download the Docker install script; install curl and re-run"

  local tmp
  tmp="$(mktemp)"
  if ! curl -fsSL https://get.docker.com -o "$tmp"; then
    rm -f "$tmp"
    fail "failed to download the Docker install script"
  fi
  if ! sh "$tmp"; then
    rm -f "$tmp"
    fail "Docker installation failed"
  fi
  rm -f "$tmp"

  if command -v systemctl >/dev/null 2>&1; then
    systemctl enable --now docker >/dev/null 2>&1 || true
  fi

  docker compose version >/dev/null 2>&1 || fail "Docker was installed but 'docker compose' is still unavailable"
  log "Docker installed: $(docker --version)"
}

current_env_domain() {
  [ -f "$ENV_FILE" ] || return 0
  sed -n 's/^HAMLANEH_DOMAIN=//p' "$ENV_FILE" | tail -n 1
}

validate_domain() {
  case "$1" in
    ""|*[!A-Za-z0-9.:-]*)
      fail "invalid domain or IP: '$1' (allowed characters: letters, digits, dots, hyphens, colons)"
      ;;
  esac
}

resolve_domain() {
  if [ -n "$DOMAIN" ]; then
    validate_domain "$DOMAIN"
    return
  fi

  local existing
  existing="$(current_env_domain)"
  if [ -n "$existing" ]; then
    DOMAIN="$existing"
    log "using existing domain from deploy/.env: ${DOMAIN}"
    return
  fi

  if [ "$NON_INTERACTIVE" -eq 1 ]; then
    DOMAIN="localhost"
    log "non-interactive mode: defaulting domain to localhost"
    return
  fi

  printf 'Domain or IP to serve Hamlaneh on [localhost]: '
  read -r DOMAIN
  DOMAIN="${DOMAIN:-localhost}"
  validate_domain "$DOMAIN"
}

write_env() {
  if [ -f "$ENV_FILE" ]; then
    chmod 600 "$ENV_FILE"
    local existing
    existing="$(current_env_domain)"
    if [ "$existing" = "$DOMAIN" ]; then
      log "deploy/.env already up to date — leaving it untouched"
      return
    fi

    # Only the domain changes; the generated password is always kept.
    log "updating HAMLANEH_DOMAIN in deploy/.env (existing secrets kept)"
    local tmp
    tmp="$(mktemp "${ENV_FILE}.XXXXXX")"
    chmod 600 "$tmp"
    # validate_domain guarantees $DOMAIN contains no sed metacharacters.
    sed "s/^HAMLANEH_DOMAIN=.*/HAMLANEH_DOMAIN=${DOMAIN}/" "$ENV_FILE" > "$tmp"
    if ! grep -q '^HAMLANEH_DOMAIN=' "$tmp"; then
      printf 'HAMLANEH_DOMAIN=%s\n' "$DOMAIN" >> "$tmp"
    fi
    mv "$tmp" "$ENV_FILE"
    return
  fi

  log "generating deploy/.env with a random database password"
  local password old_umask
  password="$(openssl rand -base64 32)"
  old_umask="$(umask)"
  umask 077
  {
    printf '# Generated by install.sh on %s — DO NOT COMMIT.\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'HAMLANEH_DOMAIN=%s\n' "$DOMAIN"
    printf 'POSTGRES_PASSWORD=%s\n' "$password"
  } > "$ENV_FILE"
  umask "$old_umask"
  chmod 600 "$ENV_FILE"
}

start_stack() {
  log "building images and starting the stack (first run can take a few minutes)"
  docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d --build
}

print_success() {
  log "Hamlaneh is up."
  log "URL: https://${DOMAIN}/"
  if [ "$DOMAIN" = "localhost" ]; then
    log "Note: with a localhost/IP address the certificate comes from Caddy's internal CA — your browser will warn until you trust that CA."
  fi
  log "Status:          docker compose -f ${COMPOSE_FILE} ps"
  log "Verify defaults: ${SCRIPT_DIR}/verify-defaults.sh"
}

main() {
  parse_args "$@"
  require_root
  require_supported_os
  require_checkout
  ensure_docker
  resolve_domain
  write_env
  start_stack
  print_success
}

main "$@"
