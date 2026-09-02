#!/usr/bin/env bash
#
# Hamlaneh installer.
#
# Boots the Hamlaneh stack on a fresh Linux host:
#   0. when run standalone — `bash <(curl -fsSL …/deploy/install.sh)` — it
#      first fetches the repository into /opt/hamlaneh and hands over to the
#      installer inside it, because the stack builds from source until the
#      first published release (see bootstrap_checkout)
#   1. verifies root and a supported distribution (Ubuntu, Debian, Fedora,
#      RHEL and its clones) — anything else exits with a clear error before
#      touching the system
#   2. installs curl/openssl if the image is minimal, then Docker, by the
#      route that distribution actually supports (see ensure_docker)
#   3. resolves the domain/IP (prompt, --domain flag, or existing .env),
#      generates the random secrets into deploy/.env (chmod 600) — the
#      Postgres password, the key that signs file URLs, the audit-chain key
#      and the LiveKit credential pair calls are authorized by —
#      and writes the optional MAIL settings .env.example documents with
#      empty values, so turning email on later is an edit rather than a
#      search. The SSO block is not written: an install that wants OIDC
#      copies those four keys out of .env.example
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
# runs `down` and never touches a volume, and the only secret it ever replaces
# in place is a REPLACED_AT_INSTALL placeholder — a constant published in this
# repository, which is a hole rather than a secret. See
# scrub_placeholder_secrets for what that costs when the instance is live.
#
# Usage:
#   sudo ./install.sh [--domain <domain-or-ip>] [--non-interactive]
#   bash <(curl -fsSL https://raw.githubusercontent.com/hamlaneh/hamlaneh/main/deploy/install.sh)
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
# The web ports this install publishes. 80/443 are the product's promise —
# a domain's trusted certificate is only issuable there — and they move only
# when something else already holds them AND the operator chooses to move
# (IP and localhost installs only; resolve_ports owns the conversation).
HTTP_PORT="${HAMLANEH_HTTP_PORT:-80}"
HTTPS_PORT="${HAMLANEH_HTTPS_PORT:-443}"
# The admin dashboard's own port (ADR 015), and which host interface publishes
# it. Both empty means the split is OFF — every admin route stays on the web
# port, exactly where it was before this feature — and that is what a
# --non-interactive run gets, so no existing script changes meaning.
# resolve_admin_port owns the conversation.
ADMIN_PORT=""
ADMIN_BIND=""
ADMIN_USERNAME="admin"
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

# Seconds between heartbeat lines; a test seam so the suite can exercise the
# beat without waiting twenty real seconds for one.
HEARTBEAT_INTERVAL="${HAMLANEH_HEARTBEAT_INTERVAL:-20}"

# Runs a long command while proving it is alive. The command's own output
# still streams; on top of it, every HEARTBEAT_INTERVAL seconds one line
# says how long it has run and how much the package cache has grown — a real
# download meter, read from disk. Exists because the longest step of a fresh
# install is Docker's own installer, whose inner apt/dnf runs fully
# silenced, and an operator watching multi-minute silence reads it as a hang
# (a real report from the first VPS install). Returns the command's own
# status; the ERR trap is cleared in the child so a failure is reported once,
# by whoever owns the message, not twice.
heartbeat_run() {
  local label="$1"
  shift
  (
    trap - ERR
    "$@"
  ) &
  local pid=$! started="$SECONDS" dir cache="" mb
  for dir in /var/cache/apt/archives /var/cache/dnf /var/cache/yum; do
    [ -d "$dir" ] && cache="$dir" && break
  done
  while kill -0 "$pid" 2>/dev/null; do
    sleep "$HEARTBEAT_INTERVAL"
    kill -0 "$pid" 2>/dev/null || break
    mb=""
    if [ -n "$cache" ]; then
      mb=", $(du -sm "$cache" 2>/dev/null | cut -f1 || printf '?') MB in the package cache"
    fi
    log "${label} — still working ($(( (SECONDS - started) / 60 ))m$(( (SECONDS - started) % 60 ))s elapsed${mb})"
  done
  wait "$pid"
}

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

# Where a standalone run (`bash <(curl …/install.sh)`) puts the checkout it
# fetches for itself. The stack builds from source until the first published
# release, so the files have to exist somewhere; /opt is where an operator
# expects installed software to live.
REPO_GIT_URL="https://github.com/hamlaneh/hamlaneh.git"
REPO_TARBALL_URL="https://github.com/hamlaneh/hamlaneh/archive/refs/heads/main.tar.gz"
CHECKOUT_DIR="${HAMLANEH_CHECKOUT_DIR:-/opt/hamlaneh}"

# Fetches the repository and hands over to the installer inside it, passing
# the original arguments through. git when available (a later run can pull),
# tarball over TLS otherwise — curl and tar are installed if missing, which
# is why this runs after detect_os. On a refresh, deploy/.env is the
# instance's identity and is carried over; the rest of the tree is
# disposable and replaced whole.
bootstrap_checkout() {
  [ "${HAMLANEH_BOOTSTRAPPED:-0}" -eq 0 ] ||
    fail "the fetched checkout at ${CHECKOUT_DIR} is still incomplete — the download or extraction went wrong. Remove ${CHECKOUT_DIR} and re-run, or clone manually: git clone ${REPO_GIT_URL}"
  log "no checkout around this script — fetching Hamlaneh into ${CHECKOUT_DIR}"
  if [ -d "${CHECKOUT_DIR}/.git" ] && command -v git >/dev/null 2>&1; then
    git -C "$CHECKOUT_DIR" pull --ff-only ||
      fail "git pull in ${CHECKOUT_DIR} failed — fix what it reported (or remove the directory) and re-run."
  elif command -v git >/dev/null 2>&1 && [ ! -e "$CHECKOUT_DIR" ]; then
    git clone "$REPO_GIT_URL" "$CHECKOUT_DIR" ||
      fail "git clone of ${REPO_GIT_URL} failed. Check the host's network and re-run."
  else
    command -v curl >/dev/null 2>&1 || pkg_install curl
    command -v tar >/dev/null 2>&1 || pkg_install tar
    local tmp extracted
    tmp="$(mktemp -d)"
    curl -fsSL "$REPO_TARBALL_URL" -o "${tmp}/src.tar.gz" ||
      fail "downloading ${REPO_TARBALL_URL} failed. Check the host's network and re-run."
    tar -xzf "${tmp}/src.tar.gz" -C "$tmp" ||
      fail "extracting the downloaded archive failed. Re-run to download it again."
    extracted="$(find "$tmp" -maxdepth 1 -mindepth 1 -type d | head -n 1)"
    if [ -z "$extracted" ] || [ ! -f "${extracted}/deploy/docker-compose.yml" ]; then
      fail "the downloaded archive does not look like a Hamlaneh checkout. Re-run, or clone manually: git clone ${REPO_GIT_URL}"
    fi
    if [ -f "${CHECKOUT_DIR}/deploy/.env" ]; then
      cp "${CHECKOUT_DIR}/deploy/.env" "${extracted}/deploy/.env"
      chmod 600 "${extracted}/deploy/.env"
    fi
    rm -rf "${CHECKOUT_DIR}.old"
    if [ -e "$CHECKOUT_DIR" ]; then
      mv "$CHECKOUT_DIR" "${CHECKOUT_DIR}.old"
    fi
    mkdir -p "$(dirname "$CHECKOUT_DIR")"
    mv "$extracted" "$CHECKOUT_DIR"
    rm -rf "${CHECKOUT_DIR}.old" "$tmp"
  fi
  log "handing over to ${CHECKOUT_DIR}/deploy/install.sh"
  HAMLANEH_BOOTSTRAPPED=1 exec bash "${CHECKOUT_DIR}/deploy/install.sh" "$@"
}

require_checkout() {
  if [ ! -f "$COMPOSE_FILE" ] || [ ! -d "${SCRIPT_DIR}/../server" ]; then
    bootstrap_checkout "$@"
  fi
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
  log "Docker's installer downloads a few hundred MB and its package steps print nothing — expect a few silent minutes; the heartbeat below is the proof of life"
  if ! heartbeat_run "installing Docker" sh "$tmp"; then
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
  # The child's own fail() already explains a failure; the parent only has
  # to not add a second, worse report on top (FAILED quiets the ERR trap).
  heartbeat_run "installing Docker packages" \
    pkg_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin ||
    { FAILED=1; exit 1; }
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

# cosign, pinned.
#
# deploy/verify-release.sh and deploy/hamlaneh-update.sh both shell out to
# `cosign`, and this script enables the update timer that runs the second of
# those four times a day. Without cosign on the host that timer fails on every
# fire, inside a systemd unit nobody reads — so "security patches install
# themselves" would be false on every machine while looking true.
#
# The version is the one .github/workflows/release.yml and ci.yml pin, and it
# is meant to stay in lockstep with them: the pipeline that SIGNS and the host
# that VERIFIES disagreeing about cosign is the kind of drift that gets found
# during an incident rather than before one.
#
# The download is checked against a SHA-256 pinned HERE, in the repository,
# not one fetched next to the binary. A checksum served by the same host as
# the file it describes proves only that the transfer was not corrupted;
# pinning it in the source tree is what makes it an assertion about WHICH
# binary. Cosign's own signature would be stronger and is not available at
# this point: verifying it needs a working cosign, which is the thing being
# installed.
COSIGN_VERSION="v3.0.6"
COSIGN_SHA256_AMD64="c956e5dfcac53d52bcf058360d579472f0c1d2d9b69f55209e256fe7783f4c74"
COSIGN_SHA256_ARM64="bedac92e8c3729864e13d4a17048007cfafa79d5deca993a43a90ffe018ef2b8"

# Set by the three functions below so print_success can state what is actually
# switched on, rather than what was intended to be.
COSIGN_STATE="not installed"
UPDATE_TIMER_STATE="not enabled"
BACKUP_TIMER_STATE="not enabled"

ensure_cosign() {
  if command -v cosign >/dev/null 2>&1; then
    # An operator's existing cosign is left exactly where it is: clobbering a
    # binary this script did not install is not something a re-run may do.
    COSIGN_STATE="already present ($(cosign version 2>/dev/null | sed -n 's/^ *GitVersion: *//p' | head -n 1 || true))"
    log "cosign already installed — leaving it alone (${COSIGN_STATE})"
    return 0
  fi

  local arch sha
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) arch="amd64"; sha="$COSIGN_SHA256_AMD64" ;;
    aarch64|arm64) arch="arm64"; sha="$COSIGN_SHA256_ARM64" ;;
    *)
      warn "no pinned cosign build for this architecture (${arch}), so release-signature verification and automatic updates stay off. Install cosign ${COSIGN_VERSION} by hand from https://github.com/sigstore/cosign/releases and re-run this script."
      return 0
      ;;
  esac

  local url tmp
  url="https://github.com/sigstore/cosign/releases/download/${COSIGN_VERSION}/cosign-linux-${arch}"
  log "installing cosign ${COSIGN_VERSION} (verifies the signature on every update)"
  tmp="$(mktemp)"
  if ! curl -fsSL "$url" -o "$tmp"; then
    rm -f "$tmp"
    warn "could not download cosign from ${url}. Release-signature verification and automatic updates stay off until it is installed. Nothing else about this install is affected; re-run this script once the host can reach github.com."
    return 0
  fi
  if ! printf '%s  %s\n' "$sha" "$tmp" | sha256sum -c - >/dev/null 2>&1; then
    rm -f "$tmp"
    fail "the cosign ${COSIGN_VERSION} download did not match its pinned SHA-256. This is not a transient error: either the release was re-cut or the download was tampered with. Nothing was installed. Do not work around this — report it."
  fi
  install -m 0755 "$tmp" /usr/local/bin/cosign
  rm -f "$tmp"
  COSIGN_STATE="${COSIGN_VERSION} (installed by this script)"
  log "cosign ${COSIGN_VERSION} installed to /usr/local/bin/cosign"
}

# Automatic security updates and automatic backups are "on by default" only
# if something turns them on. Both sibling scripts write and enable their own
# systemd unit and both are idempotent, so the whole job here is one call each
# — but a failure must be loud, because the failure mode is a feature that
# looks enabled and is not.
#
# Neither is fatal: the instance is up and serving by this point, and refusing
# the install over a timer would trade a working deployment for a broken one.
# print_success states what actually happened either way.
enable_companion() {
  local script="$1" label="$2"
  shift 2
  if [ ! -f "${SCRIPT_DIR}/${script}" ]; then
    warn "${SCRIPT_DIR}/${script} is missing, so ${label} is NOT enabled. That file ships with Hamlaneh — an incomplete checkout is the usual cause. Restore it and run: sudo ${SCRIPT_DIR}/${script} $*"
    return 1
  fi
  if bash "${SCRIPT_DIR}/${script}" "$@"; then
    return 0
  fi
  warn "${SCRIPT_DIR}/${script} $* failed, so ${label} is NOT enabled. Its output is above. Everything else about this install is fine; run that command by hand once the cause is fixed."
  return 1
}

enable_update_timer() {
  if [ "$COSIGN_STATE" = "not installed" ]; then
    warn "automatic updates are NOT enabled, because cosign is not installed and the updater refuses to apply a release it cannot verify. Install cosign and re-run this script."
    return 0
  fi
  if enable_companion hamlaneh-update.sh "automatic security updates" --install-timer; then
    UPDATE_TIMER_STATE="enabled (hamlaneh-update.timer, four times a day)"
  fi
}

enable_backup_timer() {
  # No mechanism is named here on purpose: hamlaneh-backup.sh installs a
  # systemd timer where there is one and a /etc/cron.d entry where there is
  # not, and it logs which it chose one line above this. Claiming a systemd
  # timer on a host that got the cron entry would send the operator looking
  # for a unit that does not exist.
  if enable_companion hamlaneh-backup.sh "automated encrypted backups" enable; then
    BACKUP_TIMER_STATE="enabled, daily (see the line above for systemd or cron)"
  fi
}

current_env_domain() {
  [ -f "$ENV_FILE" ] || return 0
  sed -n 's/^HAMLANEH_DOMAIN=//p' "$ENV_FILE" | tail -n 1
}

current_env_value() {
  [ -f "$ENV_FILE" ] || return 0
  sed -n "s/^$1=//p" "$ENV_FILE" | tail -n 1
}

# One `ss`/`netstat` snapshot, shared by the interactive port conversation
# and the final preflight so the two can never disagree about what "taken"
# means. Empty output means no lister exists; callers treat that as unknown
# rather than free-or-taken.
listening_snapshot() {
  if command -v ss >/dev/null 2>&1; then
    ss -lntuH 2>/dev/null || true
  elif command -v netstat >/dev/null 2>&1; then
    netstat -lntu 2>/dev/null || true
  fi
}

snapshot_has_port() {
  grep -Eq "[:.]$2[[:space:]]" <<<"$1"
}

# The process name on a listening port — "nginx", not a bare number — so
# the operator's decision is about something they recognise. Best-effort:
# needs ss and root; empty when either is missing.
port_holder() {
  command -v ss >/dev/null 2>&1 || return 0
  ss -lntpH "sport = :$1" 2>/dev/null | grep -oE 'users:\(\("[^"]+"' | head -n 1 | cut -d'"' -f2 || true
}

valid_port() {
  case "$1" in "" | *[!0-9]*) return 1 ;; esac
  [ "$1" -ge 1 ] && [ "$1" -le 65535 ]
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
#   ipv4/ipv6 -> Caddy classifies every IP address as an internal name and
#                issues from its own CA without asking a public one, so every
#                browser warns. Let's Encrypt has issued IP-address
#                certificates under its short-lived profile since January 2026,
#                but Caddy 2.10 — the version deploy/Dockerfile.caddy pins —
#                does not request them, and our Caddyfile selects no profile.
#   localhost -> internal CA as well, but only this machine can reach it
# No security default is softened to make the bare-IP case quieter; the only
# things that vary are what the operator is TOLD and the files hostname below.
domain_kind() {
  local d="$1"
  case "$d" in
    localhost | localhost:[0-9]*) printf 'localhost'; return ;;
    *:*:*) printf 'ipv6'; return ;;
  esac
  # One colon followed by digits is a host:port pair — classify the host.
  # Only an IP or localhost may actually carry one (resolve_ports enforces
  # it); classifying the host correctly here is what lets that refusal name
  # the real problem instead of calling chat.example.com:8443 an IPv6.
  case "$d" in
    *:[0-9]*) d="${d%%:*}" ;;
  esac
  case "$d" in
    *[!0-9.]*) printf 'domain' ;;
    *) printf 'ipv4' ;;
  esac
}
# The hostname the files origin (ADR 003) is served from.
#
# Only a real domain gets files.<domain>. "files." in front of a bare IP is
# neither an IP -- so Caddy's internal CA will not cover it -- nor a name
# anybody can pass an ACME challenge for, so Caddy retries an impossible
# certificate order for a month. Those installs serve /files/* from the app
# origin, which carries the identical headers, so nothing is lost but the
# extra origin. The default is a .localhost name because Caddy certifies
# those from its internal CA without touching the network: an inert block
# rather than an impossible one.
files_domain() {
  case "$(domain_kind "$1")" in
    domain) printf 'files.%s' "$1" ;;
    *) printf 'files.localhost' ;;
  esac
}

# Who issues the certificate: Caddy's own CA for anything that is not a
# domain, because a public IP is neither a name Caddy issues for unasked
# nor one a public CA will be asked for; ACME for a domain, which is what
# Caddy does on its own.
cert_issuer() {
  case "$(domain_kind "$1")" in
    domain) printf 'acme' ;;
    *) printf 'internal' ;;
  esac
}

# The HTTP versions the proxy offers. HTTP/3 rides QUIC, and browsers refuse
# QUIC to a certificate they do not trust with no way to proceed — so on a
# locally-issued certificate, advertising it turns the second request into
# "could not reach the server" (measured: the sign-in fetch, on the first IP
# install, while curl said 200). A domain's certificate is trusted; it keeps
# the full set.
http_protocols() {
  case "$(cert_issuer "$1")" in
    acme) printf 'h1 h2 h3' ;;
    *) printf 'h1 h2' ;;
  esac
}

# The host alone — DOMAIN with any custom port stripped. An IPv6 literal is
# all colons and never carries a port (resolve_ports), so it passes whole.
domain_host() {
  case "$(domain_kind "$1")" in
    ipv6) printf '%s' "$1" ;;
    *) printf '%s' "${1%%:*}" ;;
  esac
}

# The address this machine reaches the world from — the prompt's default.
# `ip route get` asks the kernel which source address it would pick for that
# destination; no packet is sent and no external service is consulted. On a
# VPS this is the public address; behind NAT it is the LAN address, which is
# still the one the household's other machines can reach. Empty output (no
# route, no iproute2) falls back to localhost at the call site.
detected_ip() {
  command -v ip >/dev/null 2>&1 || return 0
  ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.*src \([0-9.]*\).*/\1/p' | head -n 1 || true
}

# Every global-scope IPv4 this machine holds, one per line — shown before
# the prompt so an operator on a multi-homed or NATed box can see what there
# is to choose from instead of guessing what the default was derived from.
machine_addresses() {
  command -v ip >/dev/null 2>&1 || return 0
  ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 || true
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
    # Deliberately NOT the detected address: a script's result must not
    # depend on which machine ran it. Automation states its domain or gets
    # the one value that means the same thing everywhere.
    DOMAIN="localhost"
    log "non-interactive mode: defaulting domain to localhost"
    return
  fi

  # A short numbered menu instead of one open question. The first VPS
  # install's feedback was blunt and right: an operator should see their
  # choices and their consequences, not guess what shape of answer the
  # blank after the colon wants.
  local suggested addrs choice
  suggested="$(detected_ip)"
  suggested="${suggested:-localhost}"
  addrs="$(machine_addresses | tr '\n' ' ' | sed 's/ $//')"
  printf '\n'
  log "─── Where should people reach this instance? ──────────────────"
  [ -z "$addrs" ] || log "    addresses on this machine: ${addrs}"
  log "    1) this server's IP (${suggested}) — works immediately;"
  log "       browsers show a one-time certificate warning"
  log "    2) a domain you own (e.g. chat.example.com) — automatic"
  log "       browser-trusted certificate; its DNS must point here"
  printf '[hamlaneh] choose 1 or 2 [1]: '
  read -r choice
  case "$choice" in
    "" | 1)
      DOMAIN="$suggested"
      ;;
    2)
      printf '[hamlaneh] your domain (just the name, no https://): '
      read -r DOMAIN
      ;;
    *)
      # Whatever they typed that is not a menu number is almost certainly
      # the address itself — accept it rather than scolding.
      DOMAIN="$choice"
      ;;
  esac
  validate_domain "$DOMAIN"
  log "serving on: ${DOMAIN}"
}

# The first admin account, asked for on a fresh install — the one thing the
# first real operator wanted the wizard to ask and it did not: their install
# bootstrapped an admin whose password they never saw, because the run that
# would have printed it failed later and the re-run had nothing new to
# print. A generated password is the default and is shown once at the end;
# a chosen one has to clear the server's own minimum (12), since a shorter
# one would be refused at the first forced change anyway. A re-run never
# asks: the bootstrap applies only while the user table is empty, so a new
# answer would be recorded and never used.
ADMIN_PASSWORD_MIN=12

valid_username() {
  case "$1" in "" | *[!A-Za-z0-9._-]*) return 1 ;; esac
  [ "${#1}" -le 64 ]
}

resolve_admin() {
  if [ -n "$(current_env_value HAMLANEH_ADMIN_USERNAME)" ] || [ "$NON_INTERACTIVE" -eq 1 ]; then
    return 0
  fi
  local answer
  printf '\n'
  log "─── First admin account ───────────────────────────────────────"
  log "    this signs in first and creates everyone else; its password"
  log "    must be changed at that first sign-in"
  while :; do
    printf '[hamlaneh] admin username [admin]: '
    read -r answer
    answer="${answer:-admin}"
    if valid_username "$answer"; then
      ADMIN_USERNAME="$answer"
      break
    fi
    log "usernames are letters, digits, dots, underscores and hyphens (up to 64)"
  done
  while :; do
    printf '[hamlaneh] admin password [Enter = generate one and show it at the end]: '
    read -rs answer
    printf '\n'
    if [ -z "$answer" ]; then
      break
    fi
    if [ "${#answer}" -ge "$ADMIN_PASSWORD_MIN" ]; then
      ADMIN_PASSWORD="$answer"
      break
    fi
    log "at least ${ADMIN_PASSWORD_MIN} characters — the server refuses shorter ones"
  done
}

# Blocks until both web ports are free, telling the operator what holds
# them; Enter re-checks. Used when moving ports is not an option (a domain
# needs 80/443 for its certificate) or not the operator's choice.
wait_for_free_web_ports() {
  local snap
  while :; do
    printf '[hamlaneh] press Enter to check the ports again (Ctrl-C aborts): '
    read -r _
    snap="$(listening_snapshot)"
    if ! snapshot_has_port "$snap" "$HTTP_PORT" && ! snapshot_has_port "$snap" "$HTTPS_PORT"; then
      log "ports ${HTTP_PORT} and ${HTTPS_PORT} are free now — continuing"
      return 0
    fi
    log "still in use: $(snapshot_has_port "$snap" "$HTTP_PORT" && printf '%s ' "$HTTP_PORT")$(snapshot_has_port "$snap" "$HTTPS_PORT" && printf '%s' "$HTTPS_PORT")"
  done
}

# Asks the operator to pick one free port, with a default and the holder of
# any refused answer named. First argument is the label, second the default.
ask_free_port() {
  local label="$1" default="$2" answer holder
  while :; do
    printf '[hamlaneh] %s [%s]: ' "$label" "$default" >&2
    read -r answer
    answer="${answer:-$default}"
    if ! valid_port "$answer"; then
      log "'${answer}' is not a port (1-65535)" >&2
      continue
    fi
    if snapshot_has_port "$(listening_snapshot)" "$answer"; then
      holder="$(port_holder "$answer")"
      log "port ${answer} is also in use${holder:+ (by ${holder})} — pick another" >&2
      continue
    fi
    printf '%s' "$answer"
    return 0
  done
}

# The port conversation, held only when there is something to talk about.
# Custom web ports exist for exactly one deployment shape: a server where
# 80/443 already belong to something else (measured on the first real VPS
# install: 443 held, and "stop your other service" was no answer). They are
# offered for IP and localhost installs only — a domain's browser-trusted
# certificate is issued over 80/443 or not at all, and moving a domain's
# ports would start a certificate order that can never complete.
resolve_ports() {
  # A domain carrying a port is refused whichever way it arrived (--domain
  # included): the trusted certificate it exists for cannot be issued there.
  case "$DOMAIN" in
    *:*)
      [ "$(domain_kind "$DOMAIN")" != "domain" ] ||
        fail "a domain cannot use a custom port: ${DOMAIN%%:*}'s browser-trusted certificate is only issuable over 80/443. Free those ports, or serve on the IP instead."
      ;;
  esac
  # A re-run inherits its own earlier choice; the running stack holding its
  # own ports is what success looks like, not a conflict.
  local env_port
  env_port="$(current_env_value HAMLANEH_HTTPS_PORT)"
  [ -z "$env_port" ] || HTTPS_PORT="$env_port"
  env_port="$(current_env_value HAMLANEH_HTTP_PORT)"
  [ -z "$env_port" ] || HTTP_PORT="$env_port"
  if stack_exists; then
    return 0
  fi

  local snap taken=() port line="" holder
  snap="$(listening_snapshot)"
  [ -n "$snap" ] || return 0
  for port in "$HTTP_PORT" "$HTTPS_PORT"; do
    if snapshot_has_port "$snap" "$port"; then
      taken+=("$port")
      holder="$(port_holder "$port")"
      line="${line}${port}${holder:+ (${holder})}  "
    fi
  done
  [ "${#taken[@]}" -gt 0 ] || return 0

  if [ "$NON_INTERACTIVE" -eq 1 ]; then
    fail "web ports already in use: ${line}— free them, or set HAMLANEH_HTTP_PORT/HAMLANEH_HTTPS_PORT in the environment, and re-run. Nothing was changed."
  fi

  printf '\n'
  log "─── Web ports ─────────────────────────────────────────────────"
  log "    already in use on this machine: ${line}"
  if [ "$(domain_kind "$DOMAIN")" = "domain" ]; then
    log "    ${DOMAIN}'s browser-trusted certificate can only be issued over"
    log "    ports 80/443, so this install needs them. Stop the service that"
    log "    holds them (often: systemctl disable --now nginx) and re-check."
    wait_for_free_web_ports
    return 0
  fi

  local choice
  log "    1) I'll stop that service — Hamlaneh uses the standard 80/443"
  log "    2) run Hamlaneh on other ports — the address becomes"
  log "       https://${DOMAIN}:<port>, certificate warning as before"
  printf '[hamlaneh] choose 1 or 2 [2]: '
  read -r choice
  if [ "$choice" = "1" ]; then
    wait_for_free_web_ports
    return 0
  fi

  HTTPS_PORT="$(ask_free_port "HTTPS port for Hamlaneh" 8443)"
  HTTP_PORT="$(ask_free_port "HTTP port (redirects to HTTPS)" 8080)"
  if [ "$HTTP_PORT" = "$HTTPS_PORT" ]; then
    fail "the HTTP and HTTPS ports must differ (both were ${HTTPS_PORT}). Re-run and pick two."
  fi
  # The one value three consumers read: Caddy listens where the address
  # says, the server allows the Origin browsers will actually send, and
  # every printed link is copy-pasteable — because the port travels inside
  # HAMLANEH_DOMAIN instead of beside it.
  DOMAIN="${DOMAIN%%:*}:${HTTPS_PORT}"
  log "the instance will live at https://${DOMAIN}"
}

# Where the server's second listener binds inside the compose network, and the
# port deploy/Caddyfile's admin site proxies to. One number in two files: change
# it here and there, or neither.
ADMIN_LISTEN_PORT=9090
ADMIN_PORT_DEFAULT=9443
# server.go's adminSurface, split in two by what the app port should DO with
# each half. Both mirror that function and nothing wider — the SHARED paths
# (/api/v1/auth, /api/v1/instance, /api/v1/users/me, /assets, /brand) are
# carried by both listeners and must keep answering on the web port.
#
# The powered half is refused outright: an operator who shut the admin port in
# their firewall has to be right that it is gone from here.
#
# The page half is redirected to the admin origin instead, because /admin is a
# client-side route that renders nothing until the API answers — 404ing it
# would protect nothing, and sending it on is what lets the sidebar's Admin
# control stay a plain "/admin" instead of the client being handed the admin
# port in a document anonymous visitors read.
#
# Expressions rather than prefix lists because each travels through one
# environment variable into one Caddy matcher.
ADMIN_MOVED_PATH_RE='^/(api/v1/admin/|scim/v2/)'
ADMIN_PAGE_PATH_RE='^/admin(/|$)'

# The admin-port conversation (ADR 015).
#
# The dashboard and its API move to a port of their own on the same host, so a
# cloud firewall — which filters by port and never by path — can reach them
# separately from the chat app. The question is not whether to move them but
# whether the moved port faces the internet or this machine only, because the
# answer decides whether an operator who binds it to loopback has a way back
# in. That is why this asks rather than assumes, and prints the tunnel command
# with the loopback answer.
#
# NOT asked in --non-interactive mode: the split stays off there, every admin
# route stays on the web port, and a script that has been running against
# /api/v1/admin or /scim/v2 keeps working untouched.
#
# The port is a DEPLOYMENT boundary and never an authorization decision.
# Nothing below reads it as a permission; the same session and the same role
# check refuse everybody else on either port.
resolve_admin_port() {
  # A re-run inherits its own earlier answer, so neither the question comes
  # back nor the choice is lost. Reading it here also keeps ask_free_port away
  # from a port our own Caddy is already holding.
  local env_port
  env_port="$(current_env_value HAMLANEH_ADMIN_PORT)"
  if [ -n "$env_port" ]; then
    ADMIN_PORT="$env_port"
    ADMIN_BIND="$(current_env_value HAMLANEH_ADMIN_BIND)"
    return 0
  fi
  [ "$NON_INTERACTIVE" -eq 0 ] || return 0
  # An IPv6 literal has to be bracketed to carry a port, and the proxy's site
  # address is written from the unbracketed host. Rather than write a second,
  # differently-quoted copy of the same host, this install keeps the admin
  # surface where it is and says so once.
  if [ "$(domain_kind "$DOMAIN")" = "ipv6" ]; then
    log "IPv6 install: the admin dashboard stays on the web port (a bracketed host:port site address is not written here)."
    return 0
  fi

  # An .env that exists but carries none of these variables is an install that
  # PREDATES this feature, and it may have a working SCIM integration pointed
  # at the web port. Moving that because somebody pressed Enter through a
  # question they had never seen would be exactly the silent breakage the
  # idempotency promise exists to prevent — so those get a third answer, and
  # it is the default. A machine with no instance on it yet has nothing to
  # break, and keeps the closed two-answer default.
  local choice host existing=0
  [ ! -f "$ENV_FILE" ] || existing=1
  host="$(domain_host "$DOMAIN")"
  printf '\n'
  log "─── Admin dashboard ──────────────────────────────────────────"
  log "    the dashboard and its API move to their own port, so your"
  log "    firewall can reach them separately from the chat app"
  log "    1) reachable from the internet — open this port in your"
  log "       cloud firewall to use it"
  log "    2) this machine only — reach it with:  ssh -L ${ADMIN_PORT_DEFAULT}:localhost:${ADMIN_PORT_DEFAULT}"
  if [ "$existing" -eq 1 ]; then
    log "    3) leave it where it is — the dashboard and its API stay on"
    log "       the web port"
    printf '[hamlaneh] choose 1, 2 or 3 [3]: '
  else
    printf '[hamlaneh] choose 1 or 2 [2]: '
  fi
  read -r choice
  if [ "$existing" -eq 1 ] && [ "$choice" != "1" ] && [ "$choice" != "2" ]; then
    log "leaving the admin dashboard on the web port — nothing moves. Re-run and choose 1 or 2 to move it."
    return 0
  fi
  ADMIN_PORT="$(ask_free_port "admin dashboard port" "$ADMIN_PORT_DEFAULT")"
  # Anything that is not "1" is read as the closed answer: the one that can
  # cost an operator their way in is the open one, so it has to be typed.
  if [ "$choice" = "1" ]; then
    ADMIN_BIND=""
    log "the admin dashboard will live at https://${host}:${ADMIN_PORT}/admin"
    log "open ${ADMIN_PORT}/tcp in your cloud firewall, or it will not answer"
  else
    ADMIN_BIND="127.0.0.1:"
    log "the admin dashboard will listen on this machine only. Reach it with:"
    log "  ssh -L ${ADMIN_PORT}:localhost:${ADMIN_PORT} <you>@${host}"
    log "then open https://localhost:${ADMIN_PORT}/admin"
    log "From the public chat app the Admin control will not reach this port — that is what 'this machine only' means. Opened through the tunnel, the control works normally."
  fi
}

write_env() {
  if [ -f "$ENV_FILE" ]; then
    chmod 600 "$ENV_FILE"
    # "Up to date" means every line DERIVED from the domain is present and
    # right, not merely that the domain itself matches. Measured on a live
    # install: a re-run after the SNI and issuer variables were introduced
    # saw an unchanged domain, declared the file current, and never wrote
    # them — so Caddy kept asking Let's Encrypt for an IP certificate it
    # will not issue, and the fix that was on disk never reached the proxy.
    if [ "$(current_env_domain)" = "$DOMAIN" ] &&
      [ "$(current_env_value HAMLANEH_FILES_DOMAIN)" = "$(files_domain "$DOMAIN")" ] &&
      [ "$(current_env_value HAMLANEH_DEFAULT_SNI)" = "$(domain_host "$DOMAIN")" ] &&
      [ "$(current_env_value HAMLANEH_CERT_ISSUER)" = "$(cert_issuer "$DOMAIN")" ] &&
      [ "$(current_env_value HAMLANEH_HTTP_PROTOCOLS)" = "$(http_protocols "$DOMAIN")" ] &&
      [ "$(current_env_value HAMLANEH_ADMIN_PORT)" = "$ADMIN_PORT" ] &&
      [ "$(current_env_value HAMLANEH_ADMIN_BIND)" = "$ADMIN_BIND" ] &&
      [ "$(current_env_value HAMLANEH_ADMIN_MOVED_PATHS)" = "${ADMIN_PORT:+$ADMIN_MOVED_PATH_RE}" ] &&
      [ "$(current_env_value HAMLANEH_ADMIN_PAGE_PATHS)" = "${ADMIN_PORT:+$ADMIN_PAGE_PATH_RE}" ] &&
      [ "$(current_env_value HAMLANEH_HTTPS_PORT)" = "$([ "$HTTPS_PORT" != "443" ] || [ "$HTTP_PORT" != "80" ] && printf '%s' "$HTTPS_PORT")" ]; then
      log "deploy/.env already up to date — leaving it untouched"
      return
    fi

    # Only the lines derived from the domain change; every generated
    # secret is always kept.
    log "updating the domain-derived lines in deploy/.env (existing secrets kept)"
    local tmp
    tmp="$(mktemp "${ENV_FILE}.XXXXXX")"
    chmod 600 "$tmp"
    # validate_domain guarantees $DOMAIN contains no sed metacharacters.
    local files
    files="$(files_domain "$DOMAIN")"
    sed -e "s/^HAMLANEH_DOMAIN=.*/HAMLANEH_DOMAIN=${DOMAIN}/" \
        -e "s/^HAMLANEH_FILES_DOMAIN=.*/HAMLANEH_FILES_DOMAIN=${files}/" \
        -e '/^HAMLANEH_HTTP_PORT=/d' \
        -e '/^HAMLANEH_HTTPS_PORT=/d' \
        -e '/^HAMLANEH_DEFAULT_SNI=/d' \
        -e '/^HAMLANEH_CERT_ISSUER=/d' \
        -e '/^HAMLANEH_HTTP_PROTOCOLS=/d' \
        -e '/^HAMLANEH_ADMIN_ADDR=/d' \
        -e '/^HAMLANEH_ADMIN_PORT=/d' \
        -e '/^HAMLANEH_ADMIN_BIND=/d' \
        -e '/^HAMLANEH_ADMIN_MOVED_PATHS=/d' \
        -e '/^HAMLANEH_ADMIN_PAGE_PATHS=/d' \
        "$ENV_FILE" > "$tmp"
    {
      printf 'HAMLANEH_DEFAULT_SNI=%s\n' "$(domain_host "$DOMAIN")"
      printf 'HAMLANEH_CERT_ISSUER=%s\n' "$(cert_issuer "$DOMAIN")"
      printf 'HAMLANEH_HTTP_PROTOCOLS=%s\n' "$(http_protocols "$DOMAIN")"
    } >> "$tmp"
    # Rewritten rather than edited in place: default ports carry no line at
    # all, so moving back to 80/443 removes the override instead of pinning
    # a now-wrong number.
    if [ "$HTTPS_PORT" != "443" ] || [ "$HTTP_PORT" != "80" ]; then
      printf 'HAMLANEH_HTTP_PORT=%s\n' "$HTTP_PORT" >> "$tmp"
      printf 'HAMLANEH_HTTPS_PORT=%s\n' "$HTTPS_PORT" >> "$tmp"
    fi
    # Rewritten as a group for the same reason: the split being OFF is the
    # ABSENCE of these lines, so turning it back off has to remove them rather
    # than leave a port nothing listens on. HAMLANEH_ADMIN_BIND is written even
    # when empty — empty means "every interface", and deleting the line would
    # mean "loopback" (see the compose ports entry).
    if [ -n "$ADMIN_PORT" ]; then
      {
        printf 'HAMLANEH_ADMIN_ADDR=:%s\n' "$ADMIN_LISTEN_PORT"
        printf 'HAMLANEH_ADMIN_PORT=%s\n' "$ADMIN_PORT"
        printf 'HAMLANEH_ADMIN_BIND=%s\n' "$ADMIN_BIND"
        printf 'HAMLANEH_ADMIN_MOVED_PATHS=%s\n' "$ADMIN_MOVED_PATH_RE"
        printf 'HAMLANEH_ADMIN_PAGE_PATHS=%s\n' "$ADMIN_PAGE_PATH_RE"
      } >> "$tmp"
    fi
    # Derived from the domain, so an .env written before this variable existed
    # gains it here rather than keeping a stale files hostname.
    if ! grep -q '^HAMLANEH_FILES_DOMAIN=' "$tmp"; then
      printf 'HAMLANEH_FILES_DOMAIN=%s\n' "$files" >> "$tmp"
    fi
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
  # resolve_admin may have taken these from the operator; a generated
  # password is the default, and is printed once at the end.
  [ -n "$ADMIN_PASSWORD" ] || ADMIN_PASSWORD="$(openssl rand -base64 18)"
  ADMIN_PASSWORD_NEW=1
  old_umask="$(umask)"
  umask 077
  {
    printf '# Generated by install.sh on %s — DO NOT COMMIT.\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'HAMLANEH_DOMAIN=%s\n' "$DOMAIN"
    printf 'HAMLANEH_FILES_DOMAIN=%s\n' "$(files_domain "$DOMAIN")"
    printf 'HAMLANEH_DEFAULT_SNI=%s\n' "$(domain_host "$DOMAIN")"
    printf 'HAMLANEH_CERT_ISSUER=%s\n' "$(cert_issuer "$DOMAIN")"
    printf 'HAMLANEH_HTTP_PROTOCOLS=%s\n' "$(http_protocols "$DOMAIN")"
    if [ "$HTTPS_PORT" != "443" ] || [ "$HTTP_PORT" != "80" ]; then
      printf 'HAMLANEH_HTTP_PORT=%s\n' "$HTTP_PORT"
      printf 'HAMLANEH_HTTPS_PORT=%s\n' "$HTTPS_PORT"
    fi
    if [ -n "$ADMIN_PORT" ]; then
      printf 'HAMLANEH_ADMIN_ADDR=:%s\n' "$ADMIN_LISTEN_PORT"
      printf 'HAMLANEH_ADMIN_PORT=%s\n' "$ADMIN_PORT"
      printf 'HAMLANEH_ADMIN_BIND=%s\n' "$ADMIN_BIND"
      printf 'HAMLANEH_ADMIN_MOVED_PATHS=%s\n' "$ADMIN_MOVED_PATH_RE"
      printf 'HAMLANEH_ADMIN_PAGE_PATHS=%s\n' "$ADMIN_PAGE_PATH_RE"
    fi
    printf 'POSTGRES_PASSWORD=%s\n' "$password"
    printf 'HAMLANEH_FILE_URL_KEY=%s\n' "$file_url_key"
    printf 'HAMLANEH_AUDIT_KEY=%s\n' "$audit_key"
    printf 'HAMLANEH_LIVEKIT_API_KEY=%s\n' "$(new_livekit_api_key)"
    printf 'HAMLANEH_LIVEKIT_API_SECRET=%s\n' "$(new_livekit_api_secret)"
    printf 'HAMLANEH_ADMIN_USERNAME=%s\n' "$ADMIN_USERNAME"
    printf 'HAMLANEH_ADMIN_PASSWORD=%s\n' "$ADMIN_PASSWORD"
    printf 'HAMLANEH_ADMIN_LOCALE=en\n'
    print_mail_env
  } > "$ENV_FILE"
  umask "$old_umask"
  chmod 600 "$ENV_FILE"
}

# The value .env.example ships for every secret it cannot ship a real one
# for. Six keys carry it, and exactly two of them are short enough that the
# server refuses to boot: HAMLANEH_FILE_URL_KEY and HAMLANEH_AUDIT_KEY, whose
# 32-byte floor internal/filesign and internal/audit each enforce against this
# 19-byte string. That is the only reason a wholesale `cp .env.example .env`
# has never produced a running instance with secrets printed in the repository.
#
# The other four — POSTGRES_PASSWORD, the LiveKit pair and the admin password
# — are values the stack accepts. An .env carrying one of those next to real
# keys belongs to an instance that booted, and everything below that decides
# whether something is safe to replace or delete turns on that distinction.
PLACEHOLDER_VALUE="REPLACED_AT_INSTALL"

# README's quick start says `cp .env.example .env`. An operator who did that
# and then reached for the installer used to keep every placeholder: write_env
# refuses to touch an existing .env, and each ensure_* function below tests
# for the KEY, never for the VALUE.
#
# So: strip any required secret still holding the placeholder, and let the
# ensure_* functions regenerate it exactly as they do for an .env that
# predates the key.
#
# Replacing them is right in every case, but it is not free in every case, and
# the difference is worth stating rather than assuming away. Only the file-URL
# and audit placeholders prove the server never booted. The LiveKit secret and
# the admin trio are values the stack accepts, so an .env carrying them beside
# real keys can belong to a LIVE instance — one whose join tokens are signed
# with a constant anybody can read in this repository, which is a hole and not
# a secret. Replacing it closes the hole; what it costs is that a call in
# progress ends and unused join tokens stop working. install_may_have_data is
# what decides whether to say so, and nothing here is skipped on its answer:
# leaving a published credential in place is never the safer option.
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
  if install_may_have_data; then
    warn "this instance has already run, so those placeholders were live, not dormant. Replacing them is still right — a value published in this repository is not a secret — but any call in progress ends and every unused join token stops working. Nothing stored is affected: no message, file or account is touched."
  fi
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

# The two placeholders that PROVE the server never started, as against the four
# that prove nothing. internal/filesign and internal/audit each refuse a key
# shorter than 32 bytes and the placeholder is 19, so either one of these in
# place stops the process before it serves a request — no schema migrated, no
# account created, no byte written. POSTGRES_PASSWORD, the LiveKit pair and the
# admin password are all values the stack accepts, which is why not one of them
# can stand in for this check.
boot_blocked_by_placeholder() {
  [ "$(env_value HAMLANEH_FILE_URL_KEY)" = "$PLACEHOLDER_VALUE" ] ||
    [ "$(env_value HAMLANEH_AUDIT_KEY)" = "$PLACEHOLDER_VALUE" ]
}

# Whether this install may already hold real data.
#
# "May" is the whole point. Every caller uses this to decide whether to do
# something that is irreversible if the answer is wrong, so it answers yes for
# anything it cannot rule out, and it rules something out only from a fact —
# a database volume that exists, and an .env that could have booted a server
# to write to it — never from one variable's value standing in for the rest.
#
# It has to be asked BEFORE scrub_placeholder_secrets rewrites deploy/.env,
# which is why main() calls ensure_postgres_password_env first; after the scrub
# the placeholders it reads are gone and it would answer yes to everything.
install_may_have_data() {
  db_volume_exists && ! boot_blocked_by_placeholder
}

# POSTGRES_PASSWORD is the one secret that cannot simply be regenerated: it
# is written into the database at initdb and never read from the environment
# again. On a fresh host there is no database yet and generating it is right;
# on a host that already initialised one with the placeholder, changing it
# here would lock the server out of its own database with an authentication
# error nobody would connect back to this script.
#
# So that case stops. What it says depends on whether the install can have
# data, and that is asked rather than inferred — an earlier version of this
# function read POSTGRES_PASSWORD alone, concluded from it that the server had
# never booted, and told an operator whose OTHER secrets were real, whose
# instance was serving, and whose database was full to run `down -v`.
ensure_postgres_password_env() {
  [ -f "$ENV_FILE" ] || return 0
  local value
  value="$(env_value POSTGRES_PASSWORD)"
  if [ -n "$value" ] && [ "$value" != "$PLACEHOLDER_VALUE" ]; then
    return 0
  fi

  if install_may_have_data; then
    fail "deploy/.env has POSTGRES_PASSWORD=${value:-<empty>}, and this install has both a database volume (${COMPOSE_PROJECT}_db_data) and secrets a server can boot on — so it may be holding real messages, files and accounts, and that value is the password its database was created with.
       Do NOT clear the volume to get past this. That deletes the database, and this message deliberately does not give you the command for it.
       Rotate the password instead, which loses nothing. Pick a new one, then:
         docker compose -f ${COMPOSE_FILE} up -d db
         docker compose -f ${COMPOSE_FILE} exec db psql -U hamlaneh -d hamlaneh -c \"ALTER USER hamlaneh PASSWORD 'the-new-password'\"
       put that same value in POSTGRES_PASSWORD in deploy/.env, and re-run this script.
       Do it rather than leave it: ${PLACEHOLDER_VALUE} is printed in this repository, so until you do, this database's password is public."
  fi

  if db_volume_exists; then
    fail "deploy/.env has POSTGRES_PASSWORD=${value:-<empty>} but a database volume (${COMPOSE_PROJECT}_db_data) already exists, so the database is using that value as its real password and this script must not change it silently.
       That state comes from copying .env.example and running docker compose by hand. This .env still carries the placeholder file-URL or audit key, which the server refuses to start on, so no server of ours ever wrote to that volume.
       Clear it and re-run:  docker compose -f ${COMPOSE_FILE} down -v && sudo ${0}
       If the volume predates this .env and you believe it DOES hold data, do not run that — set POSTGRES_PASSWORD in deploy/.env to the password the database was created with instead."
  fi

  log "generating POSTGRES_PASSWORD in deploy/.env"
  local tmp
  tmp="$(mktemp "${ENV_FILE}.XXXXXX")"
  chmod 600 "$tmp"
  grep -v '^POSTGRES_PASSWORD=' "$ENV_FILE" > "$tmp" || true
  printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -base64 32)" >> "$tmp"
  mv "$tmp" "$ENV_FILE"
}

# The optional MAIL settings .env.example documents, written into the
# generated .env so an operator turning email on edits a key that is already
# there instead of hunting for its name. .env.example points the reader at
# this file as the generated one; leaving them out made that a dead end.
#
# Mail only. .env.example's other optional block, the four HAMLANEH_OIDC_*
# keys, is not written here and no name in it appears anywhere in this script:
# an install that wants single sign-on copies those four lines out of
# .env.example by hand. Empty is empty either way — the server reads an absent
# key and an empty one identically — so what is missing is the reminder, not
# the capability.
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
  # Checked before the socket scan and before the stack-exists shortcut,
  # because this one is a collision inside our OWN compose file rather than
  # with anything on the host. The admin dashboard's port is published whether
  # or not the split is on — a compose ports entry cannot be conditionally
  # omitted — so the number compose will use, chosen or default, has to differ
  # from the web ports. Docker reports the clash as "port is already
  # allocated", which names neither file nor variable.
  local admin_published="${ADMIN_PORT:-$ADMIN_PORT_DEFAULT}"
  if [ "$admin_published" = "$HTTP_PORT" ] || [ "$admin_published" = "$HTTPS_PORT" ]; then
    fail "port ${admin_published} is a web port here AND where deploy/docker-compose.yml publishes the admin dashboard, so the stack could not start. Re-run and choose different web ports, or set HAMLANEH_ADMIN_PORT in deploy/.env to a free one. Nothing was changed."
  fi
  if stack_exists; then
    return 0
  fi
  local listening taken=() port needed
  listening="$(listening_snapshot)"
  [ -n "$listening" ] || return 0
  # The web ports are whatever resolve_ports settled on; the media trio is
  # fixed (ADR 005 — published TURN/ICE ports are part of the calls design,
  # not an install-time preference). The admin port joins the list only when
  # the split is on, because with it off nothing publishes one.
  needed=("$HTTP_PORT" "$HTTPS_PORT" 3478 7881 7882)
  [ -z "$ADMIN_PORT" ] || needed+=("$ADMIN_PORT")
  for port in "${needed[@]}"; do
    if snapshot_has_port "$listening" "$port"; then
      taken+=("$port")
    fi
  done
  if [ "${#taken[@]}" -gt 0 ]; then
    fail "these host ports are already in use by something else: ${taken[*]}. Hamlaneh needs ${needed[*]}. Stop whatever holds them (often nginx or apache2: 'systemctl disable --now nginx') and re-run. Nothing was changed."
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

# `up -d --build` rebuilds an image whose inputs changed, but a container
# already running the OLD image is not reliably replaced with it — measured
# on the first upgrade of a live install: the server image was rebuilt and
# took the tag, docker showed the container's image as a bare sha256, and
# the container kept running the fourteen-hour-old binary while reporting
# healthy. An update that builds but does not deploy is worse than one that
# fails, because it reports success. So, per built service, compare the
# image the container runs with the image the tag now names and recreate
# only the ones that differ — a no-change re-run bounces nothing.
recreate_stale_containers() {
  local svc cid running current
  for svc in server caddy; do
    cid="$(compose ps -q "$svc" 2>/dev/null | head -n 1)"
    [ -n "$cid" ] || continue
    running="$(docker inspect -f '{{.Image}}' "$cid" 2>/dev/null || true)"
    current="$(docker image inspect -f '{{.Id}}' "${COMPOSE_PROJECT}-${svc}" 2>/dev/null || true)"
    if [ -z "$running" ] || [ -z "$current" ]; then
      continue
    fi
    if [ "$running" != "$current" ]; then
      log "${svc}: the running container is on an older image than the one just built — recreating it"
      compose up -d --no-deps --force-recreate "$svc" ||
        fail "recreating the ${svc} container failed. Its output is above; re-running this script is safe."
    fi
  done
}

# Every rebuild leaves the previous image untagged on disk and nothing ever
# collected them, so an install that updates weekly grows by a toolchain's
# worth of layers a week. Untagged images only: nothing the stack runs is
# untagged (recreate_stale_containers just made sure of that), so nothing
# in use can be pulled out from under it.
prune_dangling_images() {
  local out reclaimed
  out="$(docker image prune -f 2>/dev/null || true)"
  reclaimed="$(printf '%s\n' "$out" | grep -o 'Total reclaimed space: .*' || true)"
  log "old build images removed (${reclaimed:-nothing to remove})"
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
  local deadline=$((SECONDS + 300)) code="" host="$DOMAIN"
  # DOMAIN may carry the custom HTTPS port; the probe needs host and port
  # apart. IPv6 literals are all colons and carry no port (resolve_ports).
  case "$(domain_kind "$DOMAIN")" in ipv6) ;; *) host="${DOMAIN%%:*}" ;; esac
  log "waiting for the stack to answer /healthz (up to 5 minutes) ..."
  while [ "$SECONDS" -lt "$deadline" ]; do
    code="$(curl -sk --max-time 5 -o /dev/null -w '%{http_code}' \
      --connect-to "${host}:${HTTPS_PORT}:127.0.0.1:${HTTPS_PORT}" "https://${host}:${HTTPS_PORT}/healthz" 2>/dev/null || true)"
    if [ "$code" = "200" ]; then
      log "the stack is serving (/healthz returned 200)"
      return 0
    fi
    sleep 5
  done

  # Before dumping logs, separate the two very different causes. Caddy
  # answering on port 80 while HTTPS does not serve means the containers are
  # healthy and the CERTIFICATE is what is missing — for a real domain that
  # is nearly always ACME being unable to complete, and no amount of reading
  # the server's logs will show it, because the server is fine.
  if [ "$(domain_kind "$DOMAIN")" = "domain" ] &&
    [ "$(curl -s --max-time 5 -o /dev/null -w '%{http_code}' \
      --resolve "${host}:${HTTP_PORT}:127.0.0.1" "http://${host}:${HTTP_PORT}/healthz" 2>/dev/null || true)" != "000" ]; then
    print_acme_pending_notice
    print_port_notice
    fail "the containers are running and answering on port 80, but HTTPS is not serving yet — see the two notices above. Nothing was deleted and nothing here needs re-running."
  fi

  printf '\n[hamlaneh] --- container status ---\n' >&2
  compose ps >&2 || true
  printf '\n[hamlaneh] --- last 40 lines from the server ---\n' >&2
  compose logs --tail 40 server >&2 || true
  fail "the stack started but never answered https://${DOMAIN}/healthz (last status: ${code:-no response}). The output above usually names the cause on its first line. Nothing was deleted; fix the cause and re-run. Full logs: docker compose -f ${COMPOSE_FILE} logs"
}

# The single most likely way a domain install still goes wrong: the DNS
# record is not there yet, or the cloud security group is still shut, so
# Caddy cannot complete an ACME challenge and has no certificate to serve.
# Neither is visible in any container's logs, and neither is fixed by
# re-running anything — which is the part an operator most needs told.
print_acme_pending_notice() {
  cat <<EOF

  ┌──────────────────────────────────────────────────────────────────────┐
  │  NO CERTIFICATE YET — THIS IS ALMOST CERTAINLY DNS OR A FIREWALL     │
  │                                                                      │
  │  The containers are up: Caddy answered on port 80. What it does not  │
  │  have is a certificate, so nothing is served over HTTPS. Caddy gets  │
  │  one automatically, but only once BOTH of these are true:            │
  │                                                                      │
  │    1. ${DOMAIN}
  │       resolves to THIS server's public IP address. Check from        │
  │       somewhere else, not from this machine.                         │
  │    2. inbound 80/tcp and 443/tcp reach this host from the internet.  │
  │       On AWS, Azure, GCP, Hetzner or DigitalOcean that is a cloud    │
  │       security group, not the host firewall — see the next box.      │
  │                                                                      │
  │  Do NOT re-run this script to fix it. Caddy retries on its own, and  │
  │  the site starts working within about a minute of both being true.   │
  │  Watch it happen:                                                    │
  │    docker compose -f ${COMPOSE_FILE}
  │      logs -f caddy                                                   │
  └──────────────────────────────────────────────────────────────────────┘

EOF
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
  print_background_jobs
  print_host_notes
  print_port_notice
}

# What is actually switched on, reported from the state each step reached
# rather than from what this script set out to do. A feature that failed to
# arm and says nothing is worse than one that was never offered: nobody
# discovers an update timer is dead until they needed the update.
print_background_jobs() {
  log ""
  log "Running in the background:"
  log "  Release signatures: cosign ${COSIGN_STATE}"
  log "  Security updates:   ${UPDATE_TIMER_STATE}"
  log "  Encrypted backups:  ${BACKUP_TIMER_STATE}"
  case "${COSIGN_STATE}${UPDATE_TIMER_STATE}${BACKUP_TIMER_STATE}" in
    *"not installed"*|*"not enabled"*)
      log "  ^ Something above is off. The warnings earlier in this run say why and what to run."
      ;;
  esac
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
      cat <<'EOF'

  ┌──────────────────────────────────────────────────────────────────────┐
  │  BARE-IP MODE — READ THIS, IT IS A REAL TRADE-OFF                    │
  │                                                                      │
  │  You gave an IP address rather than a domain, so this instance has   │
  │  no publicly trusted certificate. Caddy issues one from its own      │
  │  internal CA instead, and every browser then shows a full-page       │
  │  warning that every user has to click through. A user trained to     │
  │  click through that warning cannot tell it apart from a real         │
  │  interception. That is the actual cost.                              │
  │                                                                      │
  │  HSTS is still sent, but browsers ignore it over an untrusted        │
  │  certificate, so it protects nothing here either.                    │
  │                                                                      │
  │  Let's Encrypt does issue certificates for IP addresses now, under   │
  │  its short-lived profile. That does not reach you: Caddy 2.10, the   │
  │  version this stack pins, treats every IP address as an internal     │
  │  name and goes straight to its own CA without asking any public CA   │
  │  for anything. No setting here changes that.                         │
  │                                                                      │
  │  What this does NOT cost you: nothing was weakened to make bare-IP   │
  │  work. Same security headers, same non-root read-only containers,    │
  │  same closed database, traffic still encrypted in transit. Uploads   │
  │  are served from the app origin with the identical headers, so       │
  │  there is no second hostname here and no failing certificate order   │
  │  for one.                                                            │
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
  # The admin port's row, or nothing when the split is off. The loopback
  # answer must NOT be told to open it in a cloud firewall — that is the one
  # sentence in this box that would undo the choice the operator just made.
  # Built here rather than inline because a heredoc cannot skip a line, and
  # ASCII-only because printf pads by bytes: an em dash would shorten the row
  # by two columns and bend the box.
  local admin="" row=""
  if [ -n "$ADMIN_PORT" ] && [ -n "$ADMIN_BIND" ]; then
    row="$(printf '%6s' "$ADMIN_PORT")/tcp    admin dashboard: this machine only, do NOT open it"
  elif [ -n "$ADMIN_PORT" ]; then
    row="$(printf '%6s' "$ADMIN_PORT")/tcp    admin dashboard: open this one too"
  fi
  [ -z "$row" ] || admin="$(printf '\n  │  %-68s│' "$row")"
  cat <<EOF

  ┌──────────────────────────────────────────────────────────────────────┐
  │  OPEN THESE PORTS IN YOUR CLOUD PROVIDER'S FIREWALL                  │
  │                                                                      │
  │  This installer already opened them on the host. It CANNOT open a    │
  │  cloud security group (AWS, Azure NSG, GCP, Hetzner, DigitalOcean),  │
  │  and calls fail silently for as long as those stay shut.             │
  │                                                                      │
  │  $(printf '%6s' "$HTTP_PORT")/tcp    HTTP — redirects to HTTPS, and ACME certificates      │
  │  $(printf '%6s' "$HTTPS_PORT")/tcp    HTTPS — the application                               │
  │  $(printf '%6s' "$HTTPS_PORT")/udp    HTTP/3                                                │${admin}
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
  require_checkout "$@"
  ensure_prereqs
  ensure_docker
  ensure_cosign
  resolve_domain
  resolve_ports
  resolve_admin_port
  resolve_admin
  write_env
  # Order is load-bearing: ensure_postgres_password_env asks
  # install_may_have_data, which reads placeholders that the scrub then
  # removes. Swap these two and it can only ever answer "may have data".
  ensure_postgres_password_env
  scrub_placeholder_secrets
  ensure_admin_env
  ensure_mail_env
  ensure_file_url_key_env
  ensure_audit_key_env
  ensure_livekit_env
  preflight
  start_stack
  recreate_stale_containers
  wait_for_stack
  enable_update_timer
  enable_backup_timer
  prune_dangling_images
  print_success
}

# Sourced by install.test.sh, executed by everyone else.
if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
