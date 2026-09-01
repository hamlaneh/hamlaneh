#!/usr/bin/env bash
#
# Hamlaneh release verification and anti-rollback gate.
#
# Run by an operator before installing a downloaded release, and by the
# auto-updater before applying one. It answers two questions:
#
#   1. Did this release come from us, unmodified, with its SBOM?
#   2. Is it actually newer than what is already installed?
#
# Question 2 is not paperwork. An attacker who can feed the updater a release
# does not need to forge a signature — an OLD release of ours is already
# validly signed, and serving one is how a patched instance gets walked back
# onto a vulnerability we already fixed. So a downgrade is refused here, in
# this script's own logic, and only --force gets past it (ROADMAP.md Phase 4
# test gate item 2; PLAN.md §6.6).
#
# Usage:
#   verify-release.sh --version vX.Y.Z [--dir DIR] [--installed vA.B.C]
#                     [--force] [--repo OWNER/NAME] [--key FILE] [--cosign PATH]
#   verify-release.sh --version vX.Y.Z --print-identity
#
#   --version    the release being verified. Required.
#   --dir        directory holding the downloaded assets. Default: the
#                current directory.
#   --installed  the version running now. Also read from
#                $HAMLANEH_INSTALLED_VERSION. If neither is set there is
#                nothing to roll back from and the check is skipped, loudly.
#   --force      apply a downgrade anyway. The one escape hatch.
#   --repo       repository whose release workflow is allowed to have signed
#                this. Default: hamlaneh/hamlaneh.
#   --key        verify against a cosign PUBLIC KEY instead of the keyless
#                GitHub identity. For an offline mirror, and for
#                verify-release.test.sh, which needs real cosign verification
#                without reaching Sigstore's production infrastructure.
#   --cosign     path to the cosign binary. Also $COSIGN_BIN. Default: cosign.
#   --print-identity
#                print the Fulcio certificate identity a genuine release of
#                this version must carry, and exit. Paste it into `cosign
#                verify` to check the container image by hand, and see
#                docs/releasing.md.
#
# Expects in DIR (as produced by .github/workflows/release.yml):
#   SHA256SUMS                    every released file, one line each
#   SHA256SUMS.sigstore.json      the cosign bundle over SHA256SUMS
#   hamlaneh-<version>.spdx.json  the SBOM
#   ...the release artifacts themselves, however many were downloaded.
#
# Exit codes, so the updater can tell the two failures apart:
#   0  verified
#   1  verification failed (signature, checksum, coverage or SBOM)
#   2  usage error
#   3  refused: this release is older than the installed one, and --force
#      was not given

set -euo pipefail

REPO_DEFAULT="hamlaneh/hamlaneh"
OIDC_ISSUER="https://token.actions.githubusercontent.com"
SIGNING_WORKFLOW=".github/workflows/release.yml"

version=""
dir="."
installed="${HAMLANEH_INSTALLED_VERSION:-}"
force=0
repo="$REPO_DEFAULT"
key=""
cosign_bin="${COSIGN_BIN:-cosign}"
print_identity=0

usage() {
  sed -n '/^# Usage:/,/^#   3  refused/p' "$0" | sed 's/^#[[:space:]]\{0,1\}//'
}

fail() {
  printf 'FAIL: %s\n' "$1" >&2
  exit 1
}

refuse() {
  printf 'REFUSED: %s\n' "$1" >&2
  exit 3
}

usage_error() {
  printf 'ERROR: %s\n\n' "$1" >&2
  usage >&2
  exit 2
}

pass() {
  printf 'OK: %s\n' "$1"
}

note() {
  printf 'NOTE: %s\n' "$1"
}

# ---------------------------------------------------------------------------
# Version ordering
#
# Implemented here rather than shelled out to `sort -V`, which is not semver:
# it orders 1.2.3-rc1 ABOVE 1.2.3, so a rollback from a release to its own
# release candidate would read as an upgrade.
# ---------------------------------------------------------------------------

# Rejects anything that is not OWNER/NAME. The repository reaches a URL, so it
# is validated at the boundary like everything else that does.
repo_is_valid() {
  [[ "$1" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]]
}

# Rejects anything that is not MAJOR.MINOR.PATCH with optional pre-release and
# build metadata. Validation at the boundary, so the arithmetic below can never
# be handed something that is not a number.
#
# The same shape hamlaneh-update.sh's version_is_valid enforces — named rather
# than cited by line, because the line moved and the reference rotted. Kept
# identical on purpose: one script validating what the other does not is how a
# value becomes trusted in one place and attacker-shaped in the next.
version_is_valid() {
  [[ "${1#v}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]
}

# Compares two dot-separated pre-release strings (semver §11). Echoes -1, 0 or 1.
# An empty string means "no pre-release", which outranks every pre-release of
# the same core version.
prerelease_cmp() {
  local a="$1" b="$2"
  if [ -z "$a" ] && [ -z "$b" ]; then printf '0'; return; fi
  if [ -z "$a" ]; then printf '1'; return; fi
  if [ -z "$b" ]; then printf -- '-1'; return; fi

  local -a A B
  IFS=. read -r -a A <<<"$a"
  IFS=. read -r -a B <<<"$b"

  local i x y
  for ((i = 0; i < ${#A[@]} || i < ${#B[@]}; i++)); do
    x="${A[i]-}"
    y="${B[i]-}"
    # Ran out of identifiers on one side while everything matched: the shorter
    # list has the lower precedence.
    if [ -z "$x" ]; then printf -- '-1'; return; fi
    if [ -z "$y" ]; then printf '1'; return; fi

    if [[ "$x" =~ ^[0-9]+$ ]] && [[ "$y" =~ ^[0-9]+$ ]]; then
      if ((10#$x < 10#$y)); then printf -- '-1'; return; fi
      if ((10#$x > 10#$y)); then printf '1'; return; fi
    elif [[ "$x" =~ ^[0-9]+$ ]]; then
      printf -- '-1'; return  # numeric identifiers rank below alphanumeric ones
    elif [[ "$y" =~ ^[0-9]+$ ]]; then
      printf '1'; return
    else
      if [[ "$x" < "$y" ]]; then printf -- '-1'; return; fi
      if [[ "$x" > "$y" ]]; then printf '1'; return; fi
    fi
  done
  printf '0'
}

# Echoes -1 if $1 sorts below $2, 0 if they are equal, 1 if above.
semver_cmp() {
  local a="${1#v}" b="${2#v}"
  a="${a%%+*}"  # build metadata carries no precedence (semver §10)
  b="${b%%+*}"

  local a_core="${a%%-*}" b_core="${b%%-*}"
  local a_pre="" b_pre=""
  if [ "$a" != "$a_core" ]; then a_pre="${a#*-}"; fi
  if [ "$b" != "$b_core" ]; then b_pre="${b#*-}"; fi

  local -a A B
  IFS=. read -r -a A <<<"$a_core"
  IFS=. read -r -a B <<<"$b_core"

  local i x y
  for i in 0 1 2; do
    x="${A[i]:-0}"
    y="${B[i]:-0}"
    if ((10#$x < 10#$y)); then printf -- '-1'; return; fi
    if ((10#$x > 10#$y)); then printf '1'; return; fi
  done

  prerelease_cmp "$a_pre" "$b_pre"
}

# ---------------------------------------------------------------------------
# The signing identity
# ---------------------------------------------------------------------------

# The Fulcio certificate identity that a genuine release carries: this
# repository's release workflow, at this exact tag. This one string is what
# separates "signed by us" from "signed by somebody with a GitHub account", so
# it is built in one place, printable with --print-identity, and tested.
#
# Every literal dot matters: an unescaped dot is a regex wildcard, and this
# pattern fails OPEN when it is too permissive — unescaped, `.github` also
# matches `Xgithub`, a path an attacker owns in their own repository.
#
# Dots are written as the character class [.] rather than \. deliberately. A
# backslash here has to survive bash's double quotes, then a substitution's
# replacement text, then printf, and getting that count wrong is silent in
# both directions. [.] needs no backslash at all, so there is nothing to
# miscount.
# Wraps the characters a regex would read as syntax so they can only match
# themselves. Written as [x] rather than with a backslash for the reason the
# dots already were: a backslash here has to survive double quotes, a
# substitution's replacement text and printf, and miscounting it is silent in
# both directions.
#
# Only . and + can reach here -- version_is_valid and repo_is_valid between
# them admit no other metacharacter -- and that is the point: the pattern's
# safety is a property of what can arrive rather than an argument about
# whether widening happens to be harmless. It was harmless. This function has
# failed open once, so 'harmless' is not the standard it gets held to.
regex_literal() {
  local out="${1//./[.]}"
  printf '%s' "${out//+/[+]}"
}

signing_identity() {
  printf '^https://github[.]com/%s/%s@refs/tags/%s$' \
    "$(regex_literal "$repo")" \
    "$(regex_literal "$SIGNING_WORKFLOW")" \
    "$(regex_literal "$version")"
}

# ---------------------------------------------------------------------------
# Checks
# ---------------------------------------------------------------------------

require_tools() {
  local cmd
  for cmd in "$cosign_bin" sha256sum sed awk; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      printf 'ERROR: required tool not found: %s\n' "$cmd" >&2
      exit 2
    fi
  done
}

# The anti-rollback control.
check_not_a_rollback() {
  if [ -z "$installed" ]; then
    note "no installed version given (--installed / \$HAMLANEH_INSTALLED_VERSION) — rollback check skipped"
    return
  fi
  version_is_valid "$installed" ||
    usage_error "--installed '$installed' is not a semantic version"

  local order
  order="$(semver_cmp "$version" "$installed")"

  if [ "$order" = '-1' ]; then
    if [ "$force" -eq 1 ]; then
      note "$version is OLDER than the installed $installed — applying anyway because --force was given"
      return
    fi
    refuse "$version is older than the installed $installed. A validly signed old release is still a downgrade onto a fixed vulnerability. Re-run with --force if this is deliberate."
  fi

  if [ "$order" = '0' ]; then
    pass "$version is the installed version — not a rollback"
  else
    pass "$version is newer than the installed $installed"
  fi
}

# The cosign signature over SHA256SUMS. Everything else in this file trusts
# SHA256SUMS, so this is the check that has to hold.
check_signature() {
  local sums="$dir/SHA256SUMS"
  local bundle="$dir/SHA256SUMS.sigstore.json"

  [ -f "$sums" ] || fail "$sums is missing — nothing to verify against"
  [ -f "$bundle" ] || fail "$bundle is missing — this release is unsigned"

  if [ -n "$key" ]; then
    # Offline mirror / test path: a public key replaces the Fulcio identity.
    # The transparency log cannot vouch for a key-signed blob, hence
    # --insecure-ignore-tlog; the signature itself is checked in full.
    [ -f "$key" ] || usage_error "--key '$key' does not exist"
    "$cosign_bin" verify-blob \
      --key "$key" \
      --bundle "$bundle" \
      --insecure-ignore-tlog \
      "$sums" >/dev/null 2>&1 ||
      fail "cosign could not verify SHA256SUMS against the public key $key"
    pass "SHA256SUMS carries a valid cosign signature (public key $key)"
    return
  fi

  # Keyless. The certificate identity is pinned to this repository's release
  # workflow AT THIS TAG, so a genuine signature over a different version's
  # checksums cannot be replayed onto this one.
  local identity
  identity="$(signing_identity)"

  "$cosign_bin" verify-blob \
    --bundle "$bundle" \
    --certificate-identity-regexp "$identity" \
    --certificate-oidc-issuer "$OIDC_ISSUER" \
    "$sums" >/dev/null 2>&1 ||
    fail "cosign could not verify SHA256SUMS as signed by ${repo}'s release workflow at tag ${version}"
  pass "SHA256SUMS was signed by ${repo}'s release workflow at ${version}"
}

# Every file in DIR has to be named by the signed SHA256SUMS. Without this, an
# attacker who drops in an extra artifact and deletes its checksum line gets a
# clean run: `sha256sum -c` only checks what it was told about.
check_every_file_is_covered() {
  local listed uncovered=""
  listed="$(sed -E 's/^[0-9a-fA-F]+[[:space:]][[:space:]*]//' "$dir/SHA256SUMS")"

  local path name
  for path in "$dir"/*; do
    [ -f "$path" ] || continue
    name="$(basename "$path")"
    case "$name" in
      SHA256SUMS | SHA256SUMS.sigstore.json) continue ;;
    esac
    if ! grep -qxF -- "$name" <<<"$listed"; then
      uncovered="${uncovered}${uncovered:+, }${name}"
    fi
  done

  [ -z "$uncovered" ] ||
    fail "present but not named by the signed SHA256SUMS: ${uncovered}"
  pass "every file in $dir is covered by the signed SHA256SUMS"
}

# Content. --ignore-missing so an operator who downloaded one binary is not
# told the other four are corrupt; it still fails when nothing at all matched.
check_checksums() {
  (cd "$dir" && sha256sum -c --ignore-missing --quiet SHA256SUMS) ||
    fail "at least one artifact does not match its signed checksum"
  pass "every downloaded artifact matches its signed checksum"
}

check_sbom() {
  local sbom="hamlaneh-${version}.spdx.json"

  grep -qF -- "$sbom" "$dir/SHA256SUMS" ||
    fail "the signed SHA256SUMS does not name an SBOM ($sbom) — this release did not ship one"
  [ -f "$dir/$sbom" ] || fail "$dir/$sbom is missing — download the SBOM before installing"
  # Cheap sanity check: a saved error page is a file too, and it would sail
  # past a bare -f test.
  grep -qF '"spdxVersion"' "$dir/$sbom" ||
    fail "$dir/$sbom does not look like an SPDX document"
  pass "SBOM present and covered by the signature ($sbom)"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

while [ $# -gt 0 ]; do
  case "$1" in
    --version) version="${2-}"; shift 2 ;;
    --dir) dir="${2-}"; shift 2 ;;
    --installed) installed="${2-}"; shift 2 ;;
    --repo) repo="${2-}"; shift 2 ;;
    --key) key="${2-}"; shift 2 ;;
    --cosign) cosign_bin="${2-}"; shift 2 ;;
    --force) force=1; shift ;;
    --print-identity) print_identity=1; shift ;;
    -h | --help) usage; exit 0 ;;
    *) usage_error "unknown argument: $1" ;;
  esac
done

[ -n "$version" ] || usage_error "--version is required"
version_is_valid "$version" ||
  usage_error "--version '$version' is not a semantic version (expected vX.Y.Z)"

repo_is_valid "$repo" ||
  usage_error "--repo '$repo' is not an OWNER/NAME repository"

if [ "$print_identity" -eq 1 ]; then
  signing_identity
  printf '\n'
  exit 0
fi

[ -d "$dir" ] || usage_error "--dir '$dir' is not a directory"

require_tools

printf 'Verifying Hamlaneh %s in %s\n\n' "$version" "$dir"

check_not_a_rollback
check_signature
check_every_file_is_covered
check_checksums
check_sbom

printf '\nHamlaneh %s verified.\n' "$version"
