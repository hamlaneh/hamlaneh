#!/usr/bin/env bash
#
# Tests for deploy/hamlaneh-update.sh — the "auto-update applies a signed
# release" half of ROADMAP.md's Phase 4 test gate item 2.
#
# deploy/verify-release.test.sh proves the verifier refuses the right things.
# This file proves the UPDATER does not apply what the verifier refused, and
# that when it does apply something it does so atomically and can undo it:
#
#   a signed release is applied         -> "clean upgrade is applied"
#   a tampered release is not applied   -> "tampered artifact", "SHA256SUMS tampered"
#   an unsigned release is not applied  -> "signature bundle removed"
#   an older release is not applied     -> "rollback refused ...", exit 3
#   ...unless forced                    -> "rollback applied with --force", exit 0
#   an unhealthy new version is undone  -> "... rolled back", exit 4
#
# Every negative asserts BOTH the exit code and the version that is installed
# afterwards. A refusal that still swapped the binary would pass an exit-code
# check on its own, and that is the exact failure this file exists to catch.
#
# What is real and what is not:
#
#   REAL  cosign, its signatures over the release directories built here, and
#         verify-release.sh checking them. A tampered byte is refused because
#         cosign and sha256sum say so.
#   REAL  every branch of hamlaneh-update.sh: the channel gate, the exit-code
#         handoff, the rename(2) swap, the health probe, both rollbacks.
#   REAL  the home-mode swap, end to end, against an executable that is
#         swapped on disk and asked afterwards who it is.
#   NOT   the network. Releases are applied with --from-dir, so curl, the
#         GitHub release API and the download URLs are not exercised. The
#         bytes those steps produce are exactly what --from-dir is handed.
#   NOT   docker. Compose mode drives a stub that models the three things the
#         swap depends on — a tag is a movable pointer, a recreate makes the
#         container serve whatever the tag points at, and the container can
#         be asked its version — so what is tested there is the ORDER of the
#         operations and the rollback, not docker itself.
#   NOT   the keyless identity, for the same reason verify-release.test.sh
#         cannot test it: Fulcio and Rekor need a real tag. See
#         docs/releasing.md.
#
# Requires cosign on PATH, or $COSIGN_BIN. It does not skip when cosign is
# absent: a green run against a missing tool would be theatre.
#
# Usage: bash deploy/hamlaneh-update.test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UPDATE="$SCRIPT_DIR/hamlaneh-update.sh"
COSIGN="${COSIGN_BIN:-cosign}"

if ! command -v "$COSIGN" >/dev/null 2>&1; then
  printf 'ERROR: cosign not found (set COSIGN_BIN to override).\n' >&2
  printf 'These tests verify real signatures; there is nothing to run without it.\n' >&2
  exit 2
fi
COSIGN_REAL="$(command -v "$COSIGN")"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Keep this run off the host's real lock, and off its real TMPDIR. Both are
# needed: HAMLANEH_UPDATE_LOCK_DIR is where the updater locks, TMPDIR is where
# its mktemp scratch goes.
export TMPDIR="$WORK"
export HAMLANEH_UPDATE_LOCK_DIR="$WORK/hamlaneh-update.lock"

failures=0
checks=0

pass_check() { printf 'PASS  %s\n' "$1"; }

fail_check() {
  printf 'FAIL  %s\n' "$1"
  shift
  printf '%s\n' "$@" | sed 's/^/      | /'
  failures=$((failures + 1))
}

# check <name> <expected exit> <expected output substring> <command...>
#
# The substring is not decoration: every negative here can also be made to
# exit non-zero by breaking something unrelated, and a test that only asserts
# "it failed" goes green for the wrong reason and stays green while the
# control it names quietly stops working.
check() {
  local name="$1" want="$2" want_msg="$3"
  shift 3
  local out rc=0
  checks=$((checks + 1))
  out="$("$@" 2>&1)" || rc=$?
  if [ "$rc" -ne "$want" ]; then
    fail_check "$name — expected exit $want, got $rc" "$out"
    return
  fi
  if ! grep -qF -- "$want_msg" <<<"$out"; then
    fail_check "$name — exit $rc was right, but for the wrong reason (wanted: $want_msg)" "$out"
    return
  fi
  pass_check "$name"
}

# assert_installed <expected version> <name> — what the binary on disk says it
# is, after the run. This is the assertion the exit code cannot make.
assert_installed() {
  local want="$1" name="$2" got
  checks=$((checks + 1))
  got="$("$HOME_BIN" --version 2>/dev/null | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -n 1)" || got=""
  if [ "$got" = "$want" ]; then
    pass_check "$name (installed: $got)"
  else
    fail_check "$name — expected $want installed, found '${got:-nothing}'"
  fi
}

# assert_file <path> <expected content> <name>
assert_file() {
  local path="$1" want="$2" name="$3" got=""
  checks=$((checks + 1))
  [ -f "$path" ] && got="$(cat "$path")"
  if [ "$got" = "$want" ]; then
    pass_check "$name"
  else
    fail_check "$name — expected '$want', found '${got:-nothing}'"
  fi
}

# --- signing keys -----------------------------------------------------------
# Throwaway, per run, never leaves $WORK; --tlog-upload=false keeps every
# signature off the public transparency log.

mkdir -p "$WORK/keys"
(cd "$WORK/keys" && COSIGN_PASSWORD='' "$COSIGN" generate-key-pair >/dev/null 2>&1)
KEY="$WORK/keys/cosign.key"
PUB="$WORK/keys/cosign.pub"

# A cosign that forwards everything to the real binary except image
# verification, which has no registry to reach here. verify-blob — the check
# that actually decides whether a release is genuine — is REAL.
COSIGN_WRAP="$WORK/cosign-wrap"
cat >"$COSIGN_WRAP" <<EOF
#!/usr/bin/env bash
if [ "\${1:-}" = "verify" ]; then
  [ -f "\${COSIGN_IMAGE_BAD:-/nonexistent}" ] && exit 1
  exit 0
fi
exec "$COSIGN_REAL" "\$@"
EOF
chmod +x "$COSIGN_WRAP"

# --- release fixtures -------------------------------------------------------

ASSET_ARCH="linux-amd64"

# fake_server <path> <version> <health exit code> — stands in for the real
# hamlaneh-server. It implements the two things the updater asks of it: the
# --version contract documented above probe_version in hamlaneh-update.sh, and
# the healthcheck subcommand cmd/hamlaneh-server already ships.
fake_server() {
  local path="$1" v="$2" health="$3"
  cat >"$path" <<EOF
#!/bin/sh
case "\${1:-}" in
  --version) echo "hamlaneh-server ${v}"; exit 0 ;;
  healthcheck) exit ${health} ;;
esac
echo "hamlaneh-server ${v}: unknown command \${1:-}" >&2
exit 2
EOF
  chmod +x "$path"
}

# make_release <version> [health exit code] [binary version override]
# A release directory shaped exactly like the one release.yml uploads.
make_release() {
  local v="$1" health="${2:-0}" bin_v="${3:-$1}"
  local d="$WORK/rel-$v-$health-$bin_v"
  rm -rf "$d" "$WORK/pack"
  mkdir -p "$d" "$WORK/pack"

  fake_server "$WORK/pack/hamlaneh-server" "$bin_v" "$health"
  tar -czf "$d/hamlaneh-${v}-${ASSET_ARCH}.tar.gz" -C "$WORK/pack" hamlaneh-server
  printf '{"spdxVersion":"SPDX-2.3","name":"hamlaneh-%s"}\n' "$v" >"$d/hamlaneh-${v}.spdx.json"

  (cd "$d" && LC_ALL=C sha256sum -- *) >"$WORK/sums.tmp"
  mv "$WORK/sums.tmp" "$d/SHA256SUMS"
  COSIGN_PASSWORD='' "$COSIGN" sign-blob --yes \
    --key "$KEY" \
    --bundle "$d/SHA256SUMS.sigstore.json" \
    --use-signing-config=false \
    --tlog-upload=false \
    "$d/SHA256SUMS" >/dev/null 2>&1
  printf '%s' "$d"
}

clone_release() {
  local src="$1" dst
  dst="$WORK/$(basename "$src")-$2"
  rm -rf "$dst"
  cp -r "$src" "$dst"
  printf '%s' "$dst"
}

# --- home mode --------------------------------------------------------------

HOME_DIR="$WORK/home"
HOME_BIN="$HOME_DIR/hamlaneh-server"
mkdir -p "$HOME_DIR"

# install_home <version> — the state of the machine before an update.
install_home() {
  rm -f "$HOME_BIN" "$HOME_BIN.previous"
  fake_server "$HOME_BIN" "$1" 0
}

# home_update <release dir> <version> [extra args...]
home_update() {
  local dir="$1" v="$2"
  shift 2
  bash "$UPDATE" \
    --mode home \
    --binary "$HOME_BIN" \
    --from-dir "$dir" \
    --version "$v" \
    --asset "hamlaneh-${v}-${ASSET_ARCH}.tar.gz" \
    --key "$PUB" \
    --cosign "$COSIGN_WRAP" \
    --restart-command true \
    --health-timeout 4 \
    "$@"
}

printf 'Testing hamlaneh-update.sh with %s\n\n' \
  "$("$COSIGN" version 2>/dev/null | sed -n 's/^GitVersion: *//p')"

REL_140="$(make_release v1.4.0)"
REL_141="$(make_release v1.4.1)"
REL_150="$(make_release v1.5.0)"
REL_141_SICK="$(make_release v1.4.1 1)"

# --- the happy path: a signed release is applied ----------------------------

install_home v1.4.0
check "clean upgrade is applied" 0 "updated to v1.4.1" \
  home_update "$REL_141" v1.4.1
assert_installed v1.4.1 "clean upgrade actually swapped the binary"

install_home v1.4.1
check "the installed version is not re-applied" 0 "already on v1.4.1 — nothing to do" \
  home_update "$REL_141" v1.4.1

install_home v1.4.0
check "--check reports and changes nothing" 0 "would apply v1.4.1 over v1.4.0" \
  home_update "$REL_141" v1.4.1 --check
assert_installed v1.4.0 "--check left the installed binary alone"

# --- a tampered release is not applied --------------------------------------

TAMPERED="$(clone_release "$REL_141" tampered)"
printf 'malicious payload\n' >>"$TAMPERED/hamlaneh-v1.4.1-${ASSET_ARCH}.tar.gz"
install_home v1.4.0
check "tampered artifact is not applied" 1 "does not match its signed checksum" \
  home_update "$TAMPERED" v1.4.1
assert_installed v1.4.0 "tampered artifact left the installed binary alone"

SUMS_TAMPERED="$(clone_release "$REL_141" sums-tampered)"
printf 'malicious payload\n' >>"$SUMS_TAMPERED/hamlaneh-v1.4.1-${ASSET_ARCH}.tar.gz"
(cd "$SUMS_TAMPERED" && LC_ALL=C sha256sum -- \
  "hamlaneh-v1.4.1-${ASSET_ARCH}.tar.gz" hamlaneh-v1.4.1.spdx.json) >"$WORK/sums.tmp"
mv "$WORK/sums.tmp" "$SUMS_TAMPERED/SHA256SUMS"
install_home v1.4.0
check "SHA256SUMS tampered — signature no longer valid, nothing applied" 1 \
  "cosign could not verify SHA256SUMS" \
  home_update "$SUMS_TAMPERED" v1.4.1
assert_installed v1.4.0 "re-checksummed tamper left the installed binary alone"

UNSIGNED="$(clone_release "$REL_141" unsigned)"
rm -f "$UNSIGNED/SHA256SUMS.sigstore.json"
install_home v1.4.0
check "signature bundle removed — nothing applied" 1 "this release is unsigned" \
  home_update "$UNSIGNED" v1.4.1
assert_installed v1.4.0 "unsigned release left the installed binary alone"

# A validly signed release whose binary is not the one the tag names. Every
# checksum matches; the payload still is not v1.4.1.
WRONG_BIN="$(make_release v1.4.1 0 v0.9.0)"
install_home v1.4.0
check "signed release carrying the wrong binary is not applied" 1 \
  "reports v0.9.0, not v1.4.1" \
  home_update "$WRONG_BIN" v1.4.1
assert_installed v1.4.0 "wrong-binary release left the installed binary alone"

# --- an older validly-signed release is refused -----------------------------
#
# Nothing about the crypto is wrong in any of these. The refusal is the point:
# an old release of ours is exactly what an attacker replays to walk a patched
# instance back onto a fixed vulnerability. verify-release.sh owns the
# decision; what is tested here is that the updater HONOURS it.

install_home v1.4.1
check "rollback refused — v1.4.0 offered to a v1.4.1 instance" 3 \
  "refused v1.4.0 as a downgrade from v1.4.1" \
  home_update "$REL_140" v1.4.0
assert_installed v1.4.1 "refused rollback left the installed binary alone"

install_home v1.4.1
check "rollback applied with --force" 0 "updated to v1.4.0" \
  home_update "$REL_140" v1.4.0 --force
assert_installed v1.4.0 "forced rollback actually swapped the binary"

# Across a series boundary, so --channel all is needed to even reach the
# verifier — and the verifier still refuses.
install_home v1.5.0
check "cross-series rollback refused" 3 \
  "refused v1.4.0 as a downgrade from v1.5.0" \
  home_update "$REL_140" v1.4.0 --channel all
assert_installed v1.5.0 "refused cross-series rollback left the installed binary alone"

# --- an unhealthy new version is rolled back --------------------------------

install_home v1.4.0
check "a new version that will not come up healthy is rolled back" 4 \
  "ROLLED BACK: v1.4.1 did not come up healthy" \
  home_update "$REL_141_SICK" v1.4.1
assert_installed v1.4.0 "rollback restored the previous binary"

# A restart command that fails must not abort the run before the health check
# has had its say — that is precisely the case where the rollback is needed.
install_home v1.4.0
check "a failing restart still reaches the rollback" 4 \
  "ROLLED BACK: v1.4.1 did not come up healthy" \
  home_update "$REL_141_SICK" v1.4.1 --restart-command false
assert_installed v1.4.0 "a failing restart rolled back like any other failure"

# --- the channel, on by default ---------------------------------------------

install_home v1.4.0
check "a minor release is outside the default security channel" 0 \
  "outside the security channel" \
  home_update "$REL_150" v1.5.0
assert_installed v1.4.0 "the security channel did not apply the minor release"

install_home v1.4.0
check "--channel all applies the minor release" 0 "updated to v1.5.0" \
  home_update "$REL_150" v1.5.0 --channel all
assert_installed v1.5.0 "--channel all actually swapped the binary"

# --- the --version contract this script depends on --------------------------
#
# hamlaneh-server --version is what tells the updater what is installed, and
# the installed version is the ONLY input to the anti-rollback check. If that
# flag ever disappears or changes shape, this updater has to stop, loudly —
# not carry on with a guess, which would leave every run reporting success
# while the control this gate is about silently stopped running.

cat >"$HOME_BIN" <<'EOF'
#!/bin/sh
echo "hamlaneh-server: unknown flag $1" >&2
exit 2
EOF
chmod +x "$HOME_BIN"
check "a server without --version stops the updater" 1 \
  "cannot determine what is installed" \
  home_update "$REL_141" v1.4.1

cat >"$HOME_BIN" <<'EOF'
#!/bin/sh
case "${1:-}" in --version) echo "hamlaneh-server (dev build)"; exit 0 ;; esac
exit 0
EOF
chmod +x "$HOME_BIN"
check "a server whose --version prints no version stops the updater" 1 \
  "printed no vX.Y.Z version" \
  home_update "$REL_141" v1.4.1

# --- compose mode -----------------------------------------------------------
#
# The stub below models exactly three properties of docker that the swap
# depends on, and nothing else: a tag is a movable pointer to an image, a
# recreate makes the container serve whatever the tag pointed at when it ran,
# and the container can be asked its version. What is under test is the ORDER
# — nothing is retagged before the verifier and cosign have both passed — and
# that the rollback puts the pointer back.

STATE="$WORK/docker-state"
DOCKER_STUB="$WORK/docker-stub"
cat >"$DOCKER_STUB" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
S="$DOCKER_STUB_STATE"
printf '%s\n' "$*" >>"$S/log"
slug() { printf '%s' "${1//[^A-Za-z0-9]/_}"; }

if [ "${1:-}" = "compose" ]; then
  shift
  while [ $# -gt 0 ]; do
    case "${1:-}" in
      -f | --env-file | -p) shift 2 ;;
      *) break ;;
    esac
  done
  case "${1:-}" in
    ps) cat "$S/container_id"; exit 0 ;;
    exec)
      shift
      while [ "${1:-}" = "-T" ]; do shift; done
      shift 2 # service name, then the binary path
      case "${1:-}" in
        --version) printf 'hamlaneh-server %s\n' "$(cat "$S/current_version")"; exit 0 ;;
        healthcheck) exit "$(cat "$S/current_health")" ;;
      esac
      exit 2
      ;;
    up)
      src="$(slug "$(cat "$S/tagged")")"
      cp "$S/img.$src.version" "$S/current_version"
      cp "$S/img.$src.health" "$S/current_health"
      cat "$S/tagged" >"$S/current_image_id"
      exit 0
      ;;
  esac
  exit 2
fi

case "${1:-}" in
  pull) exit 0 ;;
  tag) printf '%s' "$2" >"$S/tagged"; exit 0 ;;
  inspect)
    case "$3" in
      *Config.Image*) cat "$S/local_tag" ;;
      *) cat "$S/current_image_id" ;;
    esac
    exit 0
    ;;
  image) printf '%s@sha256:%064d\n' "$5" 1; exit 0 ;;
esac
exit 2
STUB
chmod +x "$DOCKER_STUB"
export DOCKER_STUB_STATE="$STATE"

PREVIOUS_ID="sha256:previous"
NEW_IMAGE="ghcr.io/hamlaneh/hamlaneh/server:v1.4.1"

# seed_compose <health of the new image>
seed_compose() {
  rm -rf "$STATE"
  mkdir -p "$STATE"
  printf 'fakecid' >"$STATE/container_id"
  printf 'hamlaneh-server' >"$STATE/local_tag"
  printf '%s' "$PREVIOUS_ID" >"$STATE/tagged"
  printf '%s' "$PREVIOUS_ID" >"$STATE/current_image_id"
  printf 'v1.4.0' >"$STATE/img.sha256_previous.version"
  printf '0' >"$STATE/img.sha256_previous.health"
  printf 'v1.4.1' >"$STATE/img.${NEW_IMAGE//[^A-Za-z0-9]/_}.version"
  printf '%s' "$1" >"$STATE/img.${NEW_IMAGE//[^A-Za-z0-9]/_}.health"
  printf 'v1.4.0' >"$STATE/current_version"
  printf '0' >"$STATE/current_health"
  : >"$STATE/log"
}

COMPOSE_ENV="$WORK/compose.env"
: >"$COMPOSE_ENV"

# compose_update <release dir> <version> [extra args...] — no --key: compose
# mode refuses it, because the image can only be checked keyless.
compose_update() {
  local dir="$1" v="$2"
  shift 2
  bash "$UPDATE" \
    --mode compose \
    --compose-file "$SCRIPT_DIR/docker-compose.yml" \
    --env-file "$COMPOSE_ENV" \
    --docker "$DOCKER_STUB" \
    --cosign "$COSIGN_WRAP" \
    --from-dir "$dir" \
    --version "$v" \
    --health-timeout 4 \
    "$@"
}

# The release fixtures are key-signed, so verify-release.sh needs the public
# key — which compose mode refuses. These compose cases therefore pass
# --installed and stub the blob check through the same wrapper, keeping the
# subject under test the SWAP rather than the signature (which the home cases
# above already exercise against real cosign).
COSIGN_COMPOSE="$WORK/cosign-compose"
cat >"$COSIGN_COMPOSE" <<EOF
#!/usr/bin/env bash
case "\${1:-}" in
  verify-blob) exit 0 ;;
  verify) [ -f "\${COSIGN_IMAGE_BAD:-/nonexistent}" ] && exit 1; exit 0 ;;
esac
exec "$COSIGN_REAL" "\$@"
EOF
chmod +x "$COSIGN_COMPOSE"

seed_compose 0
check "compose: a signed release is applied" 0 "updated to v1.4.1" \
  compose_update "$REL_141" v1.4.1 --cosign "$COSIGN_COMPOSE"
assert_file "$STATE/tagged" "$NEW_IMAGE" "compose: the tag points at the new image"
assert_file "$STATE/current_version" "v1.4.1" "compose: the container serves the new version"

seed_compose 1
check "compose: an unhealthy new image is rolled back" 4 \
  "ROLLED BACK: v1.4.1 did not come up healthy" \
  compose_update "$REL_141_SICK" v1.4.1 --cosign "$COSIGN_COMPOSE"
assert_file "$STATE/tagged" "$PREVIOUS_ID" "compose: the tag was put back"
assert_file "$STATE/current_version" "v1.4.0" "compose: the previous version is serving again"

seed_compose 0
COSIGN_IMAGE_BAD="$WORK/image-bad"
: >"$COSIGN_IMAGE_BAD"
export COSIGN_IMAGE_BAD
check "compose: an image the signature does not cover is not applied" 1 \
  "cosign could not verify" \
  compose_update "$REL_141" v1.4.1 --cosign "$COSIGN_COMPOSE"
assert_file "$STATE/tagged" "$PREVIOUS_ID" "compose: an unverified image is never retagged"
unset COSIGN_IMAGE_BAD

seed_compose 0
check "compose: a refused downgrade never reaches docker" 3 \
  "refused v1.4.0 as a downgrade from v1.4.1" \
  compose_update "$REL_140" v1.4.0 --installed v1.4.1 --cosign "$COSIGN_COMPOSE"
assert_file "$STATE/tagged" "$PREVIOUS_ID" "compose: a refused downgrade never retagged anything"

# --- usage errors are not silent partial verification -----------------------

check "compose mode refuses --key, which cannot cover the image" 2 \
  "Refusing to apply a compose update with half of it verified" \
  compose_update "$REL_141" v1.4.1 --key "$PUB"

check "an unknown channel is a usage error" 2 "--channel must be" \
  home_update "$REL_141" v1.4.1 --channel weekly

install_home v1.4.0
check "a malformed --version is refused before anything is fetched" 1 \
  "is not a semantic version" \
  home_update "$REL_141" "latest"
assert_installed v1.4.0 "a malformed --version changed nothing"

# --- one updater at a time --------------------------------------------------

# The lock excludes, and it excludes BEFORE anything is fetched or swapped.
install_home v1.4.0
mkdir "$HAMLANEH_UPDATE_LOCK_DIR"
check "a second updater refuses while the lock is held" 1 \
  "another update is already running" \
  home_update "$REL_141" v1.4.1
assert_installed v1.4.0 "a locked-out run swapped nothing"
rmdir "$HAMLANEH_UPDATE_LOCK_DIR"

# And it has to exclude across the case it is named for: a timer firing while
# an operator runs the script by hand. Those two do not share a /tmp — the unit
# hamlaneh-update.sh itself writes sets PrivateTmp=true — so a lock under
# $TMPDIR is a different directory for each of them, both mkdir calls succeed,
# and both processes swap the same binary. That is what this asserts, and it
# has to assert it against the source: seeing the timer's private /tmp would
# mean booting systemd, which this suite does not do. Two settings written by
# one file agreeing with each other is the whole invariant.
checks=$((checks + 1))
if grep -q '^PrivateTmp=true' "$UPDATE" &&
  ! grep -q 'LOCK_DIR="/run/hamlaneh-update.lock"' "$UPDATE"; then
  fail_check "the unit sets PrivateTmp=true but the lock does not prefer /run" \
    "A lock under TMPDIR is invisible across that boundary, so the timer and an" \
    "operator would each take their own and both would swap the binary."
else
  pass_check "the lock lives outside the /tmp the unit's PrivateTmp namespaces"
fi

# ---------------------------------------------------------------------------

printf '\n%d checks, %d failures\n' "$checks" "$failures"
[ "$failures" -eq 0 ]
