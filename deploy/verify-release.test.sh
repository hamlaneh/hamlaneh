#!/usr/bin/env bash
#
# Tests for deploy/verify-release.sh — ROADMAP.md Phase 4 test gate item 2.
#
# The gate names three negatives, and all three are exercised here against a
# REAL cosign binary signing and verifying REAL signatures:
#
#   a tampered release is rejected     -> "artifact tampered", "SHA256SUMS tampered"
#   an unsigned release is rejected    -> "signature bundle removed", "signed by the wrong key"
#   an older release is refused        -> "rollback refused", and accepted with --force
#
# What is real and what is not, stated here rather than left to be assumed:
#
#   REAL  cosign itself, the ECDSA signatures it produces, and its verification
#         of them. A tampered byte fails because cosign says so, not because a
#         stub said so.
#   REAL  every line of verify-release.sh: argument handling, the semver
#         ordering, checksum coverage, SBOM presence, exit codes.
#   NOT   the keyless half. These signatures are made with a locally generated
#         key pair and --tlog-upload=false, so nothing here contacts Fulcio or
#         Rekor and nothing is written to the public transparency log. That
#         means --certificate-identity-regexp and --certificate-oidc-issuer —
#         the flags that decide WHOSE signature counts in production — are not
#         exercised. Proving those needs a real tag pushed to a real
#         repository; see docs/releasing.md.
#
# Requires cosign on PATH, or $COSIGN_BIN. It does not skip when cosign is
# absent: a green run against a missing tool would be the exact theatre this
# file exists to avoid.
#
# Usage: bash deploy/verify-release.test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERIFY="$SCRIPT_DIR/verify-release.sh"
COSIGN="${COSIGN_BIN:-cosign}"

if ! command -v "$COSIGN" >/dev/null 2>&1; then
  printf 'ERROR: cosign not found (set COSIGN_BIN to override).\n' >&2
  printf 'These tests verify real signatures; there is nothing to run without it.\n' >&2
  exit 2
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

failures=0
checks=0

# check <name> <expected exit> <expected output substring> <command...>
#
# The substring is not decoration. Every negative below can also be made to
# exit non-zero by breaking something unrelated, and a test that only asserts
# "it failed" would go green for the wrong reason and stay green while the
# control it names quietly stops working.
check() {
  local name="$1" want="$2" want_msg="$3"
  shift 3
  local out rc=0
  checks=$((checks + 1))
  out="$("$@" 2>&1)" || rc=$?
  if [ "$rc" -ne "$want" ]; then
    printf 'FAIL  %s — expected exit %d, got %d\n' "$name" "$want" "$rc"
    printf '%s\n' "$out" | sed 's/^/      | /'
    failures=$((failures + 1))
    return
  fi
  if ! grep -qF -- "$want_msg" <<<"$out"; then
    printf 'FAIL  %s — exit %d was right, but for the wrong reason\n' "$name" "$rc"
    printf '      expected to see: %s\n' "$want_msg"
    printf '%s\n' "$out" | sed 's/^/      | /'
    failures=$((failures + 1))
    return
  fi
  printf 'PASS  %s\n' "$name"
}

# --- signing keys -----------------------------------------------------------
# Throwaway, generated per run, never leaves $WORK. --tlog-upload=false below
# keeps every signature off the public transparency log.

mkdir -p "$WORK/keys" "$WORK/otherkeys"
(cd "$WORK/keys" && COSIGN_PASSWORD='' "$COSIGN" generate-key-pair >/dev/null 2>&1)
(cd "$WORK/otherkeys" && COSIGN_PASSWORD='' "$COSIGN" generate-key-pair >/dev/null 2>&1)
KEY="$WORK/keys/cosign.key"
PUB="$WORK/keys/cosign.pub"

# sign_sums <dir> [key]
sign_sums() {
  local d="$1" k="${2:-$KEY}"
  COSIGN_PASSWORD='' "$COSIGN" sign-blob --yes \
    --key "$k" \
    --bundle "$d/SHA256SUMS.sigstore.json" \
    --use-signing-config=false \
    --tlog-upload=false \
    "$d/SHA256SUMS" >/dev/null 2>&1
}

# write_sums <dir> — rebuilds SHA256SUMS from whatever the directory holds.
write_sums() {
  local d="$1"
  rm -f "$d/SHA256SUMS" "$d/SHA256SUMS.sigstore.json"
  (cd "$d" && LC_ALL=C sha256sum -- *) >"$WORK/sums.tmp"
  mv "$WORK/sums.tmp" "$d/SHA256SUMS"
}

# make_release <version> [--no-sbom] — a release directory shaped exactly like
# the one .github/workflows/release.yml uploads. Echoes the directory.
make_release() {
  local v="$1" flag="${2:-}"
  local d="$WORK/rel-$v"
  rm -rf "$d"
  mkdir -p "$d"
  printf 'not really a binary, but a distinct one per version: %s\n' "$v" \
    >"$d/hamlaneh-${v}-linux-amd64.tar.gz"
  if [ "$flag" != "--no-sbom" ]; then
    printf '{"spdxVersion":"SPDX-2.3","name":"hamlaneh-%s"}\n' "$v" \
      >"$d/hamlaneh-${v}.spdx.json"
  fi
  write_sums "$d"
  sign_sums "$d"
  printf '%s' "$d"
}

# clone_release <dir> <suffix> — a scratch copy to damage.
clone_release() {
  local src="$1"
  local dst
  dst="$WORK/$(basename "$src")-$2"
  rm -rf "$dst"
  cp -r "$src" "$dst"
  printf '%s' "$dst"
}

# verify <dir> <version> [extra args...] — always in --key mode, because these
# signatures are key-signed.
verify() {
  local d="$1" v="$2"
  shift 2
  bash "$VERIFY" --version "$v" --dir "$d" --key "$PUB" --cosign "$COSIGN" "$@"
}

printf 'Building test releases with %s\n\n' "$("$COSIGN" version 2>/dev/null | sed -n 's/^GitVersion: *//p')"

REL="$(make_release v1.2.3)"
REL_OLD="$(make_release v1.1.0)"
REL_RC="$(make_release v1.2.3-rc.1)"
REL_1_9="$(make_release v1.9.0)"
REL_1_10="$(make_release v1.10.0)"

# --- the happy path ---------------------------------------------------------

check "clean release verifies" 0 "Hamlaneh v1.2.3 verified." \
  verify "$REL" v1.2.3

check "clean release verifies as an upgrade" 0 "newer than the installed v1.1.0" \
  verify "$REL" v1.2.3 --installed v1.1.0

check "reinstalling the installed version is not a rollback" 0 "is the installed version" \
  verify "$REL" v1.2.3 --installed v1.2.3

# --- negative 1: a tampered release is rejected -----------------------------

TAMPERED="$(clone_release "$REL" tampered)"
printf 'malicious payload\n' >>"$TAMPERED/hamlaneh-v1.2.3-linux-amd64.tar.gz"
check "artifact tampered — checksum no longer matches" 1 \
  "does not match its signed checksum" \
  verify "$TAMPERED" v1.2.3

# Re-checksumming the tampered artifact without re-signing: the checksums now
# agree with the bytes, and the signature no longer agrees with the checksums.
SUMS_TAMPERED="$(clone_release "$REL" sums-tampered)"
printf 'malicious payload\n' >>"$SUMS_TAMPERED/hamlaneh-v1.2.3-linux-amd64.tar.gz"
(cd "$SUMS_TAMPERED" && LC_ALL=C sha256sum -- \
  hamlaneh-v1.2.3-linux-amd64.tar.gz hamlaneh-v1.2.3.spdx.json) >"$WORK/sums.tmp"
mv "$WORK/sums.tmp" "$SUMS_TAMPERED/SHA256SUMS"
check "SHA256SUMS tampered — cosign signature no longer valid" 1 \
  "cosign could not verify SHA256SUMS" \
  verify "$SUMS_TAMPERED" v1.2.3

# An extra artifact smuggled in and left out of the checksums file. `sha256sum
# -c` alone would never look at it.
INJECTED="$(clone_release "$REL" injected)"
printf 'a file nobody signed\n' >"$INJECTED/hamlaneh-v1.2.3-linux-arm64.tar.gz"
check "unlisted artifact injected — not covered by the signature" 1 \
  "not named by the signed SHA256SUMS: hamlaneh-v1.2.3-linux-arm64.tar.gz" \
  verify "$INJECTED" v1.2.3

# --- negative 2: an unsigned release is rejected ----------------------------

UNSIGNED="$(clone_release "$REL" unsigned)"
rm -f "$UNSIGNED/SHA256SUMS.sigstore.json"
check "signature bundle removed — release is unsigned" 1 \
  "this release is unsigned" \
  verify "$UNSIGNED" v1.2.3

WRONGKEY="$(clone_release "$REL" wrongkey)"
sign_sums "$WRONGKEY" "$WORK/otherkeys/cosign.key"
check "signed by the wrong key — signature does not verify" 1 \
  "cosign could not verify SHA256SUMS" \
  verify "$WRONGKEY" v1.2.3

# --- negative 3: an older validly-signed release is refused -----------------
#
# Every release below is correctly signed. Nothing about the crypto is wrong.
# The refusal is the point: an old release of ours is exactly what an attacker
# replays to walk a patched instance back onto a fixed vulnerability.

check "rollback refused — v1.1.0 offered to a v1.2.3 instance" 3 \
  "v1.1.0 is older than the installed v1.2.3" \
  verify "$REL_OLD" v1.1.0 --installed v1.2.3

check "rollback accepted with --force" 0 \
  "applying anyway because --force was given" \
  verify "$REL_OLD" v1.1.0 --installed v1.2.3 --force

# 1.10 > 1.9 numerically and "1.10" < "1.9" as strings. A string comparison
# here would wave a rollback through.
check "1.10.0 over 1.9.0 is an upgrade, not a string comparison" 0 \
  "v1.10.0 is newer than the installed v1.9.0" \
  verify "$REL_1_10" v1.10.0 --installed v1.9.0

check "1.9.0 offered to a 1.10.0 instance is a rollback" 3 \
  "v1.9.0 is older than the installed v1.10.0" \
  verify "$REL_1_9" v1.9.0 --installed v1.10.0

# `sort -V` places 1.2.3-rc.1 ABOVE 1.2.3 and would call this an upgrade.
check "a pre-release of the installed version is a rollback" 3 \
  "v1.2.3-rc.1 is older than the installed v1.2.3" \
  verify "$REL_RC" v1.2.3-rc.1 --installed v1.2.3

check "the release after its own release candidate is an upgrade" 0 \
  "v1.2.3 is newer than the installed v1.2.3-rc.1" \
  verify "$REL" v1.2.3 --installed v1.2.3-rc.1

# --- the SBOM has to actually ship ------------------------------------------

NO_SBOM="$(make_release v1.3.0 --no-sbom)"
check "release built without an SBOM is rejected" 1 \
  "does not name an SBOM" \
  verify "$NO_SBOM" v1.3.0

# A directory holding only a valid signature and no artifacts at all. Every
# per-file check has nothing to look at, so this is the case where a verifier
# built out of --ignore-missing quietly reports success over an empty download.
EMPTY="$(clone_release "$REL" empty)"
rm -f "$EMPTY"/hamlaneh-*
check "signature without any artifacts does not verify" 1 \
  "does not match its signed checksum" \
  verify "$EMPTY" v1.2.3

SBOM_GONE="$(clone_release "$REL" sbom-gone)"
rm -f "$SBOM_GONE/hamlaneh-v1.2.3.spdx.json"
check "SBOM named by the signature but not downloaded is rejected" 1 \
  "download the SBOM before installing" \
  verify "$SBOM_GONE" v1.2.3

# --- usage errors are not verification failures -----------------------------

check "a malformed --version is a usage error, not a pass" 2 \
  "is not a semantic version" \
  verify "$REL" 1.2 --installed v1.0.0

check "a malformed --installed is a usage error, not a pass" 2 \
  "--installed 'latest' is not a semantic version" \
  verify "$REL" v1.2.3 --installed "latest"

check "--version is required" 2 \
  "--version is required" \
  bash "$VERIFY" --dir "$REL"

# ---------------------------------------------------------------------------

printf '\n%d checks, %d failures\n' "$checks" "$failures"
[ "$failures" -eq 0 ]
