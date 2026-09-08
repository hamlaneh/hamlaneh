#!/usr/bin/env bash
#
# Hamlaneh auto-updater — applies a signed release, and only a signed release.
#
# ROADMAP.md Phase 4 test gate item 2: "Auto-update applies a signed release; a
# tampered release is rejected; an older validly-signed release is rejected
# unless explicitly forced." deploy/verify-release.sh already implements the
# whole of that sentence except the word "applies". This script is the word
# "applies", and it owns nothing else:
#
#   THIS SCRIPT CONTAINS NO SIGNATURE LOGIC AND NO VERSION ORDERING.
#
# It downloads, calls verify-release.sh, and treats that script's exit code as
# the authority — 0 applies, 3 is a refused downgrade, anything else aborts.
# Two copies of a security check is how one of them rots.
#
# Two deployment shapes, two genuinely different swaps:
#
#   compose  the stack in deploy/docker-compose.yml. The server image is
#            pulled from GHCR, verified against the SAME signing identity
#            verify-release.sh pins (asked for with --print-identity, never
#            retyped), retagged onto the tag the running container already
#            uses, and the service is recreated. Rollback retags the previous
#            image id back — the bytes are still in the local docker store, so
#            it needs no network.
#
#   home     the single binary (ADR 012). The new binary is extracted beside
#            the installed one and moved into place with rename(2), which is
#            atomic: there is no instant at which the path does not exist, so
#            a power cut leaves either the old binary or the new one and never
#            a hole. Rollback is the same rename in reverse.
#
# Both paths verify BEFORE they touch anything, and both roll back if the new
# version does not come up healthy.
#
# Usage:
#   hamlaneh-update.sh [--mode compose|home] [--channel security|all]
#                      [--version vX.Y.Z] [--installed vX.Y.Z] [--force]
#                      [--from-dir DIR] [--asset NAME] [--check]
#                      [--binary PATH] [--restart-command CMD]
#                      [--compose-file PATH] [--env-file PATH]
#                      [--repo OWNER/NAME] [--key FILE]
#                      [--cosign PATH] [--docker PATH]
#                      [--state-dir DIR] [--no-prune]
#   hamlaneh-update.sh --install-timer
#   hamlaneh-update.sh --serve-request
#   hamlaneh-update.sh --prune-only
#
#   --mode        deployment shape. Default: detected (a running compose
#                 server service means compose, otherwise home).
#   --channel     security (default) applies only patch releases of the
#                 installed MAJOR.MINOR — where security fixes are backported,
#                 per SECURITY.md's supported-versions model. all applies any
#                 newer release. A newer release outside the channel is
#                 reported, never silently ignored.
#   --version     the release to apply. Default: the latest GitHub release.
#   --installed   the version running now. Default: asked of the server
#                 binary (see the contract note above probe_version).
#   --force       apply a downgrade. Passed straight through to
#                 verify-release.sh, which owns the refusal.
#   --from-dir    apply an already-downloaded release directory instead of
#                 downloading one. For an offline mirror, and for
#                 hamlaneh-update.test.sh. Verification is identical.
#   --asset       home mode: the release asset to install. Default: the
#                 tarball for this host's os/arch.
#   --check       report what would happen and change nothing.
#   --binary      home mode: the installed binary.
#                 Default: /usr/local/bin/hamlaneh-server.
#   --restart-command
#                 home mode: how to restart the service after the swap.
#                 Default: systemctl restart hamlaneh.
#   --health-timeout
#                 seconds the new version gets to report healthy AND report
#                 its own version before the update is rolled back.
#                 Default: 120.
#   --compose-file, --env-file
#                 compose mode: default to the files beside this script.
#   --repo        the repository a release must come from.
#                 Default: hamlaneh/hamlaneh.
#   --key         verify against a cosign public key instead of the keyless
#                 GitHub identity. Handed to verify-release.sh; in this mode
#                 the container image is NOT identity-checked, so it is
#                 refused in compose mode rather than half-checked.
#   --cosign      path to cosign. Also $COSIGN_BIN. Default: cosign.
#   --docker      path to docker. Also $DOCKER_BIN. Default: docker.
#   --state-dir   where the admin dashboard and this script exchange state
#                 (ADR 016). Default: the directory the running server has
#                 mounted at /var/lib/hamlaneh-update in compose mode, and
#                 /var/lib/hamlaneh-update in home mode. Every run that can
#                 resolve it records what it did there, which is the only way
#                 the dashboard learns that the last run failed.
#   --no-prune    keep the images, containers and build cache this run
#                 orphaned. Off by default: see prune_docker.
#   --install-timer
#                 write and enable the systemd timer that runs this script on
#                 the security channel, AND the path unit that watches for a
#                 request from the dashboard, then exit. This is what makes
#                 auto-update on by default; see the block above install_timer.
#   --serve-request
#                 consume one request written by the server and act on it.
#                 What the dashboard's button ultimately reaches, and the one
#                 entry point that reads a file somebody else wrote — see
#                 serve_request for why that file cannot name what runs.
#   --prune-only  run the cleanup and nothing else.
#
# Exit codes:
#   0  up to date, or updated successfully
#   1  failed — download, verification, or the update could not be applied
#   2  usage error
#   3  refused: the offered release is older than the installed one and
#      --force was not given (verify-release.sh's own code, propagated)
#   4  rolled back: the new version was applied but did not come up healthy,
#      and the previous version was restored

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT_PATH="${SCRIPT_DIR}/$(basename "${BASH_SOURCE[0]}")"
VERIFY="${SCRIPT_DIR}/verify-release.sh"

REPO_DEFAULT="hamlaneh/hamlaneh"
API_HOST="https://api.github.com"
DOWNLOAD_HOST="https://github.com"
OIDC_ISSUER="https://token.actions.githubusercontent.com"
# The image release.yml pushes: ghcr.io/<repo>/server.
IMAGE_SUFFIX="server"
SERVER_BIN_IN_IMAGE="/usr/local/bin/hamlaneh-server"

# How long the new version gets to answer its own healthcheck before the
# update is called a failure and rolled back.
HEALTH_TIMEOUT=120
HEALTH_INTERVAL=3

# Where "one updater at a time" is enforced. See take_lock for why this is
# /run and deliberately NOT under TMPDIR.
#
# HAMLANEH_UPDATE_LOCK_DIR overrides the whole path and is the only test seam
# in this script; deploy/hamlaneh-update.test.sh uses it so a test run cannot
# contend with an updater running for real on the same host.
if [ -n "${HAMLANEH_UPDATE_LOCK_DIR:-}" ]; then
  LOCK_DIR="$HAMLANEH_UPDATE_LOCK_DIR"
elif [ -d /run ] && [ -w /run ]; then
  LOCK_DIR="/run/hamlaneh-update.lock"
else
  LOCK_DIR="${TMPDIR:-/tmp}/hamlaneh-update.lock"
fi

mode=""
channel="security"
version=""
installed=""
force=0
from_dir=""
asset=""
check_only=0
binary="/usr/local/bin/hamlaneh-server"
restart_command="systemctl restart hamlaneh"
compose_file="${SCRIPT_DIR}/docker-compose.yml"
env_file="${SCRIPT_DIR}/.env"
repo="$REPO_DEFAULT"
key=""
cosign_bin="${COSIGN_BIN:-cosign}"
docker_bin="${DOCKER_BIN:-docker}"
install_timer_only=0
serve_request_only=0
prune_only=0
state_dir="${HAMLANEH_UPDATE_STATE_DIR:-}"
no_prune=0

# Where the server container has the state volume mounted. One constant in
# two places — deploy/docker-compose.yml mounts it there and the server is
# told the same path — so changing it means changing both.
STATE_MOUNT="/var/lib/hamlaneh-update"

# The id of the request being served, echoed back into status.json so the
# dashboard can tell its own click apart from the timer's scheduled run.
request_id=""

# Set to 1 when a newer release exists that this channel will not apply. Its
# own field rather than a kind of "up to date", because those are different
# sentences to an operator: one means there is nothing to do, the other means
# there is something to do somewhere else.
available_outside_channel=0

# Stamped once, at the top of main, so every status this run writes agrees
# about when the run began.
run_started_at=""

# Cleanup state, set as the run progresses so the EXIT trap knows what it owns.
download_dir=""
staging_dir=""
lock_held=0

usage() {
  sed -n '/^# Usage:/,/^#      and the previous/p' "$0" | sed 's/^#[[:space:]]\{0,1\}//'
}

log() { printf '[hamlaneh-update] %s\n' "$*"; }

# Every failure exits through here, so this is also the one place that has to
# remember to tell the dashboard. write_status is a no-op until the run knows
# where its state directory is, which is why it is safe on the early paths
# that fail before anything is resolved.
fail() {
  printf '[hamlaneh-update] FAIL: %s\n' "$*" >&2
  write_status failed 1 "$*"
  exit 1
}

usage_error() {
  printf '[hamlaneh-update] ERROR: %s\n\n' "$*" >&2
  usage >&2
  exit 2
}

cleanup() {
  [ -n "$download_dir" ] && rm -rf "$download_dir"
  [ -n "$staging_dir" ] && rm -rf "$staging_dir"
  [ "$lock_held" -eq 1 ] && rmdir "$LOCK_DIR" 2>/dev/null
  return 0
}
trap cleanup EXIT

# One updater at a time. A timer firing while an operator runs this by hand
# would otherwise have two processes swapping the same binary. mkdir is the
# lock rather than flock because home mode is not only Linux (ADR 012) and
# mkdir is atomic everywhere.
#
# The lock lives in /run and NOT under TMPDIR, and that is the whole point
# rather than a preference. install_timer below writes a unit with
# PrivateTmp=true, which hands the timer a /tmp of its own. Under TMPDIR the
# timer's lock and the operator's are then two different directories in two
# namespaces neither can see: both mkdir calls succeed, both processes swap
# the same binary, and the timer-versus-operator race this lock is named for
# is exactly the one it fails to cover. PrivateTmp namespaces /tmp and
# /var/tmp and nothing else, so /run is one directory for both — and both are
# root here, since the update writes /usr/local/bin or drives docker.
#
# A host with no writable /run falls back to TMPDIR. That host has no systemd
# and therefore no timer, so the only race left there is manual against
# manual, which TMPDIR does cover.
take_lock() {
  if ! mkdir "$LOCK_DIR" 2>/dev/null; then
    fail "another update is already running (lock: $LOCK_DIR). Remove it by hand only if no updater is running."
  fi
  lock_held=1
}

# ---------------------------------------------------------------------------
# The installed version
#
# CONTRACT THIS SCRIPT DEPENDS ON: `hamlaneh-server --version` exits 0 and
# writes to stdout a line containing a semantic version tag, "vX.Y.Z" with an
# optional pre-release suffix — e.g. "hamlaneh-server v1.4.0". The first such
# token on the output is taken as the installed version.
#
# That flag is being added in server/ in parallel with this script. If it is
# missing, or prints something else, this function FAILS THE RUN. It must
# never fall back to a guess or to "unknown": the installed version is the
# only input to verify-release.sh's anti-rollback check, and an updater that
# quietly stopped supplying it would keep reporting success while the one
# control this gate is about silently stopped running.
# ---------------------------------------------------------------------------
probe_version() {
  local out v rc=0
  out="$("$@" --version 2>&1)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    fail "'$* --version' exited ${rc}. This updater cannot determine what is installed, and without it the anti-rollback check cannot run. Output was: ${out}"
  fi
  v="$(grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?' <<<"$out" | head -n 1)" || v=""
  if [ -z "$v" ]; then
    fail "'$* --version' printed no vX.Y.Z version, so the installed version is unknown and the anti-rollback check cannot run. Output was: ${out}"
  fi
  printf '%s' "$v"
}

# Anything that reaches a URL, a filename or docker has to be a version and
# nothing else. The release name is attacker-influenced input (it arrives over
# the network), so it is validated at the boundary, once.
version_is_valid() {
  [[ "${1#v}" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]
}

repo_is_valid() {
  [[ "$1" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]]
}

# MAJOR.MINOR, for the channel gate. Deliberately NOT an ordering: which of
# two versions is newer is verify-release.sh's question, and it is asked there.
version_series() {
  local v="${1#v}"
  v="${v%%-*}"
  v="${v%%+*}"
  printf '%s.%s' "${v%%.*}" "$(cut -d. -f2 <<<"$v")"
}

# ---------------------------------------------------------------------------
# The state directory (ADR 016)
#
# Where this script tells the admin dashboard what it did, and where it finds
# a request the dashboard wrote. Nothing here is authoritative about anything:
# the dashboard renders it, and the only file this script READS is
# request.json, whose entire influence is described above serve_request.
#
# Every write is a temporary file plus a rename, because the reader is a
# different process with no lock: rename(2) is atomic, so the server sees
# either the whole previous file or the whole new one, never half of either.
# ---------------------------------------------------------------------------

now_utc() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# The subset of JSON string escaping these values can actually need. Version
# strings and the updater's own log lines are the only free-form things that
# reach a status file, but "only" is how an injected quote gets in, so both
# metacharacters are escaped and control characters are dropped rather than
# emitted raw (a literal newline inside a JSON string is invalid, and would
# make the dashboard's parse fail on exactly the run it most needs to read).
json_escape() {
  printf '%s' "$1" | tr -d '\000-\037' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

# The directory, or empty if this deployment does not have one. Empty is a
# normal answer, not an error: an install whose stack is down, or a home-mode
# host that never created the directory, simply has no state to publish, and
# the dashboard reports the feature as unavailable rather than lying.
#
# In compose mode the path is read off the running container's mount table
# rather than reconstructed from the volume's name, for the same reason
# apply_compose reads the image it is replacing: the project name is not ours
# to predict, and a guess here writes the status somewhere nobody reads.
resolve_state_dir() {
  local cid src
  if [ -n "$state_dir" ]; then
    printf '%s' "$state_dir"
    return
  fi
  if [ "$mode" = "compose" ]; then
    cid="$(compose_container)" || return 0
    [ -n "$cid" ] || return 0
    src="$("$docker_bin" inspect --format \
      "{{range .Mounts}}{{if eq .Destination \"${STATE_MOUNT}\"}}{{.Source}}{{end}}{{end}}" \
      "$cid" 2>/dev/null)" || return 0
    printf '%s' "$src"
    return
  fi
  [ -d "$STATE_MOUNT" ] && printf '%s' "$STATE_MOUNT"
  return 0
}

# state, exit code, message. Everything else comes from what the run already
# knows. Failure to write is logged and never fatal: an instance that cannot
# record its status is still an instance that must finish updating.
write_status() {
  local state="$1" code="$2" message="$3"
  local dir tmp available="null" outside="false"
  dir="$(resolve_state_dir)"
  [ -n "$dir" ] && [ -d "$dir" ] || return 0

  [ -n "$version" ] && available="\"$(json_escape "$version")\""
  [ "$available_outside_channel" -eq 1 ] && outside="true"

  tmp="${dir}/.status.json.$$"
  {
    printf '{"schema":1'
    printf ',"id":"%s"' "$(json_escape "$request_id")"
    printf ',"state":"%s"' "$state"
    printf ',"channel":"%s"' "$channel"
    printf ',"available_version":%s' "$available"
    printf ',"available_outside_channel":%s' "$outside"
    printf ',"exit_code":%s' "$code"
    printf ',"started_at":"%s"' "$run_started_at"
    printf ',"finished_at":"%s"' "$(now_utc)"
    printf ',"checked_at":"%s"' "$(now_utc)"
    printf ',"message":"%s"' "$(json_escape "$message")"
    printf '}\n'
  } >"$tmp" 2>/dev/null || { rm -f "$tmp"; return 0; }
  mv -f "$tmp" "${dir}/status.json" 2>/dev/null || rm -f "$tmp"
  return 0
}

# What makes the dashboard's button appear at all: proof that something on
# this host is listening. Refreshed on every run, so a host whose units were
# removed stops claiming to listen once the server's staleness window passes
# — a claim that expires beats a flag nobody remembers to clear.
stamp_watcher() {
  local dir="$1" tmp installed_at
  [ -n "$dir" ] && [ -d "$dir" ] || return 0
  installed_at="$(now_utc)"
  if [ -f "${dir}/watcher.json" ]; then
    installed_at="$(sed -n 's/.*"installed_at":"\([^"]*\)".*/\1/p' \
      "${dir}/watcher.json" 2>/dev/null | head -n 1)"
    [ -n "$installed_at" ] || installed_at="$(now_utc)"
  fi
  tmp="${dir}/.watcher.json.$$"
  printf '{"schema":1,"installed_at":"%s","last_seen":"%s","channel":"%s"}\n' \
    "$(json_escape "$installed_at")" "$(now_utc)" "$channel" \
    >"$tmp" 2>/dev/null || { rm -f "$tmp"; return 0; }
  mv -f "$tmp" "${dir}/watcher.json" 2>/dev/null || rm -f "$tmp"
  return 0
}

# ---------------------------------------------------------------------------
# Cleanup
#
# What "no leftovers on the disk" means, spelled out as three narrow commands
# rather than one broad one. `docker system prune` is the command everybody
# reaches for and it is the wrong one here: its --volumes form would take
# db_data, file_data and caddy_data — the database, every uploaded file, and
# the certificate store — and there is no version of this feature worth that
# risk. No path below passes --volumes, and none ever should.
#
#   image prune      dangling images only, never -a. A retag orphans the
#                    image it replaced, and that orphan is what accumulates
#                    across updates. Anything a container still uses is
#                    skipped by docker itself.
#   container prune  scoped to this compose project by label, so a host that
#                    also runs something else keeps its stopped containers.
#   builder prune    the build cache, which is the big one: an install that
#                    builds images locally leaves gigabytes here and nothing
#                    else ever removes it.
#
# Called after a run that SUCCEEDED or found nothing to do. Deliberately not
# after a rollback — the previous image is untagged at that moment and is
# exactly what an image prune would take — and not after a failure, where
# what is left behind is evidence.
# ---------------------------------------------------------------------------
prune_docker() {
  local project=""
  if [ "$no_prune" -eq 1 ]; then
    log "skipping cleanup (--no-prune)"
    return 0
  fi
  command -v "$docker_bin" >/dev/null 2>&1 || return 0

  log "removing what the update orphaned (no volume is touched)"
  "$docker_bin" image prune -f >/dev/null 2>&1 || true

  if [ "$mode" = "compose" ]; then
    project="$("$docker_bin" inspect --format \
      '{{index .Config.Labels "com.docker.compose.project"}}' \
      "$(compose_container)" 2>/dev/null)" || project=""
  fi
  if [ -n "$project" ]; then
    "$docker_bin" container prune -f \
      --filter "label=com.docker.compose.project=${project}" >/dev/null 2>&1 || true
  fi

  "$docker_bin" builder prune -f >/dev/null 2>&1 || true
  return 0
}

# ---------------------------------------------------------------------------
# Serving a request from the dashboard
#
# THE ONE PLACE THIS SCRIPT READS A FILE SOMEBODY ELSE WROTE, and therefore
# the place worth reading carefully.
#
# Exactly one field is consulted, and it selects between two fixed argument
# vectors written out below in full. No value from the file is interpolated
# into a command, expanded, or passed as an argument to anything. The id is
# copied into status.json and nowhere else, and it is escaped on the way.
#
# That is not defensive style, it is the security property (ADR 016 §1): a
# request that could name a version could name an older one, and the flag
# that applies an older one is --force, which is the whole anti-rollback
# control. There is no code path from this file to that flag.
#
# The request is deleted BEFORE it is acted on. The path unit retriggers
# while the file exists, so a request that survived its own run would run
# forever; and a request that crashes the updater must not be retried
# automatically, because the second attempt would meet a half-applied stack.
# ---------------------------------------------------------------------------
serve_request() {
  local dir req kind id
  dir="$(resolve_state_dir)"
  [ -n "$dir" ] && [ -d "$dir" ] ||
    fail "no state directory — nothing asked for an update, or the stack is down"
  req="${dir}/request.json"
  [ -f "$req" ] || { log "no request to serve"; exit 0; }

  kind="$(sed -n 's/.*"kind"[[:space:]]*:[[:space:]]*"\([a-z]*\)".*/\1/p' "$req" 2>/dev/null | head -n 1)"
  id="$(sed -n 's/.*"id"[[:space:]]*:[[:space:]]*"\([0-9A-Za-z-]*\)".*/\1/p' "$req" 2>/dev/null | head -n 1)"
  rm -f "$req"

  request_id="$id"
  # The second and last test seam in this script, alongside
  # HAMLANEH_UPDATE_LOCK_DIR: what gets re-invoked. A test needs to read the
  # argument vector rather than infer it, because "the vector is fixed" is the
  # property, and asserting a property by inference is how it stops holding.
  local self="${HAMLANEH_UPDATE_SERVE_EXEC:-$SCRIPT_PATH}"
  case "$kind" in
    check)
      log "serving a check requested from the dashboard"
      "$self" --check --state-dir "$dir" --request-id "$id"
      ;;
    apply)
      log "serving an update requested from the dashboard"
      "$self" --state-dir "$dir" --request-id "$id"
      ;;
    *)
      # fail writes the status. A request naming something else is a failure
      # and not the contract's `refused`, which means the updater declined a
      # real release on purpose — the operator needs to be able to tell "the
      # dashboard sent nonsense" from "a downgrade was blocked".
      fail "request.json named '${kind:-nothing}'; only 'check' and 'apply' exist"
      ;;
  esac
}

# ---------------------------------------------------------------------------
# Discovery and download
# ---------------------------------------------------------------------------

require_tools() {
  local cmd
  for cmd in "$@"; do
    command -v "$cmd" >/dev/null 2>&1 ||
      fail "required tool not found: ${cmd}"
  done
}

# --proto/--proto-redir pin https for the request AND for anything it is
# redirected to. There is no flag here that weakens TLS, and there must never
# be one: this fetches the bytes a signature is about to be checked over, and
# the checksums file that says which bytes those are.
fetch() {
  local url="$1" dest="$2"
  curl -fsSL \
    --proto '=https' --proto-redir '=https' \
    --tlsv1.2 \
    --retry 3 --retry-delay 2 \
    -o "$dest" "$url" ||
    fail "could not download ${url}"
}

latest_release() {
  local body v
  body="$(curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 --retry 3 \
    -H 'Accept: application/vnd.github+json' \
    "${API_HOST}/repos/${repo}/releases/latest")" ||
    fail "could not reach ${API_HOST} to discover the latest release"
  v="$(grep -oE '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' <<<"$body" |
    head -n 1 | sed -E 's/.*"([^"]*)"$/\1/')" || v=""
  [ -n "$v" ] || fail "the release API answered without a tag_name"
  printf '%s' "$v"
}

host_asset() {
  local os arch
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) fail "no release asset is built for $(uname -s); install by hand or use --asset" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) fail "no release asset is built for $(uname -m); install by hand or use --asset" ;;
  esac
  printf 'hamlaneh-%s-%s-%s.tar.gz' "$version" "$os" "$arch"
}

# The four files verify-release.sh expects, and not one file more: it fails a
# directory holding anything the signed SHA256SUMS does not name.
download_release() {
  download_dir="$(mktemp -d)"
  local base="${DOWNLOAD_HOST}/${repo}/releases/download/${version}"
  local name
  log "downloading ${version} from ${DOWNLOAD_HOST}/${repo}"
  for name in SHA256SUMS SHA256SUMS.sigstore.json "hamlaneh-${version}.spdx.json"; do
    fetch "${base}/${name}" "${download_dir}/${name}"
  done
  if [ "$mode" = "home" ]; then
    fetch "${base}/${asset}" "${download_dir}/${asset}"
  fi
  printf '%s' "$download_dir"
}

# ---------------------------------------------------------------------------
# The gate
#
# verify-release.sh is the authority. This function neither second-guesses its
# verdict nor adds one of its own; it maps the exit code and stops.
# ---------------------------------------------------------------------------
verify_release() {
  local dir="$1" rc=0
  local -a args=(--version "$version" --dir "$dir" --repo "$repo" --cosign "$cosign_bin")
  if [ -n "$installed" ]; then args+=(--installed "$installed"); fi
  if [ "$force" -eq 1 ]; then args+=(--force); fi
  if [ -n "$key" ]; then args+=(--key "$key"); fi

  log "handing ${version} to verify-release.sh"
  bash "$VERIFY" "${args[@]}" || rc=$?

  case "$rc" in
    0) ;;
    3)
      printf '[hamlaneh-update] REFUSED: verify-release.sh refused %s as a downgrade from %s. Nothing was changed.\n' \
        "$version" "$installed" >&2
      write_status refused 3 \
        "${version} was refused as a downgrade from ${installed}; nothing was changed"
      exit 3
      ;;
    *)
      fail "verify-release.sh rejected ${version} (exit ${rc}). Nothing was changed."
      ;;
  esac
}

# ---------------------------------------------------------------------------
# compose mode
# ---------------------------------------------------------------------------

compose() {
  "$docker_bin" compose -f "$compose_file" --env-file "$env_file" "$@"
}

compose_container() {
  compose ps -q server 2>/dev/null || true
}

# The image carries its own signature, over its digest, made by the same
# workflow at the same tag. The identity pattern is ASKED FOR rather than
# rebuilt here: a hand-copied regex is a regex that can be wrong in the
# permissive direction, and that one string is the whole difference between
# "signed by us" and "signed by somebody with a GitHub account".
verify_image() {
  local image="$1" digest identity
  digest="$("$docker_bin" image inspect --format '{{index .RepoDigests 0}}' "$image" 2>/dev/null)" || digest=""
  [ -n "$digest" ] ||
    fail "${image} has no registry digest — it was not pulled from a registry, so there is nothing signed to check"

  identity="$(bash "$VERIFY" --version "$version" --repo "$repo" --print-identity)" ||
    fail "could not obtain the signing identity pattern from verify-release.sh"

  "$cosign_bin" verify \
    --certificate-identity-regexp "$identity" \
    --certificate-oidc-issuer "$OIDC_ISSUER" \
    "$digest" >/dev/null 2>&1 ||
    fail "cosign could not verify ${digest} as signed by ${repo}'s release workflow at ${version}. Nothing was changed."
  log "image signature verified: ${digest}"
}

compose_healthy() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT)) reported
  while [ "$SECONDS" -lt "$deadline" ]; do
    if compose exec -T server "$SERVER_BIN_IN_IMAGE" healthcheck >/dev/null 2>&1; then
      reported="$(compose exec -T server "$SERVER_BIN_IN_IMAGE" --version 2>/dev/null |
        grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?' | head -n 1)" || reported=""
      if [ "$reported" = "$version" ]; then
        return 0
      fi
    fi
    sleep "$HEALTH_INTERVAL"
  done
  return 1
}

apply_compose() {
  local image="ghcr.io/${repo}/${IMAGE_SUFFIX}:${version}"
  local cid previous_id local_tag

  cid="$(compose_container)"
  [ -n "$cid" ] || fail "no running 'server' container — start the stack before updating it"

  # What the running container was created from. Read from reality rather than
  # reconstructed from compose's image-naming convention, so this keeps working
  # if that convention or the project name changes.
  local_tag="$("$docker_bin" inspect --format '{{.Config.Image}}' "$cid")"
  previous_id="$("$docker_bin" inspect --format '{{.Image}}' "$cid")"
  # Spelled as an if rather than `A && B || fail`: on this path C running when
  # A is true is exactly what has to happen, but that is the one case the
  # `&& ||` shape does not say out loud, and it is a rollback anchor — reading
  # it wrong once means rolling back to nothing.
  if [ -z "$local_tag" ] || [ -z "$previous_id" ]; then
    fail "could not read the running server container's image — refusing to swap something this script cannot roll back"
  fi

  log "pulling ${image}"
  "$docker_bin" pull "$image" >/dev/null ||
    fail "could not pull ${image}"

  verify_image "$image"

  # From here the local docker store holds verified bytes and the old image id
  # is recorded. Retagging is a store-local, atomic pointer move; the running
  # container is untouched by it, so a crash between here and the recreate
  # leaves the OLD version serving and the next run converges.
  log "retagging ${image} as ${local_tag}"
  "$docker_bin" tag "$image" "$local_tag" ||
    fail "could not retag ${image} as ${local_tag}"

  log "recreating the server service"
  if ! compose up -d --no-build --force-recreate server; then
    rollback_compose "$previous_id" "$local_tag"
    fail "the new server container would not start; rolled back to the previous image"
  fi

  if ! compose_healthy; then
    log "the new server did not report healthy as ${version} within ${HEALTH_TIMEOUT}s — rolling back"
    rollback_compose "$previous_id" "$local_tag"
    printf '[hamlaneh-update] ROLLED BACK: %s did not come up healthy; the previous image is running again.\n' \
      "$version" >&2
    # No prune on this path, deliberately: the image rollback just restored
    # is untagged at this moment and is exactly what a dangling-image prune
    # would take. See prune_docker.
    write_status rolled_back 4 \
      "${version} did not come up healthy; ${installed} is running again"
    exit 4
  fi

  log "updated to ${version}"
}

rollback_compose() {
  local previous_id="$1" local_tag="$2"
  "$docker_bin" tag "$previous_id" "$local_tag" ||
    fail "ROLLBACK FAILED: could not retag ${previous_id} as ${local_tag}. The instance may be down; restore it by hand."
  compose up -d --no-build --force-recreate server ||
    fail "ROLLBACK FAILED: could not recreate the server service from ${previous_id}. The instance may be down; restore it by hand."
  log "rolled back to ${previous_id}"
}

# ---------------------------------------------------------------------------
# home mode
#
# The swap is one rename(2). There is deliberately no "remove the old binary,
# then put the new one in place" step: rename replaces the destination
# atomically, so the path is never absent and a crash lands on one whole
# binary or the other.
# ---------------------------------------------------------------------------

home_healthy() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT)) reported
  while [ "$SECONDS" -lt "$deadline" ]; do
    if "$binary" healthcheck >/dev/null 2>&1; then
      reported="$("$binary" --version 2>/dev/null |
        grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?' | head -n 1)" || reported=""
      if [ "$reported" = "$version" ]; then
        return 0
      fi
    fi
    sleep "$HEALTH_INTERVAL"
  done
  return 1
}

restart_home() {
  log "restarting: ${restart_command}"
  # Word-split on purpose: --restart-command is a command line, and the
  # default is one.
  #
  # A non-zero exit is logged and not fatal, deliberately: the health check
  # that follows is the arbiter of whether the new version is serving, and
  # letting set -e kill the run here would skip the rollback in exactly the
  # case that most needs it.
  # shellcheck disable=SC2086
  $restart_command ||
    log "the restart command exited non-zero — the health check decides what happens next"
}

apply_home() {
  local dir="$1" previous="${binary}.previous" extracted new_reported

  # Same directory as the target, so the move below is a rename within one
  # filesystem — across filesystems mv degrades to copy-then-unlink, which is
  # exactly the non-atomic swap this design exists to avoid.
  staging_dir="$(mktemp -d "$(dirname "$binary")/.hamlaneh-update.XXXXXX")"

  tar -xzf "${dir}/${asset}" -C "$staging_dir" ||
    fail "could not extract ${asset}"
  extracted="${staging_dir}/hamlaneh-server"
  [ -f "$extracted" ] ||
    fail "${asset} does not contain hamlaneh-server"
  chmod 0755 "$extracted"

  # Ask the new binary who it is BEFORE it is anywhere near the live path. A
  # tarball whose contents disagree with the release it was signed into is not
  # something to discover after the swap.
  new_reported="$(probe_version "$extracted")"
  [ "$new_reported" = "$version" ] ||
    fail "the binary in ${asset} reports ${new_reported}, not ${version}. Nothing was changed."

  cp -p "$binary" "$previous" ||
    fail "could not keep a copy of the installed binary at ${previous} — refusing to swap something this script cannot roll back"

  log "swapping in ${version}"
  mv -f "$extracted" "$binary" ||
    fail "could not move the new binary into ${binary}"

  restart_home

  if ! home_healthy; then
    log "the new binary did not report healthy as ${version} within ${HEALTH_TIMEOUT}s — rolling back"
    rollback_home "$previous"
    printf '[hamlaneh-update] ROLLED BACK: %s did not come up healthy; %s is running again.\n' \
      "$version" "$installed" >&2
    write_status rolled_back 4 \
      "${version} did not come up healthy; ${installed} is running again"
    exit 4
  fi

  log "updated to ${version} (previous binary kept at ${previous})"
}

rollback_home() {
  local previous="$1"
  mv -f "$previous" "$binary" ||
    fail "ROLLBACK FAILED: could not restore ${previous} to ${binary}. The instance may be down; restore it by hand."
  restart_home
  log "rolled back to ${installed}"
}

# ---------------------------------------------------------------------------
# The schedule
#
# What makes "auto-update, on by default for security patches" (ROADMAP Phase
# 4) true on a host. A systemd timer rather than a compose sidecar: a sidecar
# that swaps images needs the docker socket mounted into a container, which
# hands anything inside that container root on the host — an insecure default
# in a project whose first principle is that defaults carry the security load.
#
# The unit runs with no --channel argument, so it takes the default: security.
# ---------------------------------------------------------------------------
install_timer() {
  local unit_dir="/etc/systemd/system"
  command -v systemctl >/dev/null 2>&1 ||
    fail "systemd not found — this host needs another scheduler; run '${SCRIPT_PATH}' from cron or a launchd job instead"
  [ "$(id -u)" -eq 0 ] || fail "installing the timer needs root (try: sudo $0 --install-timer)"

  cat >"${unit_dir}/hamlaneh-update.service" <<EOF
[Unit]
Description=Hamlaneh auto-update (signed releases, security channel)
Documentation=https://github.com/${repo}/blob/main/docs/releasing.md
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=${SCRIPT_PATH}
# Runs as root: it swaps a binary in /usr/local/bin or drives docker. Nothing
# below can be tightened into ProtectSystem=full or =strict, because that is
# precisely the write this unit exists to perform.
NoNewPrivileges=true
# PrivateTmp gives this unit its own /tmp. That is why the lock at the top of
# this script is in /run: a lock under TMPDIR would be invisible across this
# boundary, and the timer would happily run alongside a manual invocation.
PrivateTmp=true
ProtectHome=read-only
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictRealtime=true
LockPersonality=true
EOF

  cat >"${unit_dir}/hamlaneh-update.timer" <<'EOF'
[Unit]
Description=Check for signed Hamlaneh updates

[Timer]
# Four times a day: PLAN.md §6.6 wants a security patch to be able to reach
# every instance in the same hour the advisory does, and a daily timer makes
# the worst case a day. The jitter keeps every instance in the world from
# asking GitHub the same question in the same second.
OnCalendar=*-*-* 00,06,12,18:00:00
RandomizedDelaySec=1h
Persistent=true

[Install]
WantedBy=timers.target
EOF

  systemctl daemon-reload
  systemctl enable --now hamlaneh-update.timer
  log "auto-update is on: hamlaneh-update.timer, security channel"
  log "status:  systemctl list-timers hamlaneh-update.timer"
  log "off:     systemctl disable --now hamlaneh-update.timer"

  install_request_watcher "$unit_dir"
}

# The half that makes the dashboard's button real (ADR 016 §3).
#
# A .path unit rather than a second timer, because the whole point of a
# button is that it does not wait: PathExists fires within a moment of the
# server writing the file. It also retriggers as long as the file is there,
# which is why serve_request deletes the request before acting on it.
#
# Resolving the directory needs the stack to be up, and at install time it
# is — install.sh calls this after wait_for_stack. Where it cannot be
# resolved, no unit is written and no watcher.json is stamped, so the server
# reports the feature as unavailable and draws no button. A control that
# quietly does nothing is worse than no control, because the operator then
# believes the instance is patched.
install_request_watcher() {
  local unit_dir="$1" dir
  if [ -z "$mode" ]; then mode="$(detect_mode 2>/dev/null || true)"; fi
  dir="$(resolve_state_dir)"
  if [ -z "$dir" ] || [ ! -d "$dir" ]; then
    log "no update state directory yet, so the dashboard's update control stays off."
    log "It appears once the stack has run with the update_state volume; re-run '$0 --install-timer' then."
    return 0
  fi

  cat >"${unit_dir}/hamlaneh-update-request.service" <<EOF
[Unit]
Description=Hamlaneh update requested from the admin dashboard
Documentation=https://github.com/${repo}/blob/main/docs/adr/016-operator-triggered-updates.md
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=${SCRIPT_PATH} --serve-request --state-dir ${dir}
# The same hardening the scheduled unit carries, and for the same reason: it
# does the same work. What it does NOT get is any additional authority for
# having been asked by a person rather than a clock.
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=read-only
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictRealtime=true
LockPersonality=true
EOF

  cat >"${unit_dir}/hamlaneh-update-request.path" <<EOF
[Unit]
Description=Watch for a Hamlaneh update requested from the admin dashboard

[Path]
PathExists=${dir}/request.json
Unit=hamlaneh-update-request.service

[Install]
WantedBy=paths.target
EOF

  systemctl daemon-reload
  systemctl enable --now hamlaneh-update-request.path
  stamp_watcher "$dir"
  log "the dashboard can now ask for an update: hamlaneh-update-request.path"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

detect_mode() {
  if [ -f "$compose_file" ] && [ -f "$env_file" ] &&
    command -v "$docker_bin" >/dev/null 2>&1 &&
    [ -n "$(compose_container)" ]; then
    printf 'compose'
    return
  fi
  if [ -x "$binary" ]; then
    printf 'home'
    return
  fi
  fail "cannot tell which deployment this is: no running compose 'server' service, and no executable at ${binary}. Pass --mode."
}

while [ $# -gt 0 ]; do
  case "$1" in
    --mode) mode="${2-}"; shift 2 ;;
    --channel) channel="${2-}"; shift 2 ;;
    --version) version="${2-}"; shift 2 ;;
    --installed) installed="${2-}"; shift 2 ;;
    --from-dir) from_dir="${2-}"; shift 2 ;;
    --asset) asset="${2-}"; shift 2 ;;
    --binary) binary="${2-}"; shift 2 ;;
    --restart-command) restart_command="${2-}"; shift 2 ;;
    --health-timeout) HEALTH_TIMEOUT="${2-}"; shift 2 ;;
    --compose-file) compose_file="${2-}"; shift 2 ;;
    --env-file) env_file="${2-}"; shift 2 ;;
    --repo) repo="${2-}"; shift 2 ;;
    --key) key="${2-}"; shift 2 ;;
    --cosign) cosign_bin="${2-}"; shift 2 ;;
    --docker) docker_bin="${2-}"; shift 2 ;;
    --state-dir) state_dir="${2-}"; shift 2 ;;
    # Not in the usage block on purpose: serve_request passes it to the child
    # it spawns, and nothing else should. It labels a status line; it selects
    # nothing and reaches no command.
    --request-id) request_id="${2-}"; shift 2 ;;
    --force) force=1; shift ;;
    --check) check_only=1; shift ;;
    --no-prune) no_prune=1; shift ;;
    --prune-only) prune_only=1; shift ;;
    --serve-request) serve_request_only=1; shift ;;
    --install-timer) install_timer_only=1; shift ;;
    -h | --help) usage; exit 0 ;;
    *) usage_error "unknown argument: $1" ;;
  esac
done

[ -f "$VERIFY" ] ||
  fail "verify-release.sh is not next to this script (${VERIFY}). This updater applies nothing it cannot verify."

repo_is_valid "$repo" || usage_error "--repo '${repo}' is not OWNER/NAME"

[[ "$HEALTH_TIMEOUT" =~ ^[0-9]+$ ]] ||
  usage_error "--health-timeout '${HEALTH_TIMEOUT}' is not a number of seconds"

case "$channel" in
  security | all) ;;
  *) usage_error "--channel must be 'security' or 'all', not '${channel}'" ;;
esac

run_started_at="$(now_utc)"

if [ "$install_timer_only" -eq 1 ]; then
  install_timer
  exit 0
fi

if [ "$serve_request_only" -eq 1 ]; then
  if [ -z "$mode" ]; then mode="$(detect_mode)"; fi
  serve_request
  exit 0
fi

if [ "$prune_only" -eq 1 ]; then
  if [ -z "$mode" ]; then mode="$(detect_mode 2>/dev/null || true)"; fi
  prune_docker
  exit 0
fi

require_tools sha256sum tar
if [ -z "$from_dir" ]; then require_tools curl; fi

if [ -z "$mode" ]; then mode="$(detect_mode)"; fi
case "$mode" in
  compose | home) ;;
  *) usage_error "--mode must be 'compose' or 'home', not '${mode}'" ;;
esac

if [ "$mode" = "compose" ] && [ -n "$key" ]; then
  usage_error "--key verifies the release artifacts against a public key, but the container image can only be checked against the keyless GitHub identity. Refusing to apply a compose update with half of it verified."
fi

take_lock

# What is installed. Asked of the server itself, never assumed.
if [ -z "$installed" ]; then
  if [ "$mode" = "compose" ]; then
    [ -f "$env_file" ] || fail "${env_file} not found — compose cannot resolve the stack without it"
    [ -n "$(compose_container)" ] ||
      fail "no running 'server' container. Start the stack, or pass --installed if you know the version."
    installed="$(probe_version compose exec -T server "$SERVER_BIN_IN_IMAGE")"
  else
    [ -x "$binary" ] || fail "${binary} is not an executable — pass --binary"
    installed="$(probe_version "$binary")"
  fi
fi
version_is_valid "$installed" ||
  usage_error "installed version '${installed}' is not a semantic version"
log "installed: ${installed} (${mode} mode)"

if [ -z "$version" ]; then
  require_tools curl
  version="$(latest_release)"
fi
version_is_valid "$version" ||
  fail "'${version}' is not a semantic version — refusing to fetch anything named by it"

if [ "$version" = "$installed" ]; then
  log "already on ${version} — nothing to do"
  # A run that found nothing still prunes. Four of these a day is what keeps
  # a long-lived host clean without a second timer to forget about.
  prune_docker
  version=""
  write_status idle 0 "up to date on ${installed}"
  exit 0
fi

# The channel gate. Not an ordering — see version_series.
if [ "$channel" = "security" ] &&
  [ "$(version_series "$version")" != "$(version_series "$installed")" ]; then
  log "${version} is available but outside the security channel (installed ${installed})."
  log "The security channel applies patch releases of ${installed%.*}.x only, which is where fixes are backported."
  log "Apply it deliberately with: $0 --channel all"
  available_outside_channel=1
  prune_docker
  write_status idle 0 "${version} is available but outside the ${channel} channel"
  exit 0
fi

if [ -z "$asset" ]; then asset="$(host_asset)"; fi

if [ "$check_only" -eq 1 ]; then
  log "would apply ${version} over ${installed} in ${mode} mode (--check: nothing changed)"
  write_status idle 0 "${version} is available (installed ${installed})"
  exit 0
fi

# From here the run does real work and takes real time — a download, a
# signature check, a container recreate. Saying so before starting is what
# lets the dashboard draw progress instead of an unexplained pause.
write_status running 0 "applying ${version} over ${installed}"

if [ -n "$from_dir" ]; then
  [ -d "$from_dir" ] || usage_error "--from-dir '${from_dir}' is not a directory"
  release_dir="$from_dir"
else
  release_dir="$(download_release)"
fi

verify_release "$release_dir"

if [ "$mode" = "compose" ]; then
  apply_compose
else
  apply_home "$release_dir"
fi

# Reached only when the new version came up healthy: both apply paths exit 4
# from inside on a rollback and never return here, which is what keeps the
# cleanup below away from the image a rollback just restored.
prune_docker
write_status succeeded 0 "updated to ${version}"
