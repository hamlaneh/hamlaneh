#!/usr/bin/env bash
#
# Hamlaneh installer v0 (Phase 0).
#
# Boots the Hamlaneh stack on a fresh Linux host:
#   1. verifies root and a supported OS (Ubuntu, Debian, Fedora, RHEL clones)
#      — anything else exits with a clear error before touching the system
#   2. installs Docker via get.docker.com only if it is missing
#   3. resolves the domain/IP (prompt, --domain flag, or existing .env),
#      generates a random Postgres password into deploy/.env (chmod 600),
#      and writes the optional settings .env.example documents with empty
#      values, so turning email on later is an edit rather than a search
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
ADMIN_PASSWORD=""
ADMIN_PASSWORD_NEW=0
ADMIN_PASSWORD_APPENDED=0

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
  ADMIN_PASSWORD="$(openssl rand -base64 18)"
  ADMIN_PASSWORD_NEW=1
  old_umask="$(umask)"
  umask 077
  {
    printf '# Generated by install.sh on %s — DO NOT COMMIT.\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'HAMLANEH_DOMAIN=%s\n' "$DOMAIN"
    printf 'POSTGRES_PASSWORD=%s\n' "$password"
    printf 'HAMLANEH_ADMIN_USERNAME=admin\n'
    printf 'HAMLANEH_ADMIN_PASSWORD=%s\n' "$ADMIN_PASSWORD"
    printf 'HAMLANEH_ADMIN_LOCALE=en\n'
    print_mail_env
  } > "$ENV_FILE"
  umask "$old_umask"
  chmod 600 "$ENV_FILE"
}

# The optional settings .env.example documents, written into the generated
# .env so an operator turning email on edits a key that is already there
# instead of hunting for its name. .env.example points the reader at this
# file as the generated one; leaving them out made that a dead end.
#
# Every value is EMPTY, and that is what keeps zero-config zero-config:
# compose reads each as ${VAR:-default}, which treats empty exactly like
# unset, so the public URL still falls back to https://${HAMLANEH_DOMAIN}
# and an empty SMTP host still leaves password reset switched off. Filling
# one of these in is the deliberate act that turns the feature on.
print_mail_env() {
  cat <<'EOF'

# Absolute public origin, used to build links that go out by email. Empty
# defaults to https://${HAMLANEH_DOMAIN}, which is right for almost every
# install; set it only when the public URL differs (a path prefix, or a
# domain Caddy does not know about). No query, no fragment.
#   e.g.  HAMLANEH_PUBLIC_URL=https://chat.example.invalid/hamlaneh
HAMLANEH_PUBLIC_URL=

# Password reset by email — OFF by default.
#
# Leaving HAMLANEH_SMTP_HOST empty disables password reset entirely: the
# endpoints stay reachable and answer exactly as they do for an address that
# does not exist, and the sign-in screen omits the "Forgot password?" link
# rather than offering one that goes nowhere.
#
# Setting the host turns reset on and makes HAMLANEH_SMTP_FROM required. A
# half-configured transport stops the server with a message naming the
# missing variable, instead of failing at somebody's first forgotten
# password.
#   e.g.  HAMLANEH_SMTP_HOST=smtp.example.invalid
#         HAMLANEH_SMTP_FROM=hamlaneh@chat.example.invalid
#         HAMLANEH_SMTP_FROM_NAME=Hamlaneh
HAMLANEH_SMTP_HOST=
HAMLANEH_SMTP_FROM=
HAMLANEH_SMTP_FROM_NAME=

# Connection protection: starttls (the default), tls (implicit TLS), or
# none. Empty means starttls. "none" exists only for a relay reachable
# across a private container network and must always be chosen deliberately.
HAMLANEH_SMTP_ENCRYPTION=

# Port. Empty means the conventional port for the encryption mode: 587 for
# starttls, 465 for tls, 25 for none.
HAMLANEH_SMTP_PORT=

# Submission credentials: set both or neither.
HAMLANEH_SMTP_USERNAME=
HAMLANEH_SMTP_PASSWORD=
EOF
}

# Upgrade path for the block above, mirroring ensure_admin_env: an .env from
# an older install predates these keys. Appending them changes nothing —
# every value is empty, which is what the server already assumed — it just
# puts the names where the operator can find them.
ensure_mail_env() {
  [ -f "$ENV_FILE" ] || return 0
  if grep -q '^HAMLANEH_SMTP_HOST=' "$ENV_FILE"; then
    return 0
  fi
  log "adding the optional mail settings to deploy/.env (all empty; password reset stays off)"
  print_mail_env >> "$ENV_FILE"
}

# Upgrade path: an .env from an older install may predate the admin bootstrap
# variables. Appending them is safe — the server only reads them while the
# users table is empty — but on an instance that already has users they are
# inert, so this path must NOT announce "first admin credentials".
ensure_admin_env() {
  [ -f "$ENV_FILE" ] || return 0
  if grep -q '^HAMLANEH_ADMIN_USERNAME=' "$ENV_FILE"; then
    return 0
  fi
  log "adding admin bootstrap variables to deploy/.env (used only while the user table is empty)"
  ADMIN_PASSWORD_APPENDED=1
  {
    printf 'HAMLANEH_ADMIN_USERNAME=admin\n'
    printf 'HAMLANEH_ADMIN_PASSWORD=%s\n' "$(openssl rand -base64 18)"
    printf 'HAMLANEH_ADMIN_LOCALE=en\n'
  } >> "$ENV_FILE"
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
  if [ "$ADMIN_PASSWORD_NEW" -eq 1 ]; then
    log ""
    log "First admin account (shown once — you must change this password at first sign-in):"
    log "  Username: admin"
    log "  Password: ${ADMIN_PASSWORD}"
    log ""
  elif [ "$ADMIN_PASSWORD_APPENDED" -eq 1 ]; then
    log "Admin bootstrap variables were added to deploy/.env; they take effect only if the user table is empty (fresh database)."
  else
    log "Admin bootstrap credentials live in deploy/.env (HAMLANEH_ADMIN_*); they only apply while the user table is empty."
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
  ensure_admin_env
  ensure_mail_env
  start_stack
  print_success
}

main "$@"
