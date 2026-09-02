#!/usr/bin/env bash
#
# Tests for deploy/install.sh — ROADMAP.md Phase 4, "install.sh hardened:
# Ubuntu LTS, Debian, Fedora, RHEL-clone; idempotent re-runs; clear errors".
#
# install.sh guards its own `main` with a BASH_SOURCE check, so this file
# sources it and drives the individual functions. Nothing here runs as root
# and nothing here installs a package.
#
# What is real and what is not, stated here rather than left to be assumed:
#
#   REAL  every branch of install.sh's own logic: argument parsing, domain
#         validation and classification, distribution detection and the
#         Docker route it picks, .env generation, the placeholder scrub, and
#         each error path's message and exit code.
#   REAL  in --matrix mode, the distribution detection runs inside actual
#         ubuntu / debian / fedora / rocky / almalinux images, against those
#         images' own /etc/os-release. Nothing is faked at that layer.
#   NOT   the parts that touch the machine: installing Docker, starting a
#         systemd unit, building images, opening ports. A container shares
#         the host kernel and has no systemd and no Docker of its own, so
#         those need real VMs — see docs/ROADMAP.md's install matrix gate.
#         Do not read a green run here as "the installer was proven on four
#         distributions"; read it as "the branch it takes on each of the four
#         is the intended one".
#
# Usage:
#   bash deploy/install.test.sh            # full suite (needs openssl)
#   bash deploy/install.test.sh --os-only  # detection only, against this host
#   bash deploy/install.test.sh --matrix   # --os-only inside five distro images

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SH="${SCRIPT_DIR}/install.sh"

PASS_COUNT=0
FAIL_COUNT=0

ok() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf 'PASS: %s\n' "$1"
}

bad() {
  FAIL_COUNT=$((FAIL_COUNT + 1))
  printf 'FAIL: %s\n' "$1" >&2
}

check_eq() {
  local label="$1" want="$2" got="$3"
  if [ "$want" = "$got" ]; then
    ok "$label"
  else
    bad "${label} — want '${want}', got '${got}'"
  fi
}

check_contains() {
  local label="$1" needle="$2" haystack="$3"
  if [[ "$haystack" == *"$needle"* ]]; then
    ok "$label"
  else
    bad "${label} — '${needle}' not found in: ${haystack}"
  fi
}

# --------------------------------------------------------------------------
# The distribution matrix.
#
# This table is the test's OWN statement of what each distribution must map
# to, kept separate from install.sh's case statement on purpose: if somebody
# changes the routing there, this has to be changed here too, deliberately.
#
# almalinux is the row that matters most. get.docker.com's install case is
# `ubuntu|debian|raspbian` and `centos|fedora|rhel|rocky` — AlmaLinux is not
# in it and the script exits 1 with "Unsupported distribution 'almalinux'".
# Docker publishes no almalinux repository either. So AlmaLinux must take the
# CentOS-repository route, and a regression that quietly sends it back to
# get.docker.com would break the RHEL-clone half of the roadmap gate.
# --------------------------------------------------------------------------
expected_family_for() {
  case "$1" in
    ubuntu|debian) printf 'debian' ;;
    fedora|centos|rhel|rocky|almalinux) printf 'rpm' ;;
    *) printf 'UNKNOWN' ;;
  esac
}

expected_route_for() {
  case "$1" in
    ubuntu|debian|fedora|centos|rhel|rocky) printf 'get-docker' ;;
    almalinux) printf 'rpm-centos-repo' ;;
    *) printf 'UNKNOWN' ;;
  esac
}

# --------------------------------------------------------------------------
# Fixtures and stubs
# --------------------------------------------------------------------------

WORK=""
setup_workdir() {
  WORK="$(mktemp -d)"
  mkdir -p "${WORK}/os" "${WORK}/bin" "${WORK}/env"

  # A stub `docker` whose answers this suite controls. db_volume_exists and
  # stack_exists are the only two functions that shell out to it.
  # Every invocation is appended to STUB_LOG when set, so a test can assert
  # what the installer asked docker to do. The image answers come from the
  # environment: a container "running" STUB_CONTAINER_IMAGE while the tag
  # names STUB_SERVICE_IMAGE is the stale-container case.
  cat > "${WORK}/bin/docker" <<'STUB'
#!/usr/bin/env bash
[ -z "${STUB_LOG:-}" ] || printf '%s\n' "$*" >> "$STUB_LOG"
case "$1 $2" in
  "volume inspect") [ "${STUB_DB_VOLUME:-0}" = "1" ] ;;
  "image inspect") printf '%s\n' "${STUB_SERVICE_IMAGE:-sha256:same}" ;;
  "image prune") printf 'Total reclaimed space: 1.2GB\n' ;;
  "inspect -f") printf '%s\n' "${STUB_CONTAINER_IMAGE:-sha256:same}" ;;
  "compose -f")
    case " $* " in
      *" ps -q "*) printf 'cid123\n' ;;
    esac
    ;;
  *) exit 0 ;;
esac
STUB
  chmod +x "${WORK}/bin/docker"
  PATH="${WORK}/bin:${PATH}"
  export PATH
}

teardown_workdir() {
  [ -n "$WORK" ] && rm -rf "$WORK"
}

write_os_release() {
  local name="$1" body="$2"
  printf '%s\n' "$body" > "${WORK}/os/${name}"
  printf '%s' "${WORK}/os/${name}"
}

# Run install.sh's functions with its `main` guard suppressed, by sourcing it
# into a SUBSHELL — so a fail() that exits cannot take this test process with
# it, and so install.sh's own `set -eEuo pipefail` and ERR trap are the ones
# under test. Prints the subshell's combined output.
#
# Note the `eval`: the argument runs in THIS shell, which is where the sourced
# functions live. Spawning `bash -c` would run a process that has none of them.
# Every `eval` argument below is single-quoted on purpose (SC2016): it is code
# for the subshell to run after sourcing, not a string to expand here.
run_in_subshell() {
  (
    export STUB_DB_VOLUME="${STUB_DB_VOLUME:-0}"
    # shellcheck disable=SC1090 # path is computed
    HAMLANEH_OS_RELEASE="${FIXTURE:-/etc/os-release}" . "$INSTALL_SH"
    # Both are read by the sourced install.sh, not by this file.
    # shellcheck disable=SC2034
    ENV_FILE="${TEST_ENV_FILE:-${WORK}/env/.env}"
    # shellcheck disable=SC2034
    COMPOSE_FILE="${SCRIPT_DIR}/docker-compose.yml"
    "$@"
  ) 2>&1
}

# Same, but reports the exit status instead of the output.
status_in_subshell() {
  run_in_subshell "$@" >/dev/null 2>&1
  printf '%s' "$?"
}

# detect_os against $FIXTURE, reduced to the two fields the routing decision
# consists of. The log line is deliberately dropped: asserting against the
# printed value rather than the human-readable log is what makes the check
# about behaviour instead of about wording.
detect_fields() {
  local out
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'detect_os; printf "\n%s %s" "$OS_FAMILY" "$DOCKER_ROUTE"')"
  printf '%s' "${out##*$'\n'}"
}

# --------------------------------------------------------------------------
# Tests
# --------------------------------------------------------------------------

test_detect_os_table() {
  local id fixture fields
  for id in ubuntu debian fedora centos rhel rocky almalinux; do
    fixture="$(write_os_release "$id" "ID=${id}
VERSION_ID=\"1\"
PRETTY_NAME=\"Test ${id}\"")"
    fields="$(FIXTURE="$fixture" detect_fields)"
    check_eq "detect_os(${id})" \
      "$(expected_family_for "$id") $(expected_route_for "$id")" "$fields"
  done
}

test_detect_os_rejections() {
  local fixture out

  fixture="$(write_os_release "arch" 'ID=arch
PRETTY_NAME="Arch Linux"')"
  out="$(FIXTURE="$fixture" run_in_subshell detect_os)"
  check_contains "unsupported distro is refused by name" "unsupported distribution: Arch Linux" "$out"
  check_contains "unsupported distro says nothing was installed" "Nothing was installed" "$out"
  check_eq "unsupported distro exits 1" "1" "$(FIXTURE="$fixture" status_in_subshell detect_os)"

  fixture="${WORK}/os/does-not-exist"
  out="$(FIXTURE="$fixture" run_in_subshell detect_os)"
  check_contains "missing os-release is a clear error" "cannot read" "$out"
  check_eq "missing os-release exits 1" "1" "$(FIXTURE="$fixture" status_in_subshell detect_os)"

  # A derivative is accepted, but only via ID_LIKE and only out loud. An
  # rpm derivative must take the CentOS route: get.docker.com refuses any
  # ID it does not know by name, AlmaLinux being the proof.
  fixture="$(write_os_release "oracle" 'ID=ol
ID_LIKE="fedora rhel"
PRETTY_NAME="Oracle Linux Server 9.4"')"
  out="$(FIXTURE="$fixture" run_in_subshell detect_os)"
  check_contains "rpm derivative warns that it is untested" "not a distribution this installer is tested on" "$out"
  check_eq "rpm derivative takes the CentOS repo route" "rpm rpm-centos-repo" "$(FIXTURE="$fixture" detect_fields)"

  fixture="$(write_os_release "mint" 'ID=linuxmint
ID_LIKE=ubuntu
PRETTY_NAME="Linux Mint 22"')"
  check_eq "debian derivative is treated as debian" "debian get-docker" "$(FIXTURE="$fixture" detect_fields)"
}

test_arg_parsing() {
  local out
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'parse_args --domain chat.example.com; printf "%s" "$DOMAIN"')"
  check_contains "--domain <value>" "chat.example.com" "$out"

  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'parse_args --domain=chat.example.com; printf "%s" "$DOMAIN"')"
  check_contains "--domain=<value>" "chat.example.com" "$out"

  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'parse_args --non-interactive; printf "%s" "$NON_INTERACTIVE"')"
  check_contains "--non-interactive" "1" "$out"

  out="$(run_in_subshell parse_args --domain)"
  check_contains "--domain with no value is a clear error" "--domain requires a value" "$out"
  check_eq "--domain with no value exits 1" "1" "$(status_in_subshell parse_args --domain)"

  out="$(run_in_subshell parse_args --frobnicate)"
  check_contains "unknown argument is named" "unknown argument: --frobnicate" "$out"
  check_contains "unknown argument prints usage" "Usage: sudo ./install.sh" "$out"
  check_eq "unknown argument exits 1" "1" "$(status_in_subshell parse_args --frobnicate)"

  check_eq "--help exits 0" "0" "$(status_in_subshell parse_args --help)"
}

test_validate_domain() {
  local good bad_one out
  for good in localhost chat.example.com 203.0.113.5 example-host.co.uk 2001:db8::1; do
    check_eq "validate_domain accepts ${good}" "0" "$(status_in_subshell validate_domain "$good")"
  done
  for bad_one in "https://chat.example.com/" "chat example.com" "-lead" "cha_t.example.com" ""; do
    check_eq "validate_domain rejects '${bad_one}'" "1" "$(status_in_subshell validate_domain "$bad_one")"
  done
  out="$(run_in_subshell validate_domain "https://chat.example.com/")"
  check_contains "a URL gets an error that says what to pass instead" "A URL is not a domain" "$out"
}

test_bootstrap_guard() {
  local out
  # The one branch of the standalone bootstrap that must never be reached
  # twice: an exec'd copy that STILL lacks its files means the download or
  # extraction was broken, and fetching again would loop forever. The guard
  # env var is set by the exec; with it set, the only correct move is a
  # clear error naming the directory and the manual way out.
  out="$(HAMLANEH_BOOTSTRAPPED=1 run_in_subshell eval 'COMPOSE_FILE=/nonexistent/docker-compose.yml; require_checkout')"
  check_contains "a broken fetched checkout stops instead of looping" "still incomplete" "$out"
  check_eq "the broken-checkout error exits 1" "1" "$(HAMLANEH_BOOTSTRAPPED=1 status_in_subshell eval 'COMPOSE_FILE=/nonexistent/docker-compose.yml; require_checkout')"

  # From a real checkout (this repository), require_checkout is a no-op —
  # the bootstrap must never fire when the files are already here.
  check_eq "a full checkout passes require_checkout untouched" "0" "$(status_in_subshell require_checkout)"
}

test_resolve_domain_prompt() {
  local out expected
  # The same question resolve_domain asks the kernel, asked here so the
  # expectation tracks whatever machine runs the suite. Empty means this
  # environment has no route to derive a default from; the prompt must then
  # fall back to localhost, and that branch is what gets asserted instead.
  expected="$(ip -4 route get 1.1.1.1 2>/dev/null | sed -n 's/.*src \([0-9.]*\).*/\1/p' | head -n 1)"
  expected="${expected:-localhost}"

  # Enter on an empty prompt accepts the machine's own address — the whole
  # point of the default. stdin is a pipe, not a TTY: resolve_domain reads
  # wherever stdin points, which is also what makes this testable.
  # shellcheck disable=SC2016
  out="$(printf '\n' | run_in_subshell eval 'DOMAIN=""; NON_INTERACTIVE=0; resolve_domain; printf "%s" "$DOMAIN"')"
  check_contains "empty prompt answer takes the detected address" "serving on: ${expected}" "$out"
  check_contains "the menu names the detected address" "(${expected})" "$out"

  # A typed answer always wins over the detected default.
  # shellcheck disable=SC2016
  out="$(printf 'chat.example.com\n' | run_in_subshell eval 'DOMAIN=""; NON_INTERACTIVE=0; resolve_domain; printf "%s" "$DOMAIN"')"
  check_contains "a typed domain overrides the default" "chat.example.com" "$out"

  # Non-interactive stays localhost, NOT the detected address: a script's
  # result must not depend on which machine ran it.
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'DOMAIN=""; NON_INTERACTIVE=1; resolve_domain; printf "%s" "$DOMAIN"')"
  check_contains "non-interactive default is still localhost" "localhost" "$out"
}

test_heartbeat_run() {
  local out
  # A fast command produces no beat and its status passes through — the
  # helper must be free to wrap anything without changing its outcome.
  check_eq "heartbeat passes success through" "0" "$(status_in_subshell heartbeat_run quick true)"
  check_eq "heartbeat passes failure through" "1" "$(status_in_subshell heartbeat_run quick false)"
  # With the interval seam at 1s, a 3s command must prove it is alive.
  # shellcheck disable=SC2016
  out="$(HAMLANEH_HEARTBEAT_INTERVAL=1 run_in_subshell heartbeat_run "long step" sleep 3)"
  check_contains "a long command gets a heartbeat" "long step — still working" "$out"
}

test_resolve_ports() {
  local out
  # The snapshot and the stack probe are stubbed per branch: the subject is
  # the conversation's logic, not this machine's sockets.

  # Free ports: silence, defaults, DOMAIN untouched.
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'listening_snapshot() { printf "tcp LISTEN 0 128 0.0.0.0:22 \n"; }; stack_exists() { return 1; }; DOMAIN=203.0.113.5; NON_INTERACTIVE=1; resolve_ports; printf "%s %s %s" "$DOMAIN" "$HTTP_PORT" "$HTTPS_PORT"')"
  check_contains "free web ports keep the defaults" "203.0.113.5 80 443" "$out"

  # A taken port in non-interactive mode is a named refusal, never a silent
  # port invention.
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'listening_snapshot() { printf "tcp LISTEN 0 128 0.0.0.0:443 \n"; }; port_holder() { printf "nginx"; }; stack_exists() { return 1; }; DOMAIN=203.0.113.5; NON_INTERACTIVE=1; resolve_ports')"
  check_contains "non-interactive taken port names the holder" "443 (nginx)" "$out"
  check_contains "non-interactive taken port names the way out" "HAMLANEH_HTTPS_PORT" "$out"

  # The interactive path, choosing custom ports and accepting the defaults:
  # the port lands inside DOMAIN, which is what Caddy, the server's Origin
  # check and every printed link read.
  # shellcheck disable=SC2016
  out="$(printf '2\n\n\n' | run_in_subshell eval 'listening_snapshot() { printf "tcp LISTEN 0 128 0.0.0.0:443 \n"; }; stack_exists() { return 1; }; DOMAIN=203.0.113.5; NON_INTERACTIVE=0; resolve_ports; printf "RESULT %s %s %s" "$DOMAIN" "$HTTP_PORT" "$HTTPS_PORT"')"
  check_contains "custom ports land inside the domain" "RESULT 203.0.113.5:8443 8080 8443" "$out"

  # A domain with a port is refused whichever way it arrived: the trusted
  # certificate it exists for cannot be issued off 80/443.
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'DOMAIN=chat.example.com:8443; NON_INTERACTIVE=1; resolve_ports')"
  check_contains "a domain with a custom port is refused" "browser-trusted certificate" "$out"
  check_eq "the domain-with-port refusal exits 1" "1" "$(status_in_subshell eval 'DOMAIN=chat.example.com:8443; NON_INTERACTIVE=1; resolve_ports')"

  # A re-run reads its earlier choice back from deploy/.env.
  local env_fixture="${WORK}/ports/.env"
  mkdir -p "${WORK}/ports"
  printf 'HAMLANEH_DOMAIN=203.0.113.5:8443\nHAMLANEH_HTTP_PORT=8080\nHAMLANEH_HTTPS_PORT=8443\n' > "$env_fixture"
  # shellcheck disable=SC2016
  out="$(TEST_ENV_FILE="$env_fixture" run_in_subshell eval 'stack_exists() { return 0; }; DOMAIN=203.0.113.5:8443; resolve_ports; printf "%s %s" "$HTTP_PORT" "$HTTPS_PORT"')"
  check_contains "a re-run inherits its earlier ports" "8080 8443" "$out"
}

test_domain_kind() {
  check_eq "domain_kind localhost" "localhost" "$(run_in_subshell domain_kind localhost)"
  check_eq "domain_kind ipv4" "ipv4" "$(run_in_subshell domain_kind 203.0.113.5)"
  check_eq "domain_kind ipv6" "ipv6" "$(run_in_subshell domain_kind 2001:db8::1)"
  check_eq "domain_kind domain" "domain" "$(run_in_subshell domain_kind chat.example.com)"
  # host:port pairs classify by the host, so the custom-port refusal for
  # domains can name the real problem instead of calling it an IPv6.
  check_eq "domain_kind ipv4:port" "ipv4" "$(run_in_subshell domain_kind 203.0.113.5:8443)"
  check_eq "domain_kind localhost:port" "localhost" "$(run_in_subshell domain_kind localhost:8443)"
  check_eq "domain_kind domain:port" "domain" "$(run_in_subshell domain_kind chat.example.com:8443)"
  # The host alone is what Caddy's default_sni needs — an IP client sends
  # no SNI, and a port in the name would match no certificate.
  check_eq "domain_host strips a custom port" "203.0.113.5" "$(run_in_subshell domain_host 203.0.113.5:8443)"
  check_eq "domain_host leaves a plain domain alone" "chat.example.com" "$(run_in_subshell domain_host chat.example.com)"
  check_eq "domain_host leaves an IPv6 literal whole" "2001:db8::1" "$(run_in_subshell domain_host 2001:db8::1)"
  # And write_env records it, on a fresh file and on a domain change alike.
  local sni_env="${WORK}/sni/.env"
  mkdir -p "${WORK}/sni"
  # shellcheck disable=SC2016
  TEST_ENV_FILE="$sni_env" run_in_subshell eval 'DOMAIN=203.0.113.5:8443; HTTPS_PORT=8443; HTTP_PORT=8080; write_env' >/dev/null
  check_contains "a fresh .env carries the port-less SNI host" "HAMLANEH_DEFAULT_SNI=203.0.113.5" "$(cat "$sni_env")"
  # An IP install issues its own certificate; a domain asks ACME. A public
  # IP gets nothing from Caddy unless told, which is the whole point.
  check_contains "an IP install issues internally" "HAMLANEH_CERT_ISSUER=internal" "$(cat "$sni_env")"
  check_eq "cert_issuer: ipv4 -> internal" "internal" "$(run_in_subshell cert_issuer 203.0.113.5:8443)"
  check_eq "cert_issuer: localhost -> internal" "internal" "$(run_in_subshell cert_issuer localhost)"
  check_eq "cert_issuer: domain -> acme" "acme" "$(run_in_subshell cert_issuer chat.example.com)"
  # shellcheck disable=SC2016
  TEST_ENV_FILE="$sni_env" run_in_subshell eval 'DOMAIN=chat.example.com; write_env' >/dev/null
  check_contains "a domain change rewrites the SNI host" "HAMLANEH_DEFAULT_SNI=chat.example.com" "$(cat "$sni_env")"
  check_contains "a domain change switches the issuer to ACME" "HAMLANEH_CERT_ISSUER=acme" "$(cat "$sni_env")"
  check_eq "the SNI line is written once, not appended twice" "1" "$(grep -c '^HAMLANEH_DEFAULT_SNI=' "$sni_env")"
}

# The generated .env must carry a real value for every secret compose marks
# required with ${VAR:?}, and must be readable by nobody but root.
test_write_env_generates_real_secrets() {
  local env_file="${WORK}/env/fresh.env" out key value mode
  rm -f "$env_file"
  out="$(TEST_ENV_FILE="$env_file" run_in_subshell eval 'DOMAIN=chat.example.com; write_env')"
  check_contains "fresh run announces generation" "generating deploy/.env with random secrets" "$out"

  for key in POSTGRES_PASSWORD HAMLANEH_FILE_URL_KEY HAMLANEH_AUDIT_KEY \
             HAMLANEH_LIVEKIT_API_KEY HAMLANEH_LIVEKIT_API_SECRET HAMLANEH_ADMIN_PASSWORD; do
    value="$(sed -n "s/^${key}=//p" "$env_file" | tail -n 1)"
    if [ -z "$value" ] || [ "$value" = "REPLACED_AT_INSTALL" ]; then
      bad "${key} was not given a real generated value (got '${value}')"
    else
      ok "${key} has a real generated value"
    fi
  done

  # The two keys the server enforces a 32-byte floor on.
  for key in HAMLANEH_FILE_URL_KEY HAMLANEH_AUDIT_KEY; do
    value="$(sed -n "s/^${key}=//p" "$env_file" | tail -n 1)"
    if [ "${#value}" -ge 32 ]; then
      ok "${key} clears the server's 32-byte floor (${#value} bytes)"
    else
      bad "${key} is ${#value} bytes; internal/filesign and internal/audit require 32"
    fi
  done

  # LiveKit's pair ends up inside a YAML "key: secret" string, so it must be
  # hex — no character that YAML or that split would read as syntax.
  for key in HAMLANEH_LIVEKIT_API_KEY HAMLANEH_LIVEKIT_API_SECRET; do
    value="$(sed -n "s/^${key}=//p" "$env_file" | tail -n 1)"
    if [[ "$value" =~ ^[0-9a-f]+$ ]]; then
      ok "${key} is hex, safe inside compose's LIVEKIT_KEYS string"
    else
      bad "${key} is not hex: '${value}'"
    fi
  done

  mode="$(stat -c '%a' "$env_file" 2>/dev/null || stat -f '%Lp' "$env_file" 2>/dev/null || printf 'unknown')"
  check_eq ".env is chmod 600" "600" "$mode"

  if grep -q 'REPLACED_AT_INSTALL' "$env_file"; then
    bad "the generated .env still contains a REPLACED_AT_INSTALL placeholder"
  else
    ok "the generated .env contains no placeholder value"
  fi
}

# The property most likely to be quietly broken.
test_idempotent_rerun() {
  local env_file="${WORK}/env/idem.env" before after out
  rm -f "$env_file"
  TEST_ENV_FILE="$env_file" run_in_subshell eval 'DOMAIN=chat.example.com; write_env' >/dev/null
  before="$(cat "$env_file")"

  out="$(TEST_ENV_FILE="$env_file" run_in_subshell eval '
    DOMAIN=chat.example.com
    write_env
    scrub_placeholder_secrets
    ensure_postgres_password_env
    ensure_admin_env
    ensure_mail_env
    ensure_file_url_key_env
    ensure_audit_key_env
    ensure_livekit_env')"
  after="$(cat "$env_file")"

  check_contains "second run says it left .env alone" "deploy/.env already up to date — leaving it untouched" "$out"
  if [ "$before" = "$after" ]; then
    ok "second run leaves deploy/.env byte-for-byte identical"
  else
    bad "second run modified deploy/.env:
$(diff <(printf '%s' "$before") <(printf '%s' "$after") || true)"
  fi

  # And it must not have re-announced generating anything.
  for needle in "generating deploy/.env" "generating HAMLANEH_FILE_URL_KEY" \
                "generating HAMLANEH_AUDIT_KEY" "generating the LiveKit credential pair" \
                "generating POSTGRES_PASSWORD"; do
    if [[ "$out" == *"$needle"* ]]; then
      bad "second run regenerated a secret: ${needle}"
    else
      ok "second run did not regenerate: ${needle}"
    fi
  done
}

# Changing the domain must rewrite the two lines derived from it and nothing
# else. HAMLANEH_FILES_DOMAIN is the second of those: it is files.<domain> for
# a real domain, so leaving it behind would point the files origin at the
# hostname the install just stopped using.
test_domain_change_keeps_secrets() {
  local env_file="${WORK}/env/domain.env" before after
  # Three lines derive from the domain: the domain itself, the files
  # hostname, and the default SNI host Caddy serves to clients that name
  # no site. Everything else — every secret — must survive byte for byte.
  local derived='^HAMLANEH_DOMAIN=\|^HAMLANEH_FILES_DOMAIN=\|^HAMLANEH_DEFAULT_SNI=\|^HAMLANEH_CERT_ISSUER='
  rm -f "$env_file"
  TEST_ENV_FILE="$env_file" run_in_subshell eval 'DOMAIN=old.example.com; write_env' >/dev/null
  before="$(grep -v "$derived" "$env_file")"
  TEST_ENV_FILE="$env_file" run_in_subshell eval 'DOMAIN=new.example.com; write_env' >/dev/null
  after="$(grep -v "$derived" "$env_file")"

  check_eq "domain change updates HAMLANEH_DOMAIN" "HAMLANEH_DOMAIN=new.example.com" \
    "$(grep '^HAMLANEH_DOMAIN=' "$env_file")"
  check_eq "domain change updates the files hostname with it" \
    "HAMLANEH_FILES_DOMAIN=files.new.example.com" \
    "$(grep '^HAMLANEH_FILES_DOMAIN=' "$env_file")"
  check_eq "domain change updates the default SNI host with it" \
    "HAMLANEH_DEFAULT_SNI=new.example.com" \
    "$(grep '^HAMLANEH_DEFAULT_SNI=' "$env_file")"
  if [ "$before" = "$after" ]; then
    ok "domain change leaves every other line untouched"
  else
    bad "domain change altered lines other than the three derived from the domain"
  fi
}

# README's quick start says `cp .env.example .env`. Doing that and then
# running the installer must not leave a single repository-published value in
# place. Two of these six placeholders stop the server booting; the other four
# would not have.
test_placeholder_scrub() {
  local env_file="${WORK}/env/placeholder.env" out key value
  cp "${SCRIPT_DIR}/.env.example" "$env_file"
  chmod 600 "$env_file"

  out="$(TEST_ENV_FILE="$env_file" STUB_DB_VOLUME=0 run_in_subshell eval '
    scrub_placeholder_secrets
    ensure_postgres_password_env
    ensure_admin_env
    ensure_mail_env
    ensure_file_url_key_env
    ensure_audit_key_env
    ensure_livekit_env')"

  check_contains "the scrub says what it is doing" "still holds REPLACED_AT_INSTALL placeholders" "$out"

  if grep -q 'REPLACED_AT_INSTALL' "$env_file"; then
    bad "REPLACED_AT_INSTALL survived the scrub: $(grep -n 'REPLACED_AT_INSTALL' "$env_file")"
  else
    ok "no REPLACED_AT_INSTALL value survives a copied .env.example"
  fi

  for key in POSTGRES_PASSWORD HAMLANEH_FILE_URL_KEY HAMLANEH_AUDIT_KEY \
             HAMLANEH_LIVEKIT_API_KEY HAMLANEH_LIVEKIT_API_SECRET \
             HAMLANEH_ADMIN_USERNAME HAMLANEH_ADMIN_PASSWORD HAMLANEH_ADMIN_LOCALE; do
    value="$(sed -n "s/^${key}=//p" "$env_file" | tail -n 1)"
    if [ -n "$value" ]; then
      ok "${key} was regenerated after the scrub"
    else
      bad "${key} is missing or empty after the scrub — compose would refuse to start"
    fi
  done

  # The pair must never be left half-written.
  check_eq "LiveKit key appears exactly once" "1" "$(grep -c '^HAMLANEH_LIVEKIT_API_KEY=' "$env_file")"
  check_eq "LiveKit secret appears exactly once" "1" "$(grep -c '^HAMLANEH_LIVEKIT_API_SECRET=' "$env_file")"
}

# The one placeholder that cannot simply be replaced: an already-initialised
# database is using it as its real password.
test_postgres_placeholder_with_existing_volume() {
  local env_file="${WORK}/env/pgvol.env" out status
  cp "${SCRIPT_DIR}/.env.example" "$env_file"
  chmod 600 "$env_file"

  out="$(TEST_ENV_FILE="$env_file" STUB_DB_VOLUME=1 run_in_subshell ensure_postgres_password_env)"
  status="$(TEST_ENV_FILE="$env_file" STUB_DB_VOLUME=1 status_in_subshell ensure_postgres_password_env)"

  check_eq "an existing db volume with a placeholder password stops the install" "1" "$status"
  check_contains "it names the volume" "hamlaneh_db_data" "$out"
  check_contains "it gives the command that fixes it" "down -v" "$out"
  check_contains "it warns against running that with real data" "do not run that" "$out"
  # It may only say that because it looked: this .env carries the two
  # placeholders the server refuses to boot on, so nothing can have written
  # to that volume.
  check_contains "it says what makes the volume provably empty" "refuses to start on" "$out"
}

# An .env with a real deploy/.env's boot keys and only POSTGRES_PASSWORD left
# on the placeholder. Neither 32-byte floor is in the way, so a server HAS
# booted here: this volume holds messages, files and accounts. The install
# still has to stop — the database's password is a published constant — but
# the one thing it must never do is tell this operator to delete the volume.
#
# The bug this replaces: ensure_postgres_password_env branched on
# POSTGRES_PASSWORD alone and concluded from it that the server had never
# started, so this exact .env got "docker compose down -v".
test_postgres_placeholder_on_a_live_install() {
  local env_file="${WORK}/env/live.env" out status
  {
    printf 'HAMLANEH_DOMAIN=chat.example.com\n'
    printf 'POSTGRES_PASSWORD=REPLACED_AT_INSTALL\n'
    printf 'HAMLANEH_FILE_URL_KEY=%s\n' "$(openssl rand -base64 32)"
    printf 'HAMLANEH_AUDIT_KEY=%s\n' "$(openssl rand -base64 32)"
  } > "$env_file"
  chmod 600 "$env_file"

  out="$(TEST_ENV_FILE="$env_file" STUB_DB_VOLUME=1 run_in_subshell ensure_postgres_password_env)"
  status="$(TEST_ENV_FILE="$env_file" STUB_DB_VOLUME=1 status_in_subshell ensure_postgres_password_env)"

  check_eq "a live install with a placeholder password stops the install" "1" "$status"
  if [[ "$out" == *"down -v"* ]]; then
    bad "an install that may hold data was told to run 'down -v': ${out}"
  else
    ok "an install that may hold data is never told to run 'down -v'"
  fi
  check_contains "it says the data may be real" "may be holding real messages" "$out"
  check_contains "it gives a fix that keeps the data" "ALTER USER hamlaneh PASSWORD" "$out"
  check_contains "it says why rotating is not optional" "password is public" "$out"

  # The same .env on a host with no database volume is just a fresh install.
  out="$(TEST_ENV_FILE="$env_file" STUB_DB_VOLUME=0 run_in_subshell ensure_postgres_password_env)"
  check_contains "with no db volume the password is simply generated" "generating POSTGRES_PASSWORD" "$out"
}

# The same root cause, the other function. scrub_placeholder_secrets used to
# justify replacing the LiveKit pair and the admin trio with "no LiveKit token
# signed by it, and no admin ever created" — true only while the server cannot
# boot. Both are values the stack accepts, so beside real boot keys they were
# live. Replacing them is still right; doing it silently is what was wrong.
test_scrub_on_a_live_install_says_what_it_costs() {
  local env_file="${WORK}/env/livescrub.env" out
  {
    printf 'HAMLANEH_DOMAIN=chat.example.com\n'
    printf 'HAMLANEH_FILE_URL_KEY=%s\n' "$(openssl rand -base64 32)"
    printf 'HAMLANEH_AUDIT_KEY=%s\n' "$(openssl rand -base64 32)"
    printf 'HAMLANEH_LIVEKIT_API_KEY=REPLACED_AT_INSTALL\n'
    printf 'HAMLANEH_LIVEKIT_API_SECRET=REPLACED_AT_INSTALL\n'
  } > "$env_file"
  chmod 600 "$env_file"

  out="$(TEST_ENV_FILE="$env_file" STUB_DB_VOLUME=1 run_in_subshell eval '
    scrub_placeholder_secrets
    ensure_livekit_env')"

  check_contains "a live placeholder is still replaced" "generating the LiveKit credential pair" "$out"
  check_contains "and the run says what that costs" "any call in progress ends" "$out"
  if grep -q 'REPLACED_AT_INSTALL' "$env_file"; then
    bad "a published LiveKit secret survived on a live install"
  else
    ok "a published LiveKit secret is replaced even on a live install"
  fi

  # On an install that provably never booted there is nothing to warn about,
  # and a warning that fires either way teaches operators to ignore it.
  local fresh="${WORK}/env/freshscrub.env"
  cp "${SCRIPT_DIR}/.env.example" "$fresh"
  chmod 600 "$fresh"
  out="$(TEST_ENV_FILE="$fresh" STUB_DB_VOLUME=1 run_in_subshell scrub_placeholder_secrets)"
  if [[ "$out" == *"any call in progress ends"* ]]; then
    bad "an install that cannot have booted was warned about live calls"
  else
    ok "an install that cannot have booted gets no live-call warning"
  fi
}

# A half-configured LiveKit pair is exactly what compose refuses; the
# installer must never be the thing that creates one, and must say so.
test_livekit_half_pair() {
  local env_file="${WORK}/env/half.env" out
  printf 'HAMLANEH_DOMAIN=localhost\nHAMLANEH_LIVEKIT_API_KEY=abc123\n' > "$env_file"
  out="$(TEST_ENV_FILE="$env_file" run_in_subshell ensure_livekit_env)"
  check_contains "half a LiveKit pair is a clear error" "only one half of the LiveKit credential pair" "$out"
  check_eq "half a LiveKit pair exits 1" "1" "$(TEST_ENV_FILE="$env_file" status_in_subshell ensure_livekit_env)"
}

# An .env from an older install, missing keys added by later phases, must be
# upgraded in place rather than rejected or rewritten.
test_upgrade_from_older_env() {
  local env_file="${WORK}/env/old.env" pw out
  printf 'HAMLANEH_DOMAIN=chat.example.com\nPOSTGRES_PASSWORD=kept-by-the-upgrade\n' > "$env_file"
  chmod 600 "$env_file"
  out="$(TEST_ENV_FILE="$env_file" run_in_subshell eval '
    DOMAIN=chat.example.com
    write_env
    scrub_placeholder_secrets
    ensure_postgres_password_env
    ensure_admin_env
    ensure_mail_env
    ensure_file_url_key_env
    ensure_audit_key_env
    ensure_livekit_env')"

  pw="$(sed -n 's/^POSTGRES_PASSWORD=//p' "$env_file" | tail -n 1)"
  check_eq "an existing Postgres password is never regenerated" "kept-by-the-upgrade" "$pw"
  check_contains "the missing file-URL key is generated" "generating HAMLANEH_FILE_URL_KEY" "$out"
  check_contains "the missing audit key is generated" "generating HAMLANEH_AUDIT_KEY" "$out"
  check_contains "the missing LiveKit pair is generated" "generating the LiveKit credential pair" "$out"
  check_contains "the missing mail block is added" "adding the optional mail settings" "$out"
  check_contains "the missing admin block is added" "adding admin bootstrap variables" "$out"
}

# Bare-IP mode must state the trade-off rather than let it be discovered.
test_bare_ip_posture() {
  local out
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'DOMAIN=203.0.113.5; print_tls_posture')"
  check_contains "bare IP names the mode" "BARE-IP MODE" "$out"
  # There is one outcome here, not two. Caddy classifies every IP address as an
  # internal name and issues from its own CA without asking a public CA for
  # anything — caddyserver.com/docs/automatic-https, "hostnames qualify for
  # publicly-trusted certificates if they ... are not an IP address". Let's
  # Encrypt has issued IP certificates under its short-lived profile since
  # 2026-01-15, and caddyserver/caddy#7399 is an operator who configured that
  # profile explicitly and still got "cannot have public IP certificate". The
  # message must state the warning as the outcome, not offer it as a coin flip.
  check_contains "bare IP states there is no public certificate" "no publicly trusted certificate" "$out"
  check_contains "bare IP states the browser warning as the outcome" "full-page" "$out"
  check_contains "bare IP admits HSTS is not honoured" "browsers ignore it" "$out"
  check_contains "bare IP says why LE's IP certificates do not reach it" "Caddy 2.10" "$out"
  check_contains "bare IP states no default was weakened" "nothing was weakened" "$out"
  check_contains "bare IP names the fix" "The fix is a domain name" "$out"
  # files_domain() sends bare-IP installs to files.localhost, which Caddy
  # certifies internally. The box used to promise a month of failing ACME
  # orders for a hostname this installer stopped configuring.
  for stale in "not certain" "files.203.0.113.5" "IP-suffixed name" "certificate obtained"; do
    if [[ "$out" == *"$stale"* ]]; then
      bad "the bare-IP box still says '${stale}', which is no longer true"
    else
      ok "the bare-IP box no longer says '${stale}'"
    fi
  done

  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'DOMAIN=chat.example.com; print_tls_posture')"
  check_contains "a real domain gets the ACME line" "publicly trusted certificate" "$out"
  if [[ "$out" == *"BARE-IP MODE"* ]]; then
    bad "a real domain was given the bare-IP warning"
  else
    ok "a real domain is not given the bare-IP warning"
  fi

  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'DOMAIN=localhost; print_tls_posture')"
  check_contains "localhost explains the internal CA" "internal CA" "$out"
}

# Every failure path must print something an operator can act on, and none
# may be a bare `set -e` death.
test_no_silent_failures() {
  local out
  out="$(FIXTURE="${WORK}/os/does-not-exist" run_in_subshell eval 'pkg_install curl')"
  check_contains "pkg_install before detect_os is caught, not silent" "internal error" "$out"

  # The ERR trap: force an unexpected failure and check it names a line.
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'false')"
  check_contains "an unexpected failure names install.sh and a line" "install.sh line" "$out"
  check_contains "an unexpected failure says re-running is safe" "Re-running this script is safe" "$out"
}

# cosign's pin must match the one the release pipeline signs with. Drift
# between the two is only discovered during an incident, so it is asserted
# here against the workflow files themselves.
test_cosign_pin_matches_ci() {
  local ours theirs
  ours="$(sed -n 's/^COSIGN_VERSION="\(.*\)"$/\1/p' "$INSTALL_SH" | head -n 1)"
  check_contains "install.sh pins a cosign version" "v3" "$ours"

  theirs="$(grep -rhoE 'cosign-release: *v[0-9.]+|COSIGN_VERSION: *v[0-9.]+' \
    "${SCRIPT_DIR}/../.github/workflows/" 2>/dev/null |
    grep -oE 'v[0-9.]+' | sort -u)"
  if [ -z "$theirs" ]; then
    bad "no cosign version found in .github/workflows — cannot check the pin matches"
    return
  fi
  # Every version named by CI must be the one install.sh puts on the host.
  local v
  while read -r v; do
    check_eq "cosign pin matches CI (${v})" "$ours" "$v"
  done <<<"$theirs"
}

# A missing sibling script must produce an instruction, never a crash, and
# must never be reported as enabled.
test_companion_enablement() {
  local out
  out="$(run_in_subshell enable_companion no-such-script.sh "automatic updates" --install-timer)"
  check_contains "a missing companion names the file" "no-such-script.sh is missing" "$out"
  check_contains "a missing companion says the feature is NOT on" "is NOT enabled" "$out"
  check_contains "a missing companion gives the command to run later" "sudo" "$out"
  check_eq "a missing companion returns non-zero, not a crash" "1" \
    "$(status_in_subshell enable_companion no-such-script.sh "automatic updates" --install-timer)"

  # Without cosign the updater would refuse every release anyway, so the
  # timer must not be armed and must say why.
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'COSIGN_STATE="not installed"; enable_update_timer; printf "\nSTATE=%s" "$UPDATE_TIMER_STATE"')"
  check_contains "no cosign means updates are not enabled" "automatic updates are NOT enabled" "$out"
  check_contains "no cosign explains the updater would refuse anyway" "cannot verify" "$out"
  check_contains "no cosign leaves the reported state off" "STATE=not enabled" "$out"
}

# The end-of-run summary must report what happened, not what was intended.
test_background_jobs_summary() {
  local out
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval '
    COSIGN_STATE="v3.0.6 (installed by this script)"
    UPDATE_TIMER_STATE="enabled (hamlaneh-update.timer, four times a day)"
    BACKUP_TIMER_STATE="enabled, daily (see the line above for systemd or cron)"
    print_background_jobs')"
  check_contains "summary reports cosign" "cosign v3.0.6" "$out"
  check_contains "summary reports the update timer" "hamlaneh-update.timer" "$out"
  check_contains "summary reports backups as enabled" "Encrypted backups:  enabled" "$out"
  # The backup script picks systemd or cron at run time, so the summary must
  # not name a mechanism it did not observe.
  if [[ "$out" == *"hamlaneh-backup.timer"* ]]; then
    bad "the summary named a systemd timer for backups, which may have been a cron entry"
  else
    ok "the backup line names no mechanism it did not observe"
  fi
  if [[ "$out" == *"Something above is off"* ]]; then
    bad "the all-green summary warned anyway"
  else
    ok "an all-green summary carries no warning"
  fi

  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'BACKUP_TIMER_STATE="not enabled"; print_background_jobs')"
  check_contains "a half-armed summary says so" "Something above is off" "$out"
}

test_recreate_stale_containers() {
  local logf="${WORK}/docker.log" out
  # A container running an image the tag no longer names is stale, and the
  # only correct move is to replace it — silently reporting success on the
  # old binary is the failure this function exists to stop.
  : > "$logf"
  STUB_LOG="$logf" STUB_CONTAINER_IMAGE=sha256:old STUB_SERVICE_IMAGE=sha256:new \
    run_in_subshell recreate_stale_containers >/dev/null
  check_contains "a container on an older image is recreated" "--force-recreate server" "$(cat "$logf")"
  check_contains "recreation is scoped to that service" "--no-deps" "$(cat "$logf")"

  # An up-to-date container is left alone: a no-change re-run must bounce
  # nothing, or the idempotency promise is a restart in disguise.
  : > "$logf"
  STUB_LOG="$logf" STUB_CONTAINER_IMAGE=sha256:same STUB_SERVICE_IMAGE=sha256:same \
    run_in_subshell recreate_stale_containers >/dev/null
  if grep -q -- "--force-recreate" "$logf"; then
    bad "an up-to-date container was bounced"
  else
    ok "an up-to-date container is left running"
  fi

  out="$(run_in_subshell prune_dangling_images)"
  check_contains "the prune reports what it reclaimed" "Total reclaimed space: 1.2GB" "$out"
}

test_help_output() {
  local out
  out="$(run_in_subshell usage)"
  check_contains "help names the supported distributions" "Ubuntu, Debian, Fedora, RHEL and its clones" "$out"
  check_contains "help documents --domain" "--domain" "$out"
  check_contains "help documents --non-interactive" "--non-interactive" "$out"
}

# --------------------------------------------------------------------------
# --os-only: detection against this host's real /etc/os-release.
# Meaningful only when this host IS one of the four families, which is what
# --matrix arranges.
# --------------------------------------------------------------------------
run_os_only() {
  setup_workdir
  local id want_family
  # shellcheck disable=SC1091
  id="$(. /etc/os-release >/dev/null 2>&1; printf '%s' "${ID:-}")"
  want_family="$(expected_family_for "$id")"
  if [ "$want_family" = "UNKNOWN" ]; then
    bad "--os-only ran on '${id}', which is not one of the distributions this gate covers"
    finish
  fi

  check_eq "real ${id} /etc/os-release -> family and Docker route" \
    "${want_family} $(expected_route_for "$id")" \
    "$(FIXTURE=/etc/os-release detect_fields)"
  finish
}

# --------------------------------------------------------------------------
# --matrix: run --os-only inside the four families' real images.
# --------------------------------------------------------------------------
run_matrix() {
  local repo image failed=0
  repo="$(cd "${SCRIPT_DIR}/.." && pwd)"
  for image in ubuntu:24.04 debian:12 fedora:41 rockylinux/rockylinux:9 almalinux:9; do
    printf '\n=== %s ===\n' "$image"
    if docker run --rm -v "${repo}:/repo:ro" "$image" \
        bash /repo/deploy/install.test.sh --os-only; then
      :
    else
      failed=1
    fi
  done
  if [ "$failed" -ne 0 ]; then
    printf '\nmatrix: at least one image failed\n' >&2
    exit 1
  fi
  printf '\nmatrix: all images passed\n'
}

finish() {
  teardown_workdir
  printf '\n%d passed, %d failed\n' "$PASS_COUNT" "$FAIL_COUNT"
  [ "$FAIL_COUNT" -eq 0 ] || exit 1
  exit 0
}

main() {
  case "${1:-}" in
    --matrix) run_matrix; exit 0 ;;
    --os-only) run_os_only ;;
    "") ;;
    *) printf 'usage: %s [--os-only|--matrix]\n' "$0" >&2; exit 2 ;;
  esac

  [ -r "$INSTALL_SH" ] || { printf 'ERROR: %s not found\n' "$INSTALL_SH" >&2; exit 2; }
  command -v openssl >/dev/null 2>&1 || {
    printf 'ERROR: openssl is required by the full suite (use --os-only without it).\n' >&2
    exit 2
  }

  setup_workdir
  test_detect_os_table
  test_detect_os_rejections
  test_arg_parsing
  test_validate_domain
  test_bootstrap_guard
  test_heartbeat_run
  test_resolve_domain_prompt
  test_resolve_ports
  test_domain_kind
  test_write_env_generates_real_secrets
  test_idempotent_rerun
  test_domain_change_keeps_secrets
  test_placeholder_scrub
  test_postgres_placeholder_with_existing_volume
  test_postgres_placeholder_on_a_live_install
  test_scrub_on_a_live_install_says_what_it_costs
  test_livekit_half_pair
  test_upgrade_from_older_env
  test_bare_ip_posture
  test_no_silent_failures
  test_cosign_pin_matches_ci
  test_companion_enablement
  test_background_jobs_summary
  test_recreate_stale_containers
  test_help_output
  finish
}

main "$@"
