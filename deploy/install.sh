#!/usr/bin/env bash
#
# Hamlaneh installer.
#
# Boots the Hamlaneh stack on a fresh Linux host:
#   1. verifies root and a supported distribution (Ubuntu, Debian, Fedora,
#      RHEL and its clones) — anything else exits with a clear error before
#      touching the system
#   2. installs curl/openssl if the image is minimal, then Docker, by the
#      route that distribution actually supports (see ensure_docker)
#   3. resolves the domain/IP (prompt, --domain flag, or existing .env),
#      generates the random secrets into deploy/.env (chmod 600) — the
#      Postgres password, the key that signs file URLs, the audit-chain key
#      and the LiveKit credential pair calls are authorized by —
#      and writes the optional settings .env.example documents with empty
#      values, so turning email on later is an edit rather than a search
#   4. pre-flights the things that make an install fail late and confusingly:
#      a Docker daemon that is installed but not running, host ports already
#      taken, a domain that resolves nowhere, too little RAM to build
#   5. builds and starts the stack, then WAITS for it to answer /healthz —
#      "up" means serving, not "docker returned 0"
#   6. prints the security posture for this domain, and the ports a cloud
#      security group must be opened on, which is the one step no script
#      running on the host can perform
#
# Idempotent: a second run keeps the existing deploy/.env untouched and
# converges the already-running stack without destructive changes. It never
# regenerates a live secret, never runs `down`, and never touches a volume.
#
# Usage:
#   sudo ./install.sh [--domain <domain-or-ip>] [--non-interactive]
#
# Testing: deploy/install.test.sh sources this file (see the main guard at
# the bottom) and drives its functions directly. Keep functions free of
# global side effects at source time.

# -E so the ERR trap below is inherited by functions; without it every
# failure inside a function would die silently, which is the exact thing
# this installer must never do.
set -eEuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/.env"
COMPOSE_FILE="${SCRIPT_DIR}/docker-compose.yml"

# Compose project name, from the `name:` key in docker-compose.yml. Used to
# find our own volumes and containers without booting the stack first.
COMPOSE_PROJECT="hamlaneh"

# The only test seam in this script: os-release lives at a fixed path in
# production, and install.test.sh points it at a fixture to exercise every
# distribution branch on a host that is only ever one of them.
OS_RELEASE_FILE="${HAMLANEH_OS_RELEASE:-/etc/os-release}"

DOMAIN=""
NON_INTERACTIVE=0
ADMIN_PASSWORD=""
ADMIN_PASSWORD_NEW=0
ADMIN_PASSWORD_APPENDED=0

# Filled by detect_os.
OS_ID=""
OS_PRETTY=""
OS_FAMILY=""   # debian | rpm
DOCKER_ROUTE="" # get-docker | rpm-centos-repo

# Set by fail() so the ERR trap does not print a second, uglier report on
# top of an error we already explained.
FAILED=0

log() { printf '[hamlaneh] %s\n' "$*"; }
warn() { printf '[hamlaneh] WARNING: %s\n' "$*" >&2; }

fail() {
  FAILED=1
  printf '[hamlaneh] ERROR: %s\n' "$*" >&2
  exit 1
}

# Anything that fails without going through fail() is a bug in this script,
# not an operator mistake. Say so, name the line, and say what is safe to do
# next — a bare `set -e` death with no message is the failure mode this
# installer exists to avoid.
on_error() {
  local code=$? line="$1"
  [ "$FAILED" -eq 0 ] || exit "$code"
  printf '[hamlaneh] ERROR: unexpected failure at install.sh line %s (exit %s).\n' "$line" "$code" >&2
  printf '[hamlaneh] Nothing was deleted. Re-running this script is safe: it keeps deploy/.env\n' >&2
  printf '[hamlaneh] and every volume as they are. Please report this with the line number above.\n' >&2
  exit "$code"
}
trap 'on_error "$LINENO"' ERR

usage() {
  cat <<'EOF'
Hamlaneh installer

Usage: sudo ./install.sh [options]

Options:
  --domain <domain-or-ip>  Domain or IP to serve Hamlaneh on (skips the prompt)
  --non-interactive        Never prompt; use --domain, the existing deploy/.env
                           value, or "localhost" (in that order)
  -h, --help               Show this help and exit

Supported: Ubuntu, Debian, Fedora, RHEL and its clones (CentOS Stream, Rocky,
AlmaLinux). Anything else is refused before the system is touched.
EOF
}

parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --domain)
        [ $# -ge 2 ] || fail "--domain requires a value (e.g. --domain chat.example.com)"
        DOMAIN="$2"
        shift 2
        ;;
      --domain=*)
        DOMAIN="${1#--domain=}"
        shift
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

# Distribution detection.
#
# The four families the roadmap names differ in three ways that matter here:
# the package manager, the route Docker is installed by, and which host
# firewall is in the way. Only the first two change what this script DOES —
# the firewall is reported, never reconfigured (see print_host_notes).
#
# The Docker route is the part with a sharp edge. get.docker.com's own
# install case is `ubuntu|debian|raspbian` and `centos|fedora|rhel|rocky`;
# AlmaLinux is NOT in it and the script exits 1 with "Unsupported
# distribution 'almalinux'". Docker also publishes no almalinux repository
# (download.docker.com/linux/almalinux/docker-ce.repo is 404). The supported
# route for AlmaLinux — and for every other RHEL rebuild that get.docker.com
# has never heard of — is the CentOS repository, which is what
# rpm-centos-repo installs from.
detect_os() {
  [ -r "$OS_RELEASE_FILE" ] || fail "cannot read ${OS_RELEASE_FILE} — this does not look like a supported Linux. Supported: Ubuntu, Debian, Fedora, RHEL and clones. Nothing was installed."

  # Read os-release in a subshell-free way but without leaking its many
  # variables: only the three we need are copied out.
  local id="" id_like="" pretty=""
  # shellcheck disable=SC1090 # path is a constant in production, a fixture in tests
  id="$(. "$OS_RELEASE_FILE" >/dev/null 2>&1; printf '%s' "${ID:-}")"
  # shellcheck disable=SC1090
  id_like="$(. "$OS_RELEASE_FILE" >/dev/null 2>&1; printf '%s' "${ID_LIKE:-}")"
  # shellcheck disable=SC1090
  pretty="$(. "$OS_RELEASE_FILE" >/dev/null 2>&1; printf '%s' "${PRETTY_NAME:-}")"

  OS_ID="$id"
  OS_PRETTY="${pretty:-${id:-unknown}}"

  case "$id" in
    ubuntu|debian)
      OS_FAMILY="debian"; DOCKER_ROUTE="get-docker" ;;
    fedora|centos|rhel|rocky)
      OS_FAMILY="rpm"; DOCKER_ROUTE="get-docker" ;;
    almalinux)
      # Named explicitly rather than left to the ID_LIKE fallback below,
      # because the roadmap names it and a silent fallback is not coverage.
      OS_FAMILY="rpm"; DOCKER_ROUTE="rpm-centos-repo" ;;
    *)
      detect_os_from_id_like "$id_like" ;;
  esac

  log "detected ${OS_PRETTY} (family: ${OS_FAMILY}, Docker route: ${DOCKER_ROUTE})"
}

# A derivative of a supported distribution is supported, but it is said out
# loud: the operator should know which family's packages are about to be
# installed. Every rpm derivative goes through the CentOS repository rather
# than get.docker.com, for the same reason AlmaLinux does — get.docker.com
# refuses any ID it does not know by name.
detect_os_from_id_like() {
  local id_like=" $1 "
  case "$id_like" in
    *" ubuntu "*|*" debian "*)
      OS_FAMILY="debian"; DOCKER_ROUTE="get-docker"
      warn "${OS_PRETTY} is not a distribution this installer is tested on; treating it as Debian/Ubuntu because ID_LIKE says so." ;;
    *" rhel "*|*" centos "*|*" fedora "*)
      OS_FAMILY="rpm"; DOCKER_ROUTE="rpm-centos-repo"
      warn "${OS_PRETTY} is not a distribution this installer is tested on; treating it as a RHEL clone because ID_LIKE says so." ;;
    *)
      fail "unsupported distribution: ${OS_PRETTY}. Supported: Ubuntu, Debian, Fedora, RHEL and its clones (CentOS Stream, Rocky, AlmaLinux). Nothing was installed. If this is a derivative of one of those, its /etc/os-release is missing an ID_LIKE line." ;;
  esac
}

require_checkout() {
  [ -f "$COMPOSE_FILE" ] || fail "docker-compose.yml not found next to install.sh — run this from a full Hamlaneh checkout (git clone https://github.com/hamlaneh/hamlaneh.git && cd hamlaneh/deploy)"
  [ -d "${SCRIPT_DIR}/../server" ] || fail "server/ source directory not found — the stack is built from source, so a full checkout is required, not just deploy/"
}

pkg_install() {
  case "$OS_FAMILY" in
    debian)
      DEBIAN_FRONTEND=noninteractive apt-get update -qq ||
        fail "apt-get update failed. Check the host's network and /etc/apt/sources.list, then re-run."
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$@" ||
        fail "apt-get install of '$*' failed. Install those packages by hand and re-run."
      ;;
    rpm)
      local mgr="yum"
      command -v dnf >/dev/null 2>&1 && mgr="dnf"
      "$mgr" install -y -q "$@" ||
        fail "${mgr} install of '$*' failed. Install those packages by hand and re-run."
      ;;
    *)
      fail "internal error: pkg_install called before detect_os"
      ;;
  esac
}

# Minimal cloud images ship without curl or openssl. Failing with "openssl is
# required" on a machine that has a package manager and a network is a refusal
# where a two-second install would do.
ensure_prereqs() {
  local missing=()
  command -v curl >/dev/null 2>&1 || missing+=("curl")
  command -v openssl >/dev/null 2>&1 || missing+=("openssl")
  if [ "${#missing[@]}" -eq 0 ]; then
    return 0
  fi
  log "installing prerequisites: ${missing[*]}"
  pkg_install "${missing[@]}"
  command -v openssl >/dev/null 2>&1 || fail "openssl is still missing after installing it; secrets cannot be generated. Install openssl by hand and re-run."
  command -v curl >/dev/null 2>&1 || fail "curl is still missing after installing it. Install curl by hand and re-run."
}

install_docker_via_get_docker() {
  local tmp
  tmp="$(mktemp)"
  if ! curl -fsSL https://get.docker.com -o "$tmp"; then
    rm -f "$tmp"
    fail "could not download https://get.docker.com. Check the host's DNS and outbound HTTPS, then re-run."
  fi
  if ! sh "$tmp"; then
    rm -f "$tmp"
    fail "Docker's own install script failed on ${OS_PRETTY}. Its output is above. The usual cause is a distribution release Docker has no repository for yet (or one that has reached end of life); installing Docker Engine by hand per https://docs.docker.com/engine/install/ and re-running this script will finish the install."
  fi
  rm -f "$tmp"
}

# The AlmaLinux (and unknown-RHEL-clone) route. This is exactly what
# get.docker.com does for CentOS, minus the dnf-plugins-core dependency:
# `config-manager --add-repo` only ever writes the .repo file, and the flag
# for doing that is spelled differently in dnf4, dnf5 and yum. Downloading
# the file is the same operation in one line that works on all three.
install_docker_via_rpm_repo() {
  local repo="https://download.docker.com/linux/centos/docker-ce.repo"
  log "installing Docker from Docker's CentOS repository (get.docker.com does not support ${OS_ID})"
  curl -fsSL "$repo" -o /etc/yum.repos.d/docker-ce.repo ||
    fail "could not download ${repo}. Check the host's DNS and outbound HTTPS, then re-run."
  pkg_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
}

ensure_docker() {
  if command -v docker >/dev/null 2>&1; then
    log "Docker already installed: $(docker --version)"
  else
    log "Docker not found — installing ..."
    case "$DOCKER_ROUTE" in
      get-docker)      install_docker_via_get_docker ;;
      rpm-centos-repo) install_docker_via_rpm_repo ;;
      *)               fail "internal error: ensure_docker called before detect_os" ;;
    esac
    log "Docker installed: $(docker --version)"
  fi

  docker compose version >/dev/null 2>&1 ||
    fail "Docker is installed but the Compose v2 plugin is missing ('docker compose version' failed). Install the docker-compose-plugin package for ${OS_PRETTY} and re-run. The standalone docker-compose v1 binary is not a substitute."

  ensure_docker_daemon
}

# `docker compose version` answers from the CLI plugin alone and says nothing
# about the daemon. On the rpm family `dnf install docker-ce` does not start
# or enable the service, and after a reboot an un-enabled daemon is down
# again — so the installed-but-not-running host is the normal one there, not
# the exception. Without this check the install dies much later inside
# `compose up` with "Cannot connect to the Docker daemon", which reads like a
# Hamlaneh bug and is not one.
ensure_docker_daemon() {
  if docker info >/dev/null 2>&1; then
    return 0
  fi
  if command -v systemctl >/dev/null 2>&1; then
    log "Docker daemon is not responding — enabling and starting it"
    systemctl enable --now docker >/dev/null 2>&1 ||
      warn "systemctl enable --now docker reported an error; checking whether the daemon came up anyway"
    docker info >/dev/null 2>&1 && return 0
  fi
  fail "the Docker daemon is not running and could not be started. Check 'systemctl status docker' and 'journalctl -u docker -n 50'. A host without systemd (some LXC/OpenVZ VPS products) must start dockerd by whatever supervisor it does have before this script can continue."
}

current_env_domain() {
  [ -f "$ENV_FILE" ] || return 0
  sed -n 's/^HAMLANEH_DOMAIN=//p' "$ENV_FILE" | tail -n 1
}

validate_domain() {
  local d="$1"
  case "$d" in
    "")
      fail "the domain is empty. Pass --domain <domain-or-ip>, or answer the prompt." ;;
    -*)
      fail "invalid domain or IP: '${d}' — it must not start with a hyphen." ;;
    *[!A-Za-z0-9.:-]*)
      fail "invalid domain or IP: '${d}' (allowed characters: letters, digits, dots, hyphens, colons). A URL is not a domain — pass chat.example.com, not https://chat.example.com/." ;;
  esac
  [ "${#d}" -le 253 ] || fail "invalid domain: '${d}' is longer than 253 characters."
}

# What kind of address this is, which decides the entire TLS story:
#   domain    -> a publicly trusted certificate, issued automatically by ACME
#   ipv4/ipv6 -> no public CA will certify a bare IP over ACME; Caddy falls
#                back to its own internal CA and every browser warns
#   localhost -> internal CA as well, but only this machine can reach it
# Nothing about the install changes between them except what is TOLD to the
# operator. No default is softened to make the bare-IP case quieter.
domain_kind() {
  local d="$1"
  case "$d" in
    localhost) printf 'localhost' ;;
    *:*) printf 'ipv6' ;;
    *[!0-9.]*) printf 'domain' ;;
    *) printf 'ipv4' ;;
  esac
}

resolve_domain() {
  if [ -n "$DOMAIN" ]; then
    validate_domain "$DOMAIN"
    local existing
    existing="$(current_env_domain)"
    if [ -n "$existing" ] && [ "$existing" != "$DOMAIN" ]; then
      log "changing the domain from '${existing}' to '${DOMAIN}' — Caddy will obtain a new certificate for it"
    fi
    return
  fi

  local existing
  existing="$(current_env_domain)"
  if [ -n "$existing" ]; then
    DOMAIN="$existing"
    validate_domain "$DOMAIN"
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

    # Only the domain changes; every generated secret is always kept.
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

  log "generating deploy/.env with random secrets"
  local password file_url_key audit_key old_umask
  password="$(openssl rand -base64 32)"
  file_url_key="$(openssl rand -base64 32)"
  audit_key="$(openssl rand -base64 32)"
  ADMIN_PASSWORD="$(openssl rand -base64 18)"
  ADMIN_PASSWORD_NEW=1
  old_umask="$(umask)"
  umask 077
  {
    printf '# Generated by install.sh on %s — DO NOT COMMIT.\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'HAMLANEH_DOMAIN=%s\n' "$DOMAIN"
    printf 'POSTGRES_PASSWORD=%s\n' "$password"
    printf 'HAMLANEH_FILE_URL_KEY=%s\n' "$file_url_key"
    printf 'HAMLANEH_AUDIT_KEY=%s\n' "$audit_key"
    printf 'HAMLANEH_LIVEKIT_API_KEY=%s\n' "$(new_livekit_api_key)"
    printf 'HAMLANEH_LIVEKIT_API_SECRET=%s\n' "$(new_livekit_api_secret)"
    printf 'HAMLANEH_ADMIN_USERNAME=admin\n'
    printf 'HAMLANEH_ADMIN_PASSWORD=%s\n' "$ADMIN_PASSWORD"
    printf 'HAMLANEH_ADMIN_LOCALE=en\n'
    print_mail_env
  } > "$ENV_FILE"
  umask "$old_umask"
  chmod 600 "$ENV_FILE"
}

# The value .env.example ships for every secret it cannot ship a real one
# for. It is deliberately non-working, and two of the five keys that carry it
# are short enough that the server refuses to boot (internal/filesign and
# internal/audit both enforce a 32-byte floor) — which is the only reason a
# `cp .env.example .env` install has never produced a running instance with
# secrets printed in the repository.
PLACEHOLDER_VALUE="REPLACED_AT_INSTALL"

# README's quick start says `cp .env.example .env`. An operator who did that
# and then reached for the installer used to keep every placeholder: write_env
# refuses to touch an existing .env, and each ensure_* function below tests
# for the KEY, never for the VALUE.
#
# So: strip any required secret still holding the placeholder, and let the
# ensure_* functions regenerate it exactly as they do for an .env that
# predates the key. Regenerating these is safe precisely because the
# placeholder means the server never booted: no file URL was ever minted
# under that key, no audit entry sealed with it, no LiveKit token signed by
# it, and no admin ever created.
#
# Two groups are removed whole rather than per-key, for the same reason in
# both cases — the ensure_* function that regenerates them keys off a
# SIBLING variable, and a half-removed group would leave a placeholder behind
# that nothing regenerates:
#   the LiveKit pair, because ensure_livekit_env refuses a half-configured
#   pair and it must never be this function that creates one;
#   the admin trio, because ensure_admin_env tests HAMLANEH_ADMIN_USERNAME.
#
# POSTGRES_PASSWORD is handled separately by ensure_postgres_password_env:
# unlike the rest it may already be baked into an initialised database.
scrub_placeholder_secrets() {
  [ -f "$ENV_FILE" ] || return 0

  local groups=(
    "HAMLANEH_FILE_URL_KEY"
    "HAMLANEH_AUDIT_KEY"
    "HAMLANEH_LIVEKIT_API_KEY HAMLANEH_LIVEKIT_API_SECRET"
    "HAMLANEH_ADMIN_PASSWORD HAMLANEH_ADMIN_USERNAME HAMLANEH_ADMIN_LOCALE"
  )
  local group key remove=()
  for group in "${groups[@]}"; do
    # The first key of each group is the one that carries the placeholder;
    # the rest come with it so the group is regenerated as a unit.
    key="${group%% *}"
    if [ "$(env_value "$key")" = "$PLACEHOLDER_VALUE" ]; then
      # shellcheck disable=SC2206 # deliberate word split: the group is a key list
      remove+=($group)
    fi
  done

  if [ "${#remove[@]}" -eq 0 ]; then
    return 0
  fi

  log "deploy/.env still holds ${PLACEHOLDER_VALUE} placeholders — replacing them with generated secrets: ${remove[*]}"
  local tmp pattern
  pattern="^($(IFS='|'; printf '%s' "${remove[*]}"))="
  tmp="$(mktemp "${ENV_FILE}.XXXXXX")"
  chmod 600 "$tmp"
  grep -Ev "$pattern" "$ENV_FILE" > "$tmp" || true
  mv "$tmp" "$ENV_FILE"
}

# The value of one key in deploy/.env, empty if the file or the key is absent.
env_value() {
  [ -f "$ENV_FILE" ] || return 0
  sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1
}

# Whether this install already has a database volume — i.e. whether Postgres
# has been initialised, and therefore whether POSTGRES_PASSWORD is a value
# that can still be chosen or one the database is already using.
db_volume_exists() {
  docker volume inspect "${COMPOSE_PROJECT}_db_data" >/dev/null 2>&1
}

# POSTGRES_PASSWORD is the one secret that cannot simply be regenerated: it
# is written into the database at initdb and never read from the environment
# again. On a fresh host there is no database yet and generating it is right;
# on a host that already initialised one with the placeholder, changing it
# here would lock the server out of its own database with an authentication
# error nobody would connect back to this script.
#
# So that case stops, and says the two things the operator needs: the install
# has no data in it (the server cannot have booted — the file-URL key stopped
# it), and one command clears it.
ensure_postgres_password_env() {
  [ -f "$ENV_FILE" ] || return 0
  local value
  value="$(env_value POSTGRES_PASSWORD)"
  if [ -n "$value" ] && [ "$value" != "$PLACEHOLDER_VALUE" ]; then
    return 0
  fi

  if db_volume_exists; then
    fail "deploy/.env has POSTGRES_PASSWORD=${value:-<empty>} but a database volume (${COMPOSE_PROJECT}_db_data) already exists, so the database is using that value as its real password and this script must not change it silently.
       That state comes from copying .env.example and running docker compose by hand; the server cannot have started (it refuses the placeholder file-URL key), so the database is empty.
       Clear it and re-run:  docker compose -f ${COMPOSE_FILE} down -v && sudo ${0}
       If you believe the database DOES hold data, do not run that — set POSTGRES_PASSWORD in deploy/.env to the password the database was created with instead."
  fi

  log "generating POSTGRES_PASSWORD in deploy/.env"
  local tmp
  tmp="$(mktemp "${ENV_FILE}.XXXXXX")"
  chmod 600 "$tmp"
  grep -v '^POSTGRES_PASSWORD=' "$ENV_FILE" > "$tmp" || true
  printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -base64 32)" >> "$tmp"
  mv "$tmp" "$ENV_FILE"
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

# Upgrade path for an .env that predates the files origin. Unlike the mail
# and admin blocks, this one generates a real secret rather than an empty
# placeholder: the server refuses to start without it, and a key that changed
# between boots would invalidate every file URL already handed out. Generating
# it once, here, is what makes upgrading an existing install a no-op.
ensure_file_url_key_env() {
  [ -f "$ENV_FILE" ] || return 0
  if grep -q '^HAMLANEH_FILE_URL_KEY=' "$ENV_FILE"; then
    return 0
  fi
  log "generating HAMLANEH_FILE_URL_KEY in deploy/.env (signs the URLs uploaded files are served by)"
  printf 'HAMLANEH_FILE_URL_KEY=%s\n' "$(openssl rand -base64 32)" >> "$ENV_FILE"
}

# Upgrade path for an .env that predates the audit log, and the same shape as
# the one above for the same reason: the server refuses to start without this
# key. Generating it once, here, keeps upgrading an existing install a no-op
# — and once it exists it must never be regenerated, because every entry
# already recorded is sealed with it and would stop verifying.
ensure_audit_key_env() {
  [ -f "$ENV_FILE" ] || return 0
  if grep -q '^HAMLANEH_AUDIT_KEY=' "$ENV_FILE"; then
    return 0
  fi
  log "generating HAMLANEH_AUDIT_KEY in deploy/.env (keys the hash chain over the audit log)"
  printf 'HAMLANEH_AUDIT_KEY=%s\n' "$(openssl rand -base64 32)" >> "$ENV_FILE"
}

# The LiveKit credential pair (ADR 005). Hex, not the base64 the other
# secrets use, and deliberately: this pair is the only generated secret that
# ends up inside a YAML document — compose builds LIVEKIT_KEYS as
# "<key>: <secret>" — and hex cannot contain a character that YAML or that
# split would read as syntax. 128 and 256 bits respectively.
new_livekit_api_key() { openssl rand -hex 16; }
new_livekit_api_secret() { openssl rand -hex 32; }

# Upgrade path for an .env that predates calls, shaped like the file-URL and
# audit keys above for the same reason: the stack refuses to start without
# this pair, so a real secret is generated here rather than an empty
# placeholder. Both halves are written together — a half-configured pair is
# what compose is set up to reject, and it must never be this script that
# creates one. Once written they must not be regenerated: every token already
# minted is signed with the secret, and LiveKit would stop honouring them.
ensure_livekit_env() {
  [ -f "$ENV_FILE" ] || return 0
  if grep -q '^HAMLANEH_LIVEKIT_API_KEY=' "$ENV_FILE" &&
    grep -q '^HAMLANEH_LIVEKIT_API_SECRET=' "$ENV_FILE"; then
    return 0
  fi
  if grep -q '^HAMLANEH_LIVEKIT_API_KEY=\|^HAMLANEH_LIVEKIT_API_SECRET=' "$ENV_FILE"; then
    fail "deploy/.env has only one half of the LiveKit credential pair. Remove both HAMLANEH_LIVEKIT_API_KEY and HAMLANEH_LIVEKIT_API_SECRET and re-run, or set both by hand."
  fi
  log "generating the LiveKit credential pair in deploy/.env (mints the join tokens for calls)"
  {
    printf 'HAMLANEH_LIVEKIT_API_KEY=%s\n' "$(new_livekit_api_key)"
    printf 'HAMLANEH_LIVEKIT_API_SECRET=%s\n' "$(new_livekit_api_secret)"
  } >> "$ENV_FILE"
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

compose() {
  docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" "$@"
}

stack_exists() {
  [ -n "$(compose ps -aq 2>/dev/null)" ]
}

# Pre-flight the failures that otherwise surface late, from a tool that has
# no idea what Hamlaneh is. Every one of these is a warning or a clear stop
# BEFORE a five-minute image build, not after it.
preflight() {
  preflight_ports
  preflight_dns
  preflight_resources
}

# Ports 80 and 443 on a VPS that shipped with nginx or Apache running is the
# most boring install failure there is, and compose reports it as an opaque
# "address already in use" after the build has finished.
#
# Skipped entirely once our own stack exists: on a re-run the thing holding
# 80 and 443 is our own Caddy, and a pre-flight that fails on that would
# break the idempotency this script promises.
preflight_ports() {
  if stack_exists; then
    return 0
  fi
  local lister=()
  if command -v ss >/dev/null 2>&1; then
    lister=(ss -lntuH)
  elif command -v netstat >/dev/null 2>&1; then
    lister=(netstat -lntu)
  else
    return 0
  fi

  local listening taken=() port
  listening="$("${lister[@]}" 2>/dev/null || true)"
  for port in 80 443 3478 7881 7882; do
    if grep -Eq "[:.]${port}[[:space:]]" <<<"$listening"; then
      taken+=("$port")
    fi
  done
  if [ "${#taken[@]}" -gt 0 ]; then
    fail "these host ports are already in use by something else: ${taken[*]}. Hamlaneh needs 80, 443, 3478, 7881 and 7882. Stop whatever holds them (often nginx or apache2: 'systemctl disable --now nginx') and re-run. Nothing was changed."
  fi
}

# A domain that does not resolve is the difference between "TLS works" and
# "Caddy retries ACME forever". A warning rather than a stop, because DNS
# that is still propagating is a normal state to install in — Caddy will pick
# the certificate up on its own once the record lands.
preflight_dns() {
  [ "$(domain_kind "$DOMAIN")" = "domain" ] || return 0
  command -v getent >/dev/null 2>&1 || return 0
  if ! getent hosts "$DOMAIN" >/dev/null 2>&1; then
    warn "${DOMAIN} does not resolve from this host. Caddy needs it to point at this server's public IP before it can obtain a certificate; until then the site answers only over the untrusted internal-CA certificate. Add the DNS record — Caddy retries on its own, nothing here needs re-running."
  fi
}

# The stack is built from source on the host: a Go compile and a Vite build.
# On a 1 GB VPS the Vite build is the thing the kernel's OOM killer takes,
# and the resulting compose error names neither memory nor the webapp.
preflight_resources() {
  local mem_kb="" free_kb=""
  if [ -r /proc/meminfo ]; then
    mem_kb="$(sed -n 's/^MemTotal:[[:space:]]*\([0-9]*\).*/\1/p' /proc/meminfo | head -n 1)"
    if is_number "$mem_kb" && [ "$mem_kb" -lt 1900000 ]; then
      warn "this host has under 2 GB of RAM. The first run builds the web application, which can be killed by the kernel at that size. If the build dies without an error, add swap (fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile) and re-run."
    fi
  fi
  if command -v df >/dev/null 2>&1; then
    free_kb="$( { df -Pk /var/lib/docker 2>/dev/null || df -Pk /var 2>/dev/null || true; } | awk 'NR==2 {print $4}')"
    if is_number "$free_kb" && [ "$free_kb" -lt 5000000 ]; then
      warn "under 5 GB free where Docker stores images. Building the stack needs roughly that much; free some space if the build fails on 'no space left on device'."
    fi
  fi
}

is_number() {
  case "${1:-}" in
    ""|*[!0-9]*) return 1 ;;
    *) return 0 ;;
  esac
}

start_stack() {
  log "building images and starting the stack (first run can take a few minutes)"
  if ! compose up -d --build; then
    fail "docker compose failed to build or start the stack. Its output is above. Nothing was deleted — deploy/.env and every volume are untouched, so fixing the cause and re-running this script is safe."
  fi
}

# "docker compose up -d" returning 0 means the containers were CREATED. It
# says nothing about whether the server booted: a bad secret, a failed
# migration or an OOM-killed process all leave a crash-looping container and
# a zero exit code. Reporting "Hamlaneh is up" on that is the one lie this
# installer could tell that an operator would not catch for hours.
#
# So the success message is gated on the same thing a user experiences: an
# HTTPS request through Caddy that answers 200. Connecting to 127.0.0.1 with
# the configured domain as SNI is how verify-defaults.sh does it, and it
# works whether or not the domain resolves to this machine yet.
wait_for_stack() {
  local deadline=$((SECONDS + 300)) code=""
  log "waiting for the stack to answer /healthz (up to 5 minutes) ..."
  while [ "$SECONDS" -lt "$deadline" ]; do
    code="$(curl -sk --max-time 5 -o /dev/null -w '%{http_code}' \
      --connect-to "${DOMAIN}:443:127.0.0.1:443" "https://${DOMAIN}/healthz" 2>/dev/null || true)"
    if [ "$code" = "200" ]; then
      log "the stack is serving (/healthz returned 200)"
      return 0
    fi
    sleep 5
  done

  printf '\n[hamlaneh] --- container status ---\n' >&2
  compose ps >&2 || true
  printf '\n[hamlaneh] --- last 40 lines from the server ---\n' >&2
  compose logs --tail 40 server >&2 || true
  fail "the stack started but never answered https://${DOMAIN}/healthz (last status: ${code:-no response}). The output above usually names the cause on its first line. Nothing was deleted; fix the cause and re-run. Full logs: docker compose -f ${COMPOSE_FILE} logs"
}

print_success() {
  log "Hamlaneh is up."
  log "URL: https://${DOMAIN}/"
  print_tls_posture
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
  print_host_notes
  print_port_notice
}

# What the operator gets for TLS, stated plainly, because it is the single
# thing that differs between a domain install and a bare-IP one — and the
# difference is entirely in what a browser will believe, never in what this
# installer configured. Nothing below is a setting that was softened; the
# security headers, the container hardening and the closed database are
# identical in all three cases.
print_tls_posture() {
  case "$(domain_kind "$DOMAIN")" in
    domain)
      log "TLS: Caddy obtains a publicly trusted certificate for ${DOMAIN} automatically (ACME). Nothing to do."
      ;;
    localhost)
      log "TLS: localhost has no public certificate authority, so Caddy issues from its own internal CA. Your browser warns until you trust that CA (it lives in the caddy_data volume). Reachable only from this machine."
      ;;
    ipv4|ipv6)
      cat <<EOF

  ┌──────────────────────────────────────────────────────────────────────┐
  │  BARE-IP MODE — READ THIS, IT IS A REAL TRADE-OFF                    │
  │                                                                      │
  │  You gave an IP address rather than a domain, so which certificate   │
  │  you end up with is not certain, and the difference matters. Caddy    │
  │  asks Let's Encrypt for a certificate for the address itself, which   │
  │  it will issue ONLY for a public, routable IP that it can reach on    │
  │  ports 80 and 443 from the internet.                                 │
  │                                                                      │
  │    - if that succeeds, the certificate is publicly trusted and no    │
  │      browser warns. Good outcome, and nothing else to do.            │
  │    - if it does not — a LAN address, or a cloud firewall still shut  │
  │      — Caddy issues from its own internal CA instead. Then every     │
  │      browser shows a full-page warning that every user must click    │
  │      through, and a user trained to click through that warning       │
  │      cannot tell it apart from a real interception. That is the      │
  │      actual cost. HSTS is still sent but browsers ignore it over an  │
  │      untrusted certificate, so it protects nothing there either.     │
  │                                                                      │
  │  Which one you got:                                                  │
  │    docker compose -f ${COMPOSE_FILE}
  │      logs caddy | grep 'certificate obtained'                        │
  │    issuer "local" means the internal CA and the warning; anything    │
  │    else means a publicly trusted certificate.                        │
  │                                                                      │
  │  Either way Caddy will log repeated certificate failures for the     │
  │  files.${DOMAIN} hostname it also serves.
  │  No authority certifies an IP-suffixed name, so those never stop.    │
  │  Harmless here — bare-IP installs serve uploads from the main        │
  │  origin — but it is why that log is noisy.                           │
  │                                                                      │
  │  What this does NOT cost you: nothing was weakened to make bare-IP   │
  │  work. Same security headers, same non-root read-only containers,    │
  │  same closed database, traffic still encrypted in transit.           │
  │                                                                      │
  │  The fix is a domain name. Point any hostname at this address and    │
  │  re-run with --domain <that hostname>; the certificate becomes       │
  │  trusted with no other change to anything.                           │
  └──────────────────────────────────────────────────────────────────────┘

EOF
      ;;
  esac
}

# Two host facts that change nothing this script did, and that an operator
# will otherwise spend an afternoon on.
print_host_notes() {
  if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce 2>/dev/null || true)" = "Enforcing" ]; then
    log "SELinux is Enforcing. Nothing to relabel: the stack uses named volumes only, never a host bind mount, so no :z or :Z is needed anywhere."
  fi

  if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    log "firewalld is running. Published container ports bypass it by design (Docker writes its own DNAT/FORWARD rules), so nothing needs opening on the host — but note that 'firewall-cmd --reload' flushes those rules, and containers stay unreachable until 'systemctl restart docker'."
  elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q '^Status: active'; then
    log "ufw is active. Published container ports bypass it by design (Docker writes its own DNAT/FORWARD rules), so the ports below are already reachable even though 'ufw status' does not list them."
  fi
}

# The one manual step this installer cannot do for you.
#
# Compose punches its own holes in the host firewall (that is what publishing
# a port means), so on a plain VPS everything below is already reachable. A
# cloud provider's security group is a DIFFERENT firewall, upstream of the
# host and beyond the reach of any script running on it — an AWS security
# group, a Hetzner or DigitalOcean cloud firewall, an Azure NSG, a GCP VPC
# rule. On those, ports stay shut until a human opens them in a console this
# script cannot see, and the failure is silent and confusing: sign-in works,
# chat works, and calls connect to nothing.
#
# Printed last and printed always, including on a repeat run — the operator
# who needs it most is the one upgrading an existing install onto a locked
# down VPS, and they would never see a notice that only appeared once.
print_port_notice() {
  cat <<'EOF'

  ┌──────────────────────────────────────────────────────────────────────┐
  │  OPEN THESE PORTS IN YOUR CLOUD PROVIDER'S FIREWALL                  │
  │                                                                      │
  │  This installer already opened them on the host. It CANNOT open a    │
  │  cloud security group (AWS, Azure NSG, GCP, Hetzner, DigitalOcean),  │
  │  and calls fail silently for as long as those stay shut.             │
  │                                                                      │
  │      80/tcp    HTTP — redirects to HTTPS, and ACME certificates      │
  │     443/tcp    HTTPS — the application                               │
  │     443/udp    HTTP/3                                                │
  │    3478/udp    calls: TURN, the relay for restrictive networks       │
  │    7881/tcp    calls: media over TCP, where UDP is blocked           │
  │    7882/udp    calls: media                                          │
  │                                                                      │
  │  Nothing else. Do NOT open 7880: LiveKit's admin API is on it, and   │
  │  it is meant to stay on the internal network.                        │
  └──────────────────────────────────────────────────────────────────────┘

EOF
}

main() {
  parse_args "$@"
  require_root
  detect_os
  require_checkout
  ensure_prereqs
  ensure_docker
  resolve_domain
  write_env
  scrub_placeholder_secrets
  ensure_postgres_password_env
  ensure_admin_env
  ensure_mail_env
  ensure_file_url_key_env
  ensure_audit_key_env
  ensure_livekit_env
  preflight
  start_stack
  wait_for_stack
  print_success
}

# Sourced by install.test.sh, executed by everyone else.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
