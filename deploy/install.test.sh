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

# The admin dashboard's own port (ADR 015).
#
# The subject is the conversation and what it writes, never this machine's
# sockets: the listening snapshot and the port holder are stubbed per branch.
# The property that matters most is the LAST one — that "this machine only"
# really produces a 127.0.0.1 publication — because getting it wrong publishes
# the admin API to the internet on an install whose operator asked for the
# opposite, and nothing else in this suite would notice.
test_resolve_admin_port() {
  local out env_file="${WORK}/admin/.env"
  mkdir -p "${WORK}/admin"
  # A fresh install: no deploy/.env yet, so nothing on this machine can break.
  # Named explicitly rather than left to the default so no earlier test's
  # leftovers can change which question shape these exercise.
  local fresh_env="${WORK}/admin/never-created.env"
  rm -f "$fresh_env"

  # Answer 1, default port. "Reachable from the internet" is an EMPTY bind,
  # because empty is what a docker port mapping reads as every interface.
  # shellcheck disable=SC2016
  out="$(printf '1\n\n' | TEST_ENV_FILE="$fresh_env" run_in_subshell eval 'listening_snapshot() { printf "tcp LISTEN 0 128 0.0.0.0:22 \n"; }; DOMAIN=203.0.113.5; NON_INTERACTIVE=0; resolve_admin_port; printf "RESULT [%s] [%s]" "$ADMIN_PORT" "$ADMIN_BIND"')"
  check_contains "the internet answer takes the default port and no bind" "RESULT [9443] []" "$out"
  check_contains "the internet answer says to open the port" "open 9443/tcp in your cloud firewall" "$out"

  # Answer 2, default port: loopback, and the tunnel command carries the real
  # port rather than a placeholder.
  # shellcheck disable=SC2016
  out="$(printf '2\n\n' | TEST_ENV_FILE="$fresh_env" run_in_subshell eval 'listening_snapshot() { printf "tcp LISTEN 0 128 0.0.0.0:22 \n"; }; DOMAIN=203.0.113.5; NON_INTERACTIVE=0; resolve_admin_port; printf "RESULT [%s] [%s]" "$ADMIN_PORT" "$ADMIN_BIND"')"
  check_contains "the loopback answer binds 127.0.0.1" "RESULT [9443] [127.0.0.1:]" "$out"
  check_contains "the loopback answer prints a usable tunnel command" "ssh -L 9443:localhost:9443 <you>@203.0.113.5" "$out"
  check_contains "the loopback answer says the app's Admin control will not reach it" \
    "the Admin control will not reach this port" "$out"

  # On a machine with no instance yet, Enter is the CLOSED answer: nothing can
  # break, so the default is the safe one rather than the timid one.
  # shellcheck disable=SC2016
  out="$(printf '\n\n' | TEST_ENV_FILE="$fresh_env" run_in_subshell eval 'listening_snapshot() { printf "tcp LISTEN 0 128 0.0.0.0:22 \n"; }; DOMAIN=203.0.113.5; NON_INTERACTIVE=0; resolve_admin_port; printf "RESULT [%s] [%s]" "$ADMIN_PORT" "$ADMIN_BIND"')"
  check_contains "a fresh install defaults to the loopback answer" "RESULT [9443] [127.0.0.1:]" "$out"
  check_contains "a fresh install is offered two answers" "choose 1 or 2 [2]" "$out"

  # An install that PREDATES this feature is the case that must not move on
  # its own. Its SCIM provisioning may be pointed at the web port right now,
  # and an operator pressing Enter through a question they have never seen is
  # not consent to move it. Third answer, and it is the default.
  local legacy_env="${WORK}/admin/legacy.env"
  printf 'HAMLANEH_DOMAIN=203.0.113.5\nPOSTGRES_PASSWORD=keep-me\n' > "$legacy_env"
  # shellcheck disable=SC2016
  out="$(printf '\n' | TEST_ENV_FILE="$legacy_env" run_in_subshell eval 'listening_snapshot() { printf "tcp LISTEN 0 128 0.0.0.0:22 \n"; }; DOMAIN=203.0.113.5; NON_INTERACTIVE=0; resolve_admin_port; printf "RESULT [%s] [%s]" "$ADMIN_PORT" "$ADMIN_BIND"')"
  check_contains "an existing install is offered a third answer" "3) leave it where it is" "$out"
  check_contains "and that third answer is its default" "choose 1, 2 or 3 [3]" "$out"
  check_contains "pressing Enter on an existing install moves nothing" "RESULT [] []" "$out"
  if grep -q 'admin dashboard port' <<<"$out"; then
    bad "an existing install was asked for a port after choosing to leave the surface alone"
  else
    ok "leaving it alone asks for no port"
  fi

  # The third answer is a default, not a wall: an operator who says 2 still
  # gets the split on an existing install.
  # shellcheck disable=SC2016
  out="$(printf '2\n\n' | TEST_ENV_FILE="$legacy_env" run_in_subshell eval 'listening_snapshot() { printf "tcp LISTEN 0 128 0.0.0.0:22 \n"; }; DOMAIN=203.0.113.5; NON_INTERACTIVE=0; resolve_admin_port; printf "RESULT [%s] [%s]" "$ADMIN_PORT" "$ADMIN_BIND"')"
  check_contains "an existing install can still choose to move the surface" "RESULT [9443] [127.0.0.1:]" "$out"
  # The menu itself describes what option 1 would cost, so the assertion is
  # about the CONFIRMATION: having chosen "this machine only", the operator
  # must not then be told to open the port in their cloud firewall.
  if grep -q 'open 9443/tcp in your cloud firewall' <<<"$out"; then
    bad "the loopback answer told the operator to open the port in their cloud firewall"
  else
    ok "the loopback answer does not tell the operator to open the port"
  fi

  # A taken port is re-asked with its holder named, and the tunnel command
  # then shows the port that was actually chosen.
  # shellcheck disable=SC2016
  out="$(printf '2\n\n9444\n' | TEST_ENV_FILE="$fresh_env" run_in_subshell eval 'listening_snapshot() { printf "tcp LISTEN 0 128 0.0.0.0:9443 \n"; }; port_holder() { printf "grafana"; }; DOMAIN=203.0.113.5; NON_INTERACTIVE=0; resolve_admin_port; printf "RESULT [%s] [%s]" "$ADMIN_PORT" "$ADMIN_BIND"')"
  check_contains "a taken admin port names its holder" "port 9443 is also in use (by grafana)" "$out"
  check_contains "the re-asked answer is the one that sticks" "RESULT [9444] [127.0.0.1:]" "$out"
  check_contains "the tunnel command shows the chosen port, not the default" "ssh -L 9444:localhost:9444" "$out"

  # The admin port is published whether or not the split is on, so a web port
  # that equals it is a clash inside our own compose file. Both the chosen
  # number and the default that stands in when nothing was chosen are covered,
  # because the second one is the case nobody would think to test.
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'stack_exists() { return 1; }; ADMIN_PORT=9443; HTTP_PORT=80; HTTPS_PORT=9443; preflight_ports')"
  check_contains "an admin port that collides with a web port is refused" "is a web port here AND where deploy/docker-compose.yml publishes the admin dashboard" "$out"
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'stack_exists() { return 1; }; ADMIN_PORT=; HTTP_PORT=80; HTTPS_PORT=9443; preflight_ports')"
  check_contains "the split being off does not hide the collision" "port 9443 is a web port here" "$out"

  # Non-interactive leaves the split OFF: an existing script's admin and SCIM
  # calls must keep answering on the web port.
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'DOMAIN=203.0.113.5; NON_INTERACTIVE=1; resolve_admin_port; printf "RESULT [%s] [%s]" "$ADMIN_PORT" "$ADMIN_BIND"')"
  check_contains "non-interactive leaves the admin split off" "RESULT [] []" "$out"
  if grep -q 'Admin dashboard' <<<"$out"; then
    bad "non-interactive mode asked the admin-port question"
  else
    ok "non-interactive mode asks nothing about the admin port"
  fi

  # A re-run inherits the earlier answer: the question does not come back and
  # the choice is not lost.
  local rerun_env="${WORK}/admin/rerun.env"
  printf 'HAMLANEH_DOMAIN=203.0.113.5\nHAMLANEH_ADMIN_ADDR=:9090\nHAMLANEH_ADMIN_PORT=9443\nHAMLANEH_ADMIN_BIND=127.0.0.1:\n' > "$rerun_env"
  # shellcheck disable=SC2016
  out="$(TEST_ENV_FILE="$rerun_env" run_in_subshell eval 'DOMAIN=203.0.113.5; NON_INTERACTIVE=0; resolve_admin_port; printf "RESULT [%s] [%s]" "$ADMIN_PORT" "$ADMIN_BIND"')"
  check_contains "a re-run inherits its earlier admin port and bind" "RESULT [9443] [127.0.0.1:]" "$out"
  if grep -q 'Admin dashboard' <<<"$out"; then
    bad "a re-run asked the admin-port question again"
  else
    ok "a re-run does not ask the admin-port question again"
  fi

  # What the answers actually write, and what compose then does with it.
  rm -f "$env_file"
  # shellcheck disable=SC2016
  TEST_ENV_FILE="$env_file" run_in_subshell eval 'DOMAIN=203.0.113.5; ADMIN_PORT=9443; ADMIN_BIND=127.0.0.1:; write_env' >/dev/null
  check_contains "the loopback answer is written as a 127.0.0.1 bind" "HAMLANEH_ADMIN_BIND=127.0.0.1:" "$(cat "$env_file")"
  check_contains "the answer turns the server's second listener on" "HAMLANEH_ADMIN_ADDR=:9090" "$(cat "$env_file")"
  # What the app port does with the admin surface, in two halves that must not
  # blur into each other. The POWERED half is refused: a wider expression would
  # take the sign-in routes off the chat port with it, a narrower one would
  # leave the admin API answering there, which is the thing the operator was
  # told they had shut. The PAGE half is redirected instead, which is what lets
  # the sidebar keep a plain "/admin" — so the page must NOT be in the refusal.
  check_contains "the app port is told which powered paths have moved" \
    'HAMLANEH_ADMIN_MOVED_PATHS=^/(api/v1/admin/|scim/v2/)' "$(cat "$env_file")"
  check_contains "the app port is told which page paths to send on" \
    'HAMLANEH_ADMIN_PAGE_PATHS=^/admin(/|$)' "$(cat "$env_file")"
  local moved_re page_re
  moved_re="$(sed -n 's/^HAMLANEH_ADMIN_MOVED_PATHS=//p' "$env_file" | tail -n 1)"
  page_re="$(sed -n 's/^HAMLANEH_ADMIN_PAGE_PATHS=//p' "$env_file" | tail -n 1)"
  local moved stays redirected
  for moved in /api/v1/admin/users /scim/v2/Users; do
    if grep -Eq "$moved_re" <<<"$moved"; then
      ok "the app port refuses ${moved} once the surface has moved"
    else
      bad "${moved} moved to the admin port but the app port would still serve it"
    fi
  done
  # server.go's sharedSurface: carried by BOTH listeners, so the app port must
  # keep them. The dashboard page is here too — it is redirected, never
  # refused. /api/v1/administrators is not a route at all; it is in the list
  # because a sloppier expression would swallow it along with anything else
  # that merely starts with the same letters.
  for stays in /admin /admin/audit /api/v1/auth/login /api/v1/instance /api/v1/users/me /assets/app.js /brand/logo.svg /api/v1/administrators; do
    if grep -Eq "$moved_re" <<<"$stays"; then
      bad "${stays} must not be part of the app port's refusal"
    else
      ok "the app port does not refuse ${stays}"
    fi
  done
  for redirected in /admin /admin/ /admin/audit; do
    if grep -Eq "$page_re" <<<"$redirected"; then
      ok "the app port sends ${redirected} on to the admin origin"
    else
      bad "${redirected} is the dashboard page but the app port would serve it itself"
    fi
  done
  # The page expression must not reach past the dashboard's own subtree.
  for stays in /administrators /api/v1/admin/users; do
    if grep -Eq "$page_re" <<<"$stays"; then
      bad "${stays} is not the dashboard page but would be redirected as one"
    else
      ok "the page redirect does not reach ${stays}"
    fi
  done
  # The link that makes the line above mean anything. The SINGLE dash is the
  # whole control: with ":-" an internet answer, written as an empty value,
  # would silently fall back to loopback — and worse, a missing line would not
  # fall back at all if the default were dropped, publishing to the world.
  # shellcheck disable=SC2016 # the needle is compose's literal text, not an expansion
  check_contains "compose publishes the admin port through that bind, loopback by default" \
    '"${HAMLANEH_ADMIN_BIND-127.0.0.1:}${HAMLANEH_ADMIN_PORT:-9443}:${HAMLANEH_ADMIN_PORT:-9443}"' \
    "$(cat "${SCRIPT_DIR}/docker-compose.yml")"

  # The internet answer writes an EMPTY line, not a missing one — deleting it
  # would mean loopback.
  rm -f "$env_file"
  # shellcheck disable=SC2016
  TEST_ENV_FILE="$env_file" run_in_subshell eval 'DOMAIN=203.0.113.5; ADMIN_PORT=9443; ADMIN_BIND=; write_env' >/dev/null
  check_eq "the internet answer writes an empty bind line, not no line" "1" \
    "$(grep -c '^HAMLANEH_ADMIN_BIND=$' "$env_file")"

  # An .env carrying an OLD expression is not "up to date" merely because its
  # port and bind still match — the same trap the SNI and issuer variables fell
  # into on a live install, where an unchanged domain meant a fix on disk never
  # reached the proxy.
  printf 'HAMLANEH_DOMAIN=203.0.113.5\nHAMLANEH_ADMIN_ADDR=:9090\nHAMLANEH_ADMIN_PORT=9443\nHAMLANEH_ADMIN_BIND=127.0.0.1:\nHAMLANEH_ADMIN_MOVED_PATHS=^/(admin|api/v1/admin/|scim/v2/)\n' > "$env_file"
  # shellcheck disable=SC2016
  out="$(TEST_ENV_FILE="$env_file" run_in_subshell eval 'DOMAIN=203.0.113.5; ADMIN_PORT=9443; ADMIN_BIND=127.0.0.1:; write_env')"
  check_contains "a stale admin expression is not mistaken for up to date" "updating the domain-derived lines" "$out"
  check_contains "and it is rewritten to the shipped one" \
    'HAMLANEH_ADMIN_MOVED_PATHS=^/(api/v1/admin/|scim/v2/)' "$(cat "$env_file")"
  check_eq "the rewritten expression is written once, not appended twice" "1" \
    "$(grep -c '^HAMLANEH_ADMIN_MOVED_PATHS=' "$env_file")"

  # Off writes nothing at all: absence is what "off" is.
  rm -f "$env_file"
  # shellcheck disable=SC2016
  TEST_ENV_FILE="$env_file" run_in_subshell eval 'DOMAIN=203.0.113.5; write_env' >/dev/null
  if grep -q '^HAMLANEH_ADMIN_ADDR=\|^HAMLANEH_ADMIN_PORT=\|^HAMLANEH_ADMIN_MOVED_PATHS=' "$env_file"; then
    bad "the split being off still wrote admin lines into .env"
  else
    ok "the split being off writes no admin lines at all"
  fi

  # And the closing notice tells the truth for each mode.
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'ADMIN_PORT=9443; ADMIN_BIND=127.0.0.1:; print_port_notice')"
  check_contains "the notice marks a loopback admin port as not-to-be-opened" \
    "9443/tcp    admin dashboard: this machine only, do NOT open it" "$out"
  check_eq "the admin row keeps the box square" \
    "$(grep '7882/udp' <<<"$out" | wc -c)" "$(grep 'admin dashboard' <<<"$out" | wc -c)"
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'ADMIN_PORT=9443; ADMIN_BIND=; print_port_notice')"
  check_contains "the notice tells an internet admin port to be opened" \
    "9443/tcp    admin dashboard: open this one too" "$out"
  out="$(run_in_subshell print_port_notice)"
  if grep -q 'admin dashboard' <<<"$out"; then
    bad "the notice named an admin port on an install that has none"
  else
    ok "the notice names no admin port when the split is off"
  fi
}

# The admin origin's allow-list in deploy/Caddyfile, stated here independently
# of that file — the same discipline expected_family_for uses above, and for
# the same reason: if somebody changes the list there, they have to change it
# here too, deliberately.
#
# It exists because this list has already drifted behind the server's
# adminListenerServes twice in one slice, and neither time did the symptom look
# like a proxy problem: the dashboard served a document whose boot calls went to
# the app origin, were refused cross-origin, and left a page that never
# finished loading. A path added to sharedSurface or adminSurface in
# server/internal/httpserver/server.go has to be added below in the same change.
EXPECTED_ADMIN_ALLOWLIST='/admin /admin/* /api/v1/admin/* /scim/v2/* /api/v1/instance /api/v1/users/me /api/v1/users/me/totp* /api/v1/auth/* /assets/* /brand/*'

test_caddy_admin_allowlist() {
  local line got
  line="$(grep -F '@adminsurface path ' "${SCRIPT_DIR}/Caddyfile" | head -n 1)"
  if [ -z "$line" ]; then
    bad "deploy/Caddyfile declares no @adminsurface matcher"
    return
  fi
  got="${line#*@adminsurface path }"
  check_eq "the admin origin serves exactly the paths the server's admin listener does" \
    "$EXPECTED_ADMIN_ALLOWLIST" "$got"

  # And the app site must still send the page on and refuse the API — the two
  # halves are not interchangeable, so neither handler may quietly become the
  # other.
  check_contains "the app site refuses the powered half" "@adminmoved path_regexp" \
    "$(cat "${SCRIPT_DIR}/Caddyfile")"
  # shellcheck disable=SC2016 # the needle is the Caddyfile's literal text
  check_contains "the app site redirects the page half to the admin origin" \
    'redir https://{$HAMLANEH_DEFAULT_SNI:localhost}:{$HAMLANEH_ADMIN_PORT:9443}{uri}' \
    "$(cat "${SCRIPT_DIR}/Caddyfile")"

  # "/" on the admin origin means the way IN, and the exit marker means the
  # way OUT. The first operator to open that port met the wrong one — a bare
  # host:port bounced them to the chat app — so both readings are pinned, in
  # the order Caddy needs them: the marked exit is matched before the bare
  # root, and the bare root before the catch-all that sends chat routes home.
  local admin_block order
  admin_block="$(sed -n '/^# The admin origin (ADR 015)/,/^}/p' "${SCRIPT_DIR}/Caddyfile")"
  check_contains "the exit control's marker is matched on the admin origin" \
    "query to=chat" "$admin_block"
  # shellcheck disable=SC2016 # the needle is the Caddyfile's literal text
  check_contains "a marked exit leaves for the app origin" \
    'redir https://{$HAMLANEH_DOMAIN:localhost}/' "$admin_block"
  check_contains "a bare admin origin lands on the dashboard" "redir * /admin" "$admin_block"

  # The `*` is not decoration: `redir /admin` parses as a path matcher with no
  # destination and Caddy refuses the whole file. Losing it is a stack that
  # will not boot, so the check above spells it and this one says why.
  order="$(printf '%s\n' "$admin_block" | grep -nE '@exit|@dashboard|^\s*handle \{' | cut -d: -f1 | tr '\n' ' ')"
  check_eq "exit, then dashboard, then the catch-all — in that order" \
    "$(printf '%s\n' "$order" | tr ' ' '\n' | grep -v '^$' | sort -n | tr '\n' ' ')" "$order"

  # And the exit control must actually set the marker, or the proxy's
  # distinction is a rule nothing exercises.
  check_contains "the dashboard's exit control carries the marker" 'href="/?to=chat"' \
    "$(cat "${SCRIPT_DIR}/../webapp/src/components/admin/AdminShell.tsx")"
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
  # HTTP/3 only where a browser will actually speak it — a trusted cert.
  check_contains "an IP install offers no HTTP/3" "HAMLANEH_HTTP_PROTOCOLS=h1 h2" "$(cat "$sni_env")"
  check_eq "http_protocols: ipv4 -> h1 h2" "h1 h2" "$(run_in_subshell http_protocols 203.0.113.5:8443)"
  check_eq "http_protocols: domain -> h1 h2 h3" "h1 h2 h3" "$(run_in_subshell http_protocols chat.example.com)"
  # shellcheck disable=SC2016
  TEST_ENV_FILE="$sni_env" run_in_subshell eval 'DOMAIN=chat.example.com; write_env' >/dev/null
  check_contains "a domain change rewrites the SNI host" "HAMLANEH_DEFAULT_SNI=chat.example.com" "$(cat "$sni_env")"
  check_contains "a domain change switches the issuer to ACME" "HAMLANEH_CERT_ISSUER=acme" "$(cat "$sni_env")"
  check_eq "the SNI line is written once, not appended twice" "1" "$(grep -c '^HAMLANEH_DEFAULT_SNI=' "$sni_env")"

  # The live-install regression: an .env from before these variables
  # existed, re-run with the SAME domain, must gain them — "unchanged
  # domain" is not "up to date". Secrets stay byte-identical.
  local old_env="${WORK}/sni/old.env" secret_before secret_after
  printf 'HAMLANEH_DOMAIN=203.0.113.5:8000\nHAMLANEH_FILES_DOMAIN=files.localhost\nHAMLANEH_HTTP_PORT=8080\nHAMLANEH_HTTPS_PORT=8000\nPOSTGRES_PASSWORD=keep-me-exactly\n' > "$old_env"
  secret_before="$(grep '^POSTGRES_PASSWORD=' "$old_env")"
  # shellcheck disable=SC2016
  TEST_ENV_FILE="$old_env" run_in_subshell eval 'DOMAIN=203.0.113.5:8000; HTTPS_PORT=8000; HTTP_PORT=8080; write_env' >/dev/null
  secret_after="$(grep '^POSTGRES_PASSWORD=' "$old_env")"
  check_contains "a same-domain re-run adds a missing SNI line" "HAMLANEH_DEFAULT_SNI=203.0.113.5" "$(cat "$old_env")"
  check_contains "a same-domain re-run adds a missing issuer line" "HAMLANEH_CERT_ISSUER=internal" "$(cat "$old_env")"
  check_contains "a same-domain re-run keeps the custom ports" "HAMLANEH_HTTPS_PORT=8000" "$(cat "$old_env")"
  check_eq "a same-domain re-run keeps every secret byte for byte" "$secret_before" "$secret_after"
  # And once complete, the next re-run really is a no-op.
  # shellcheck disable=SC2016
  out="$(TEST_ENV_FILE="$old_env" run_in_subshell eval 'DOMAIN=203.0.113.5:8000; HTTPS_PORT=8000; HTTP_PORT=8080; write_env')"
  check_contains "a complete .env is left untouched" "already up to date" "$out"
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
  local derived='^HAMLANEH_DOMAIN=\|^HAMLANEH_FILES_DOMAIN=\|^HAMLANEH_DEFAULT_SNI=\|^HAMLANEH_CERT_ISSUER=\|^HAMLANEH_HTTP_PROTOCOLS='
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

test_resolve_admin() {
  local out
  # Enter twice: the defaults — "admin", and a password generated later by
  # write_env, so nothing is chosen for the operator that they did not see.
  # shellcheck disable=SC2016
  out="$(printf '\n\n' | run_in_subshell eval 'NON_INTERACTIVE=0; resolve_admin; printf "RESULT %s [%s]" "$ADMIN_USERNAME" "$ADMIN_PASSWORD"')"
  check_contains "empty answers keep admin and defer to a generated password" "RESULT admin []" "$out"

  # A chosen pair is taken as given.
  # shellcheck disable=SC2016
  out="$(printf 'amir\ncorrect-horse-battery\n' | run_in_subshell eval 'NON_INTERACTIVE=0; resolve_admin; printf "RESULT %s [%s]" "$ADMIN_USERNAME" "$ADMIN_PASSWORD"')"
  check_contains "a chosen username and password are used" "RESULT amir [correct-horse-battery]" "$out"

  # Too short is re-asked, not accepted and then refused by the server.
  # shellcheck disable=SC2016
  out="$(printf '\nshort\nlong-enough-password\n' | run_in_subshell eval 'NON_INTERACTIVE=0; resolve_admin; printf "RESULT [%s]" "$ADMIN_PASSWORD"')"
  check_contains "a short password is re-asked" "at least 12 characters" "$out"
  check_contains "the retry is accepted" "RESULT [long-enough-password]" "$out"

  # A bad username is re-asked too.
  # shellcheck disable=SC2016
  out="$(printf 'bad name!\nok_name\n\n' | run_in_subshell eval 'NON_INTERACTIVE=0; resolve_admin; printf "RESULT %s" "$ADMIN_USERNAME"')"
  check_contains "an invalid username is re-asked" "RESULT ok_name" "$out"

  # Non-interactive never prompts, and a re-run never asks again.
  # shellcheck disable=SC2016
  out="$(run_in_subshell eval 'NON_INTERACTIVE=1; resolve_admin; printf "RESULT %s" "$ADMIN_USERNAME"')"
  check_contains "non-interactive keeps the default admin" "RESULT admin" "$out"
  local env_fixture="${WORK}/admin/.env"
  mkdir -p "${WORK}/admin"
  printf 'HAMLANEH_ADMIN_USERNAME=existing\n' > "$env_fixture"
  # shellcheck disable=SC2016
  out="$(printf 'ignored\nignored-password-1\n' | TEST_ENV_FILE="$env_fixture" run_in_subshell eval 'NON_INTERACTIVE=0; resolve_admin; printf "RESULT %s" "$ADMIN_USERNAME"')"
  check_contains "a re-run with an existing admin does not ask" "RESULT admin" "$out"

  # And the chosen pair reaches the file write_env produces.
  local fresh="${WORK}/admin/fresh.env"
  # shellcheck disable=SC2016
  TEST_ENV_FILE="$fresh" run_in_subshell eval 'DOMAIN=localhost; ADMIN_USERNAME=amir; ADMIN_PASSWORD=correct-horse-battery; write_env' >/dev/null
  check_contains "write_env records the chosen username" "HAMLANEH_ADMIN_USERNAME=amir" "$(cat "$fresh")"
  check_contains "write_env records the chosen password" "HAMLANEH_ADMIN_PASSWORD=correct-horse-battery" "$(cat "$fresh")"
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
  test_resolve_admin_port
  test_caddy_admin_allowlist
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
  test_resolve_admin
  test_help_output
  finish
}

main "$@"
