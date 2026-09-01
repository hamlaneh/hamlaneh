#!/usr/bin/env bash
#
# Hamlaneh operator backups — ROADMAP Phase 4, "automated encrypted backups on
# by default; documented restore".
#
# THIS IS NOT ADR 010. ADR 010 is the *user's* MLS key backup: a blob sealed in
# the browser under a recovery key this server can never read, and it is
# already built. This script is the *operator's* instance backup: the database
# and the uploaded file bytes, encrypted under a key held by whoever runs the
# instance. Different data, different key, different owner, different threat.
# docs/backups.md opens by saying so, for the same reason this comment does.
#
# One script with subcommands rather than two files: backup and restore share
# the archive format, the key handling, the mode detection and the verifier,
# and splitting them duplicates all four in exactly the place where a drift
# between writer and reader is the one bug that quietly eats the backups.
#
# Dependencies, all already required by the install: openssl (install.sh
# refuses to run without it), tar, gzip, coreutils, and docker for server mode.
# Home mode additionally needs sqlite3. Nothing new joins the install surface —
# age and gpg were each rejected as a binary that would have to exist on every
# host to provide a property openssl already provides here.
#
# Usage:  hamlaneh-backup.sh <command> [options]   (run `help` for the list)

set -euo pipefail

SCRIPT_PATH="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
SCRIPT_DIR="$(dirname "$SCRIPT_PATH")"

# --- configuration ----------------------------------------------------------
#
# Every default is absolute and outside the git checkout on purpose. Archives
# under deploy/ would live inside the tree an update pulls into, and a key
# under deploy/ would be one `git clean -xdf` away from gone.

BACKUP_DIR="${HAMLANEH_BACKUP_DIR:-/var/backups/hamlaneh}"
KEY_FILE="${HAMLANEH_BACKUP_KEY_FILE:-/etc/hamlaneh/backup.key}"
KEEP="${HAMLANEH_BACKUP_KEEP:-14}"
BACKUP_AT="${HAMLANEH_BACKUP_AT:-03:00}"
COMPOSE_FILE="${HAMLANEH_COMPOSE_FILE:-${SCRIPT_DIR}/docker-compose.yml}"
HOME_DATA_DIR="${HAMLANEH_HOME_DATA_DIR:-}"
HOME_DB="${HAMLANEH_HOME_DB:-}"

ARCHIVE_FORMAT="hamlaneh-backup/1"
ARCHIVE_GLOB="hamlaneh-*.tar.gz.enc"
# The label is what keeps the fingerprint from being a bare hash of the key,
# published in a plaintext sidecar. The key is 256 random bits either way, so
# this is belt over braces; it costs one string.
FINGERPRINT_LABEL="hamlaneh-backup-key-fingerprint-v1"

MODE=""          # server | home, resolved by resolve_mode
FORCE=0
STAGE=""         # mktemp -d, removed by the EXIT trap
DB_CID=""        # memoized: resolving these shells out to docker compose
HELPER_IMAGE=""
FILE_VOLUME=""

log() { printf '[hamlaneh-backup] %s\n' "$*"; }
warn() { printf '[hamlaneh-backup] WARNING: %s\n' "$*" >&2; }

fail() {
  printf '[hamlaneh-backup] ERROR: %s\n' "$*" >&2
  exit 1
}

# Exit 3 is "refused on purpose" — a populated instance, a mode mismatch — as
# opposed to exit 1, "something went wrong". A scheduler can tell them apart,
# and so can the test suite.
refuse() {
  printf '[hamlaneh-backup] REFUSED: %s\n' "$*" >&2
  exit 3
}

# Same message either way; only the consequence differs. See verify_into_stage.
soft_or_fail() {
  local soft="$1"
  shift
  if [ "$soft" = "soft" ]; then
    # stdout, not stderr: the only soft caller is `scan`, whose whole output is
    # one report, and splitting it across two streams reorders it under a pipe.
    printf '[hamlaneh-backup] %s\n' "$*"
    return 0
  fi
  fail "$*"
}

cleanup() {
  if [ -n "$STAGE" ] && [ -d "$STAGE" ]; then
    rm -rf "$STAGE"
  fi
  return 0
}
trap cleanup EXIT

# make_stage creates the working directory the plaintext passes through.
#
# The plaintext IS on this disk for the duration of a backup, and that is not a
# gap being papered over: the database volume and the upload volume on this
# same host are plaintext all day. The encryption protects the archive once it
# LEAVES the machine — the copy on the NAS, in object storage, on the USB stick
# — which is the only place it meets something the host is not already exposed
# to. The staging directory is mode 700 and removed on every exit path.
make_stage() {
  cleanup
  umask 077
  STAGE="$(mktemp -d)"
  chmod 700 "$STAGE"
}

usage() {
  cat <<'EOF'
Hamlaneh operator backups: the database and the uploaded files, encrypted.

Usage: hamlaneh-backup.sh <command> [options]

Commands:
  run                     Take a backup now, verify it, prune to the retention
                          limit
  restore <archive>       Restore an archive. Verifies the whole archive before
                          touching anything, and refuses a populated instance
                          without --force
  verify <archive>        Decrypt and check the sealed manifest. Changes nothing
  scan <archive> --canary <string> [--canary ...]
                          Prove the archive carries no plaintext: the string
                          must be absent from the ciphertext and must come back
                          once the key is applied
  list                    List archives with their size and key fingerprint
  key                     Print the key fingerprint and the custody status
  key --show              Print the key itself, to copy off this machine
  key --confirm           Record that the key is stored somewhere else
  enable                  Install the daily systemd timer (cron fallback)
  disable                 Remove it
  help                    This text

Options:
  --mode server|home      Override auto-detection
  --force                 Restore over a populated instance
  --keep <n>              Retention count for this run (default 14)

Environment (all optional; the defaults are the zero-config install):
  HAMLANEH_BACKUP_DIR       archive directory   (/var/backups/hamlaneh)
  HAMLANEH_BACKUP_KEY_FILE  key file            (/etc/hamlaneh/backup.key)
  HAMLANEH_BACKUP_KEEP      archives to keep    (14)
  HAMLANEH_BACKUP_AT        daily time for the timer (03:00)
  HAMLANEH_COMPOSE_FILE     compose file        (next to this script)
  HAMLANEH_COMPOSE_ENV_FILE compose --env-file, when it is not deploy/.env
  HAMLANEH_HOME_DATA_DIR    home-mode data directory (selects home mode)
  HAMLANEH_HOME_DB          home-mode SQLite file (<data dir>/hamlaneh.db)
  HAMLANEH_BACKUP_NO_SCHEDULE=1  never install the timer as a side effect
EOF
}

# --- mode -------------------------------------------------------------------

resolve_mode() {
  if [ -n "$MODE" ]; then
    :
  elif [ -n "${HAMLANEH_BACKUP_MODE:-}" ]; then
    MODE="$HAMLANEH_BACKUP_MODE"
  elif [ -n "$HOME_DATA_DIR" ]; then
    MODE="home"
  elif [ -f "$COMPOSE_FILE" ]; then
    MODE="server"
  else
    fail "cannot tell which mode this is: no compose file at $COMPOSE_FILE, and HAMLANEH_HOME_DATA_DIR is unset"
  fi

  case "$MODE" in
    server)
      command -v docker >/dev/null 2>&1 || fail "server mode needs docker"
      ;;
    home)
      [ -n "$HOME_DATA_DIR" ] ||
        fail "home mode needs HAMLANEH_HOME_DATA_DIR — the data directory holding the SQLite file and the uploaded blobs"
      [ -n "$HOME_DB" ] || HOME_DB="${HOME_DATA_DIR}/hamlaneh.db"
      ;;
    *) fail "unknown mode '$MODE' (expected server or home)" ;;
  esac
}

# --- the key ----------------------------------------------------------------
#
# Custody is the whole problem. A key that only ever existed on the machine it
# protects is not a key, it is a delay: the disk dies, the key dies with it,
# and the archives that survived are noise. So the key is loud when it is born
# and stays loud on every run until the operator says they have moved it.

key_fingerprint() {
  { cat "$KEY_FILE"; printf '%s' "$FINGERPRINT_LABEL"; } |
    openssl dgst -sha256 -r | cut -c1-16
}

custody_marker() { printf '%s.custody-confirmed' "$KEY_FILE"; }

announce_new_key() {
  cat >&2 <<EOF

================================================================
  A NEW BACKUP KEY WAS CREATED
    file:        $KEY_FILE
    fingerprint: $(key_fingerprint)

  This key is the only thing that opens your backups, and right
  now it exists only on the machine it protects — so it does NOT
  survive that machine dying, which is the event a backup is for.

  Copy it somewhere else now:
      $SCRIPT_PATH key --show

  Then record that you have:
      $SCRIPT_PATH key --confirm

  Until you do, every backup repeats this warning.
================================================================

EOF
}

ensure_key() {
  if [ -f "$KEY_FILE" ]; then
    [ -s "$KEY_FILE" ] || fail "$KEY_FILE is empty — refusing to encrypt with nothing"
    return 0
  fi
  command -v openssl >/dev/null 2>&1 || fail "openssl is required; install it and re-run"
  local dir
  dir="$(dirname "$KEY_FILE")"
  mkdir -p "$dir" || fail "cannot create $dir for the backup key (root?)"
  chmod 700 "$dir" 2>/dev/null || true
  ( umask 077; openssl rand -base64 32 > "$KEY_FILE" ) ||
    fail "cannot write the backup key to $KEY_FILE (root?)"
  chmod 600 "$KEY_FILE"
  announce_new_key
}

require_key() {
  [ -f "$KEY_FILE" ] ||
    fail "no backup key at $KEY_FILE — an archive cannot be opened without the key it was sealed with"
  [ -s "$KEY_FILE" ] || fail "$KEY_FILE is empty"
}

nag_if_custody_unconfirmed() {
  if [ ! -f "$(custody_marker)" ]; then
    warn "the backup key at $KEY_FILE has not been copied off this machine (fingerprint $(key_fingerprint)). Run '$SCRIPT_PATH key --show', store it elsewhere, then '$SCRIPT_PATH key --confirm'."
  fi
  return 0
}

cmd_key() {
  case "${1:-}" in
    --show)
      require_key
      # The one place the key is ever printed, because printing it is the
      # entire point of the subcommand. Nothing else in this script — no log
      # line, no error, no command line visible in ps — carries it.
      cat "$KEY_FILE"
      ;;
    --confirm)
      require_key
      : > "$(custody_marker)"
      chmod 600 "$(custody_marker)"
      log "custody recorded for key fingerprint $(key_fingerprint). Nothing here verified that you actually stored it; this only silences the warning."
      ;;
    ""|--status)
      require_key
      printf 'key file:    %s\n' "$KEY_FILE"
      printf 'fingerprint: %s\n' "$(key_fingerprint)"
      if [ -f "$(custody_marker)" ]; then
        printf 'custody:     confirmed by the operator\n'
      else
        printf 'custody:     NOT CONFIRMED — the key exists only on this machine\n'
      fi
      ;;
    *) fail "unknown option for 'key': $1" ;;
  esac
}

# --- crypto -----------------------------------------------------------------
#
# AES-256-CBC with PBKDF2 over a key file that already holds 256 random bits,
# so the iteration count is not what carries the security here: it is present
# because openssl's key derivation wants one, not because a human passphrase is
# being stretched.
#
# Integrity does not come from the cipher. `openssl enc` offers no usable AEAD
# mode, so the archive carries `checksums` — SHA-256 over every member — SEALED
# INSIDE the ciphertext. Nobody without the key can rewrite it to match
# tampered content, and no restore acts on an archive whose checksums have not
# verified in full. Weaker than an AEAD in theory (a forgery is caught after
# decryption rather than before) and equivalent in the way that matters here:
# nothing is written to the instance until the archive has proved intact.
encrypt_stream() {
  openssl enc -aes-256-cbc -md sha256 -pbkdf2 -iter 200000 -salt -pass file:"$KEY_FILE"
}

decrypt_stream() {
  openssl enc -d -aes-256-cbc -md sha256 -pbkdf2 -iter 200000 -pass file:"$KEY_FILE"
}

# --- server mode plumbing ---------------------------------------------------
#
# Everything below resolves names from the running stack rather than hardcoding
# them, so an install whose compose project was renamed still works.

compose() {
  if [ -n "${HAMLANEH_COMPOSE_ENV_FILE:-}" ]; then
    docker compose -f "$COMPOSE_FILE" --env-file "$HAMLANEH_COMPOSE_ENV_FILE" "$@"
  else
    docker compose -f "$COMPOSE_FILE" "$@"
  fi
}

db_container() {
  if [ -z "$DB_CID" ]; then
    DB_CID="$(compose ps -q db 2>/dev/null || true)"
    [ -n "$DB_CID" ] ||
      fail "the database container is not running — start the stack (docker compose up -d) and re-run"
  fi
  printf '%s' "$DB_CID"
}

# The helper image for touching the file volume is the image the database is
# already running: pinned by digest in docker-compose.yml, already pulled, and
# it carries tar, find and sha256sum. Nothing new is downloaded, and the helper
# inherits the stack's pin instead of introducing a second one.
helper_image() {
  if [ -z "$HELPER_IMAGE" ]; then
    HELPER_IMAGE="$(docker inspect -f '{{.Config.Image}}' "$(db_container)")"
  fi
  printf '%s' "$HELPER_IMAGE"
}

file_volume() {
  if [ -z "$FILE_VOLUME" ]; then
    local project
    project="$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$(db_container)")"
    FILE_VOLUME="$(docker volume ls -q \
      --filter "label=com.docker.compose.project=${project}" \
      --filter "label=com.docker.compose.volume=file_data" | head -n 1)"
    if [ -z "$FILE_VOLUME" ]; then
      FILE_VOLUME="${project}_file_data"
      docker volume inspect "$FILE_VOLUME" >/dev/null 2>&1 ||
        fail "cannot find the uploaded-files volume for compose project '$project'"
    fi
  fi
  printf '%s' "$FILE_VOLUME"
}

# Runs as root inside a throwaway container on purpose: the blob tree is mode
# 700 owned by uid 65532, so both reading it and restoring its ownership need a
# uid that is not one of 65532's peers. --entrypoint sh bypasses the postgres
# image's entrypoint rather than relying on its passthrough. No network, no
# ports, one command, gone.
volume_helper() {
  local mount="$1" script="$2"
  docker run --rm -i --network none --entrypoint sh \
    -v "$mount" "$(helper_image)" -c "$script"
}

# --- backup -----------------------------------------------------------------

archive_members() {
  case "$MODE" in
    server) printf 'manifest\ndatabase.sql\nfiles.tar\nfiles.sha256\n' ;;
    home) printf 'manifest\ndatabase.sqlite\nfiles.tar\nfiles.sha256\n' ;;
  esac
}

write_manifest() {
  cat > "$STAGE/manifest" <<EOF
format: $ARCHIVE_FORMAT
mode: $MODE
created: $(date -u +%Y-%m-%dT%H:%M:%SZ)
key-fingerprint: $(key_fingerprint)
EOF
}

collect_server() {
  local cid vol
  cid="$(db_container)"
  vol="$(file_volume)"

  log "dumping the database ..."
  # No password anywhere: the official postgres image trusts local socket
  # connections, which is what the compose healthcheck already relies on.
  # --clean --if-exists is what lets the restore be one transaction that either
  # replaces the schema or changes nothing.
  docker exec -i "$cid" pg_dump -U hamlaneh -d hamlaneh \
    --clean --if-exists --no-owner --no-privileges > "$STAGE/database.sql"

  log "checksumming the uploaded files ..."
  volume_helper "${vol}:/data:ro" \
    'cd /data && find . -type f -print | LC_ALL=C sort | tr "\n" "\0" | xargs -0 -r sha256sum' \
    > "$STAGE/files.sha256"

  log "archiving the uploaded files ..."
  volume_helper "${vol}:/data:ro" 'tar -cf - -C /data .' > "$STAGE/files.tar"
}

collect_home() {
  command -v sqlite3 >/dev/null 2>&1 ||
    fail "home mode needs the sqlite3 command. A live SQLite file copied byte for byte is not a backup, it is a torn page waiting to be discovered at restore time. Install sqlite3 and re-run."
  [ -d "$HOME_DATA_DIR" ] || fail "no data directory at $HOME_DATA_DIR"
  [ -f "$HOME_DB" ] || fail "no SQLite database at $HOME_DB"

  log "backing up the SQLite database (online backup API) ..."
  # .backup, not cp: it takes a consistent snapshot of a database that may be
  # being written to, which is the entire difference between a backup and a
  # file.
  sqlite3 "$HOME_DB" ".backup '$STAGE/database.sqlite'"

  local dbname
  dbname="$(basename "$HOME_DB")"

  log "checksumming the uploaded files ..."
  ( cd "$HOME_DATA_DIR" &&
    find . -type f \
      ! -name "$dbname" ! -name "${dbname}-wal" ! -name "${dbname}-shm" -print |
    LC_ALL=C sort | tr '\n' '\0' | xargs -0 -r sha256sum ) > "$STAGE/files.sha256"

  log "archiving the uploaded files ..."
  tar -cf "$STAGE/files.tar" -C "$HOME_DATA_DIR" \
    --exclude="./$dbname" --exclude="./${dbname}-wal" --exclude="./${dbname}-shm" .
}

# Retention has one job that matters more than saving disk: the failure where
# an instance goes down BECAUSE of its own backups. Pruning runs before a
# backup (so yesterday's pile does not stop today's) and again after.
prune() {
  local kept=0 removed=0 archive
  # Names carry UTC timestamps, so reverse lexicographic order is newest first.
  while IFS= read -r archive; do
    [ -n "$archive" ] || continue
    kept=$((kept + 1))
    if [ "$kept" -gt "$KEEP" ]; then
      rm -f "$archive" "${archive%.enc}.meta"
      removed=$((removed + 1))
    fi
  done < <(find "$BACKUP_DIR" -maxdepth 1 -type f -name "$ARCHIVE_GLOB" | LC_ALL=C sort -r)
  if [ "$removed" -gt 0 ]; then
    log "pruned $removed archive(s), keeping the newest $KEEP"
  fi
  return 0
}

largest_archive_bytes() {
  local archive size largest=0
  while IFS= read -r archive; do
    [ -n "$archive" ] || continue
    size="$(wc -c < "$archive" | tr -d ' ')"
    [ "$size" -gt "$largest" ] && largest="$size"
  done < <(find "$BACKUP_DIR" -maxdepth 1 -type f -name "$ARCHIVE_GLOB")
  printf '%s' "$largest"
}

check_space() {
  local largest avail need
  largest="$(largest_archive_bytes)"
  [ "$largest" -gt 0 ] || return 0   # first run: nothing to extrapolate from
  avail="$(df -Pk "$BACKUP_DIR" | awk 'NR==2 {printf "%.0f", $4 * 1024}')"
  need=$((largest * 2))
  if [ "$avail" -lt "$need" ]; then
    fail "not enough free space in $BACKUP_DIR: $((avail / 1048576)) MiB available, about $((need / 1048576)) MiB needed. Nothing was dumped. Lower HAMLANEH_BACKUP_KEEP (currently $KEEP) or give the volume more room."
  fi
}

cmd_run() {
  resolve_mode
  [ "$KEEP" -ge 1 ] 2>/dev/null || fail "--keep/HAMLANEH_BACKUP_KEEP must be a positive integer, got '$KEEP'"
  mkdir -p "$BACKUP_DIR" || fail "cannot create $BACKUP_DIR (root?)"
  chmod 700 "$BACKUP_DIR" 2>/dev/null || true
  ensure_key
  nag_if_custody_unconfirmed
  prune
  check_space
  make_stage

  case "$MODE" in
    server) collect_server ;;
    home) collect_home ;;
  esac

  write_manifest
  local members=()
  while IFS= read -r member; do members+=("$member"); done < <(archive_members)
  ( cd "$STAGE" && sha256sum "${members[@]}" > checksums )

  local stamp archive
  stamp="$(date -u +%Y%m%dT%H%M%SZ)"
  archive="${BACKUP_DIR}/hamlaneh-${MODE}-${stamp}.tar.gz.enc"

  log "encrypting ..."
  ( cd "$STAGE" && tar -czf - checksums "${members[@]}" ) | encrypt_stream > "${archive}.part"
  mv "${archive}.part" "$archive"
  chmod 600 "$archive"

  cat > "${archive%.enc}.meta" <<EOF
# Plaintext on purpose: this says WHICH key opens the archive beside it, and
# carries nothing an attacker holding the archive does not already have.
format: $ARCHIVE_FORMAT
mode: $MODE
created: $(date -u +%Y-%m-%dT%H:%M:%SZ)
key-fingerprint: $(key_fingerprint)
key-file: $KEY_FILE
cipher: aes-256-cbc/pbkdf2-sha256
EOF
  chmod 600 "${archive%.enc}.meta"

  # An archive nobody has ever read back is a hope, not a backup. This costs
  # one decrypt pass per run and is the difference between "a file appeared"
  # and "a restorable archive exists".
  verify_into_stage "$archive"

  prune
  ensure_schedule
  log "backup complete and verified: $archive ($(du -h "$archive" | cut -f1))"
}

# --- verify / scan ----------------------------------------------------------

# Decrypts an archive into $STAGE and checks every sealed checksum. Every path
# that changes the instance calls this FIRST, which is what makes a wrong key a
# refusal rather than a half-restore: the failure happens before anything has
# been stopped, dropped or overwritten.
#
# The second argument is "soft" for callers that want a return code instead of
# an exit — only `scan`, which reports every failing check rather than stopping
# at the first. Every caller that CHANGES something uses the hard form, because
# there the right response to an unopenable archive is to stop.
verify_into_stage() {
  local archive="$1" soft="${2:-hard}"
  [ -f "$archive" ] || fail "no such archive: $archive"
  require_key
  make_stage

  if ! decrypt_stream < "$archive" 2>/dev/null | tar -xzf - -C "$STAGE" 2>/dev/null; then
    soft_or_fail "$soft" "cannot open $archive with the key at $KEY_FILE. This is what a wrong key looks like: the archive was never decrypted, so nothing on this instance was read, stopped or written. Compare the fingerprint in ${archive%.enc}.meta with '$SCRIPT_PATH key'."
    return 1
  fi

  if [ ! -f "$STAGE/manifest" ] || [ ! -f "$STAGE/checksums" ]; then
    soft_or_fail "$soft" "$archive decrypted but is not a Hamlaneh backup (no manifest)"
    return 1
  fi

  local fmt
  fmt="$(sed -n 's/^format: //p' "$STAGE/manifest")"
  if [ "$fmt" != "$ARCHIVE_FORMAT" ]; then
    soft_or_fail "$soft" "archive format '$fmt' is not $ARCHIVE_FORMAT — this script cannot read it"
    return 1
  fi

  if ! ( cd "$STAGE" && sha256sum --quiet --check checksums ); then
    soft_or_fail "$soft" "$archive failed its own checksums: the contents do not match the manifest sealed inside it. Nothing was restored."
    return 1
  fi
}

cmd_verify() {
  local archive="${1:-}"
  [ -n "$archive" ] || fail "verify needs an archive path"
  verify_into_stage "$archive"
  sed 's/^/  /' "$STAGE/manifest"
  local files
  files="$(wc -l < "$STAGE/files.sha256" | tr -d ' ')"
  log "$archive verifies: manifest intact, $files file(s) checksummed, nothing changed"
}

# scan proves the negative the Phase 4 gate names: "backup archive unreadable
# without key (scripted scan finds no plaintext canary)".
#
# Four checks, because the obvious one alone is worthless. Grepping ciphertext
# for a string finds nothing — but grepping a plain *gzip* for a string also
# finds nothing, so a build that silently skipped encryption would sail through
# a bare canary grep. The container checks and the positive control are what
# make the absence mean something:
#
#   1. the canary is absent from the archive bytes
#   2. the archive is not a gzip or a tar wearing an .enc suffix
#   3. the archive carries openssl's Salted__ header
#   4. with the key the canary comes BACK — so check 1 was not passing
#      vacuously over an empty or unrelated file
cmd_scan() {
  local archive="" failures=0 c magic
  local canaries=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --canary)
        [ $# -ge 2 ] || fail "--canary needs a value"
        canaries+=("$2")
        shift 2
        ;;
      -*) fail "unknown option for 'scan': $1" ;;
      *) archive="$1"; shift ;;
    esac
  done
  [ -n "$archive" ] || fail "scan needs an archive path"
  [ -f "$archive" ] || fail "no such archive: $archive"
  [ "${#canaries[@]}" -gt 0 ] ||
    fail "scan needs at least one --canary <string>; a scan with nothing to look for proves nothing"

  for c in "${canaries[@]}"; do
    if LC_ALL=C grep -a -q -F -e "$c" "$archive"; then
      printf 'FAIL  canary present in the ciphertext: %s\n' "$c"
      failures=$((failures + 1))
    else
      printf 'ok    canary absent from the ciphertext: %s\n' "$c"
    fi
  done

  # Read the first eight bytes as hex, once. Never as a string: an archive is
  # binary, and a command substitution silently drops NUL bytes, which would
  # make a string comparison here quietly answer the wrong question.
  magic="$(head -c 8 "$archive" | od -An -tx1 | tr -d ' \n')"
  if [ "${magic:0:4}" = "1f8b" ]; then
    printf 'FAIL  archive begins with gzip magic — it is not encrypted at all\n'
    failures=$((failures + 1))
  else
    printf 'ok    archive is not a bare gzip stream\n'
  fi

  if tar -tf "$archive" >/dev/null 2>&1; then
    printf 'FAIL  archive reads as a tar without any key\n'
    failures=$((failures + 1))
  else
    printf 'ok    archive does not read as a tar without a key\n'
  fi

  if [ "$magic" = "53616c7465645f5f" ]; then   # "Salted__"
    printf 'ok    archive carries the openssl salted header\n'
  else
    printf 'FAIL  archive does not carry the openssl salted header\n'
    failures=$((failures + 1))
  fi

  # The positive control. Without it, "the canary is not in this file" is also
  # true of every file that is not this backup. Soft verification, so a scan
  # reports every failing check instead of stopping at the first.
  if [ ! -f "$KEY_FILE" ]; then
    printf 'FAIL  no key at %s, so the positive control cannot run and the absences above prove nothing\n' "$KEY_FILE"
    failures=$((failures + 1))
  elif ! verify_into_stage "$archive" soft; then
    printf 'FAIL  the archive did not open and verify, so the positive control could not run and the absences above prove nothing\n'
    failures=$((failures + 1))
  else
    for c in "${canaries[@]}"; do
      if LC_ALL=C grep -a -r -q -F -e "$c" "$STAGE"; then
        printf 'ok    canary recovered from the decrypted archive: %s\n' "$c"
      else
        printf 'FAIL  canary is not in the decrypted archive either — the scan above was vacuous: %s\n' "$c"
        failures=$((failures + 1))
      fi
    done
  fi

  if [ "$failures" -gt 0 ]; then
    printf '\n%d check(s) failed.\n' "$failures"
    exit 1
  fi
  printf '\nAll checks passed.\n'
}

cmd_list() {
  mkdir -p "$BACKUP_DIR"
  local archive meta fingerprint found=0
  while IFS= read -r archive; do
    [ -n "$archive" ] || continue
    found=1
    meta="${archive%.enc}.meta"
    if [ -f "$meta" ]; then
      fingerprint="$(sed -n 's/^key-fingerprint: //p' "$meta")"
    else
      fingerprint="unknown"
    fi
    printf '%s  %6s  key %s\n' "$(basename "$archive")" \
      "$(du -h "$archive" | cut -f1)" "$fingerprint"
  done < <(find "$BACKUP_DIR" -maxdepth 1 -type f -name "$ARCHIVE_GLOB" | LC_ALL=C sort -r)
  [ "$found" -eq 1 ] || log "no archives in $BACKUP_DIR"
}

# --- restore ----------------------------------------------------------------

instance_is_populated() {
  local cid count
  case "$MODE" in
    server)
      cid="$(db_container)"
      # A missing users table means a fresh instance, not an error.
      count="$(docker exec -i "$cid" psql -U hamlaneh -d hamlaneh -tAc \
        'select count(*) from users' 2>/dev/null || echo 0)"
      [ "${count:-0}" -gt 0 ] 2>/dev/null && return 0
      ;;
    home)
      [ -f "$HOME_DB" ] && [ -s "$HOME_DB" ] && return 0
      ;;
  esac
  return 1
}

restore_server() {
  local cid vol
  cid="$(db_container)"
  vol="$(file_volume)"

  log "stopping the application server (the database stays up — it is what is being written to) ..."
  compose stop server >/dev/null

  # Staged first, swapped last: the verified tree is unpacked beside the live
  # one inside the same volume, so the moment the old files stop existing comes
  # after the new ones already do.
  log "staging the restored files inside the volume ..."
  volume_helper "${vol}:/data" 'rm -rf /data/.hamlaneh-restore && mkdir -p /data/.hamlaneh-restore' </dev/null
  volume_helper "${vol}:/data" 'tar -xf - -C /data/.hamlaneh-restore' < "$STAGE/files.tar"

  log "restoring the database in a single transaction ..."
  if ! docker exec -i "$cid" psql -U hamlaneh -d hamlaneh -q \
      -v ON_ERROR_STOP=1 --single-transaction < "$STAGE/database.sql"; then
    volume_helper "${vol}:/data" 'rm -rf /data/.hamlaneh-restore' </dev/null || true
    compose start server >/dev/null
    fail "the database restore failed and rolled back. The instance still holds the data it had, the staged files were discarded, and the server was restarted."
  fi

  log "swapping the restored files into place ..."
  volume_helper "${vol}:/data" '
    set -e
    find /data -mindepth 1 -maxdepth 1 ! -name ".hamlaneh-restore" -exec rm -rf {} +
    find /data/.hamlaneh-restore -mindepth 1 -maxdepth 1 -exec mv {} /data/ \;
    rmdir /data/.hamlaneh-restore' </dev/null

  log "starting the application server ..."
  compose start server >/dev/null
}

restore_home() {
  warn "home mode cannot stop the Hamlaneh process for you. If it is running, stop it before continuing — restoring under a live writer is how a database ends up with two histories."
  mkdir -p "$HOME_DATA_DIR"

  local dbname
  dbname="$(basename "$HOME_DB")"

  log "staging the restored files ..."
  rm -rf "${HOME_DATA_DIR}/.hamlaneh-restore"
  mkdir -p "${HOME_DATA_DIR}/.hamlaneh-restore"
  tar -xf "$STAGE/files.tar" -C "${HOME_DATA_DIR}/.hamlaneh-restore"

  log "restoring the database ..."
  cp "$STAGE/database.sqlite" "${HOME_DB}.restored"
  mv "${HOME_DB}.restored" "$HOME_DB"
  # A WAL and a shared-memory file belonging to the database that is gone
  # would otherwise be replayed on top of the one that just arrived.
  rm -f "${HOME_DB}-wal" "${HOME_DB}-shm"

  log "swapping the restored files into place ..."
  find "$HOME_DATA_DIR" -mindepth 1 -maxdepth 1 \
    ! -name '.hamlaneh-restore' ! -name "$dbname" \
    ! -name "${dbname}-wal" ! -name "${dbname}-shm" -exec rm -rf {} +
  find "${HOME_DATA_DIR}/.hamlaneh-restore" -mindepth 1 -maxdepth 1 \
    -exec mv {} "$HOME_DATA_DIR/" \;
  rmdir "${HOME_DATA_DIR}/.hamlaneh-restore"
}

cmd_restore() {
  local archive="${1:-}"
  [ -n "$archive" ] || fail "restore needs an archive path"
  resolve_mode

  # Order is the safety property. Verification happens before the populated
  # check, and both happen before anything is stopped or written.
  verify_into_stage "$archive"

  local archive_mode
  archive_mode="$(sed -n 's/^mode: //p' "$STAGE/manifest")"
  [ "$archive_mode" = "$MODE" ] ||
    refuse "this is a '$archive_mode' archive and this instance is '$MODE'. Restoring one into the other produces an instance that is neither."

  if [ "$FORCE" -ne 1 ] && instance_is_populated; then
    refuse "this instance already holds data. A restore replaces the database and every uploaded file. Re-run with --force if that is what you mean."
  fi

  case "$MODE" in
    server) restore_server ;;
    home) restore_home ;;
  esac

  log "restore complete from $archive"
  log "the archive's per-file checksums are in files.sha256 inside it; '$SCRIPT_PATH verify $archive' reports the count"
}

# --- schedule ---------------------------------------------------------------
#
# "On by default" is the roadmap's wording and it means a schedule nobody had
# to ask for. This is as far as this script reaches on its own: `run` installs
# the timer when it is absent and it can. The line that makes it true from the
# very first `docker compose up` belongs in install.sh, which this script
# deliberately does not edit — see docs/backups.md.

systemd_unit() { printf '/etc/systemd/system/hamlaneh-backup.service'; }
systemd_timer() { printf '/etc/systemd/system/hamlaneh-backup.timer'; }
cron_file() { printf '/etc/cron.d/hamlaneh-backup'; }

install_schedule() {
  if command -v systemctl >/dev/null 2>&1 && [ -d /etc/systemd/system ]; then
    cat > "$(systemd_unit)" <<EOF
[Unit]
Description=Hamlaneh encrypted backup
After=docker.service
Wants=docker.service

[Service]
Type=oneshot
ExecStart=$SCRIPT_PATH run
EOF
    cat > "$(systemd_timer)" <<EOF
[Unit]
Description=Daily Hamlaneh encrypted backup

[Timer]
OnCalendar=*-*-* ${BACKUP_AT}:00
# Spreads the load, and on a fleet stops every instance dumping at once.
RandomizedDelaySec=1800
# Catches up after the machine was off at ${BACKUP_AT}, which is exactly when
# a household instance is most likely to have been off.
Persistent=true

[Install]
WantedBy=timers.target
EOF
    systemctl daemon-reload
    systemctl enable --now hamlaneh-backup.timer >/dev/null
    log "daily backup timer installed (hamlaneh-backup.timer, ${BACKUP_AT} plus up to 30m jitter)"
    return 0
  fi

  if [ -d /etc/cron.d ]; then
    local hour minute
    hour="${BACKUP_AT%%:*}"
    minute="${BACKUP_AT##*:}"
    printf '%s %s * * * root %s run >/dev/null\n' \
      "${minute#0}" "${hour#0}" "$SCRIPT_PATH" > "$(cron_file)"
    chmod 644 "$(cron_file)"
    log "daily backup cron entry installed ($(cron_file), ${BACKUP_AT})"
    return 0
  fi

  return 1
}

# Idempotent and quiet: called from every `run`, so a manually taken backup is
# also the moment the schedule starts existing.
ensure_schedule() {
  if [ "${HAMLANEH_BACKUP_NO_SCHEDULE:-0}" = "1" ]; then return 0; fi
  if [ "$(id -u)" -ne 0 ]; then return 0; fi
  if [ -f "$(systemd_timer)" ] || [ -f "$(cron_file)" ]; then return 0; fi
  install_schedule || true
  return 0
}

cmd_enable() {
  [ "$(id -u)" -eq 0 ] || fail "enable needs root: it writes a systemd unit or a cron entry"
  install_schedule ||
    fail "no systemd and no /etc/cron.d on this host — schedule '$SCRIPT_PATH run' with whatever this system uses"
}

cmd_disable() {
  [ "$(id -u)" -eq 0 ] || fail "disable needs root"
  if command -v systemctl >/dev/null 2>&1 && [ -f "$(systemd_timer)" ]; then
    systemctl disable --now hamlaneh-backup.timer >/dev/null 2>&1 || true
    rm -f "$(systemd_timer)" "$(systemd_unit)"
    systemctl daemon-reload
    log "backup timer removed"
  fi
  if [ -f "$(cron_file)" ]; then
    rm -f "$(cron_file)"
    log "backup cron entry removed"
  fi
  warn "backups are off. The archives already in $BACKUP_DIR are untouched, and nothing will prune them again."
}

# --- entry point ------------------------------------------------------------

main() {
  local command="${1:-help}"
  if [ $# -gt 0 ]; then shift; fi

  local args=()
  while [ $# -gt 0 ]; do
    case "$1" in
      --mode)
        [ $# -ge 2 ] || fail "--mode needs a value"
        MODE="$2"
        shift 2
        ;;
      --force)
        FORCE=1
        shift
        ;;
      --keep)
        [ $# -ge 2 ] || fail "--keep needs a value"
        KEEP="$2"
        shift 2
        ;;
      *)
        args+=("$1")
        shift
        ;;
    esac
  done
  if [ "${#args[@]}" -gt 0 ]; then set -- "${args[@]}"; else set --; fi

  case "$command" in
    run) cmd_run "$@" ;;
    restore) cmd_restore "$@" ;;
    verify) cmd_verify "$@" ;;
    scan) cmd_scan "$@" ;;
    list) cmd_list "$@" ;;
    key) cmd_key "$@" ;;
    enable) cmd_enable ;;
    disable) cmd_disable ;;
    help|-h|--help) usage ;;
    *)
      usage >&2
      fail "unknown command: $command"
      ;;
  esac
}

main "$@"
