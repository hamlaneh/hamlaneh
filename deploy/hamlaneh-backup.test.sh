#!/usr/bin/env bash
#
# Tests for deploy/hamlaneh-backup.sh — ROADMAP.md Phase 4 test gate item 3:
#
#   "Restore drill on a fresh machine: pre-backup canary message present, file
#    checksums match, existing users log in. Negatives: backup archive
#    unreadable without key (scripted scan finds no plaintext canary); restore
#    with wrong key fails with a clear error, not a corrupt instance."
#
# Every clause above is a check below, and the negatives are *run*, not
# asserted: a canary string is planted in the database and in an uploaded file
# before the backup, and the scan that must not find it is the same scan an
# operator runs.
#
# What is real and what is not, stated here rather than left to be assumed:
#
#   REAL  PostgreSQL. The server leg runs a real postgres container from the
#         same digest-pinned image docker-compose.yml uses, and the dump and
#         restore are real pg_dump and psql against it.
#   REAL  the encryption. openssl seals the archive and only the right key
#         opens it; the wrong-key negative uses a second real random key.
#   REAL  the file volume. Uploaded bytes live on a real docker volume owned by
#         uid 65532, exactly as deploy/Dockerfile ships it, and are archived
#         and restored through the same throwaway-container path production
#         uses.
#   REAL  SQLite, in the home leg: sqlite3's online .backup, a real database
#         file, a real restore.
#   NOT   the application. There is no Go server here, so "existing users log
#         in" is proved one layer down: the users row and its argon2id hash
#         come back byte for byte, which is what a login reads. The
#         application-level drill is compose-smoke plus the e2e suite against
#         a restored stack, and docs/backups.md says so.
#   NOT   the schedule. HAMLANEH_BACKUP_NO_SCHEDULE=1 is set throughout: these
#         tests must never write a systemd unit onto the machine running them.
#
# Requires docker (server leg) and sqlite3 (home leg). It does not skip when
# they are missing — a green run against a missing tool is the exact theatre
# this file exists to avoid. Run one leg at a time to work around that:
#
#   bash deploy/hamlaneh-backup.test.sh            # both legs
#   bash deploy/hamlaneh-backup.test.sh server
#   bash deploy/hamlaneh-backup.test.sh home

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKUP="$SCRIPT_DIR/hamlaneh-backup.sh"
PROJECT="hamlaneh-backup-test-$$"

LEG="${1:-both}"
case "$LEG" in both|server|home) ;; *) printf 'usage: %s [both|server|home]\n' "$0" >&2; exit 2 ;; esac

[ -f "$BACKUP" ] || { printf 'ERROR: %s not found\n' "$BACKUP" >&2; exit 2; }

if [ "$LEG" != "home" ] && ! command -v docker >/dev/null 2>&1; then
  printf 'ERROR: docker not found; the server leg drives a real PostgreSQL container.\n' >&2
  exit 2
fi
if [ "$LEG" != "server" ] && ! command -v sqlite3 >/dev/null 2>&1; then
  printf 'ERROR: sqlite3 not found; the home leg backs up a real SQLite database.\n' >&2
  exit 2
fi

WORK="$(mktemp -d)"
COMPOSE_FILE="$WORK/docker-compose.yml"

cleanup() {
  if [ "$LEG" != "home" ] && [ -f "$COMPOSE_FILE" ]; then
    docker compose -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

checks=0
failures=0

section() { printf '\n=== %s ===\n' "$*"; }

pass() { checks=$((checks + 1)); printf '  ok    %s\n' "$1"; }

bad() {
  checks=$((checks + 1))
  failures=$((failures + 1))
  printf '  FAIL  %s\n' "$1"
  if [ $# -gt 1 ]; then printf '          %s\n' "$2"; fi
}

# check <name> <want-exit> <want-substring> <command...>
#
# The substring is not decoration. Every negative here can also be made to exit
# non-zero by breaking something unrelated, and a test that only asserts "it
# failed" goes green for the wrong reason and stays green while the control it
# names quietly stops working.
check() {
  local name="$1" want="$2" want_msg="$3"
  shift 3
  local out rc=0
  out="$("$@" 2>&1)" || rc=$?
  if [ "$rc" -ne "$want" ]; then
    bad "$name" "exit $rc, wanted $want: $(printf '%s' "$out" | tail -n 3 | tr '\n' ' ')"
    return 0
  fi
  if [ -n "$want_msg" ] && ! printf '%s' "$out" | grep -qF -- "$want_msg"; then
    bad "$name" "exit $rc as wanted, but the output never said '$want_msg'"
    return 0
  fi
  pass "$name"
}

assert_eq() {
  local name="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else bad "$name" "got '$got', wanted '$want'"; fi
}

assert_contains() {
  local name="$1" haystack="$2" needle="$3"
  if printf '%s' "$haystack" | grep -qF -- "$needle"; then
    pass "$name"
  else
    bad "$name" "output did not contain '$needle'"
  fi
}

assert_absent() {
  local name="$1" haystack="$2" needle="$3"
  if printf '%s' "$haystack" | grep -qF -- "$needle"; then
    bad "$name" "output contained '$needle' and must not have"
  else
    pass "$name"
  fi
}

# The canary. Random so it cannot accidentally pre-exist anywhere, and long
# enough that finding it in an archive means exactly one thing.
CANARY="CANARY-$(openssl rand -hex 12)-HAMLANEH-RESTORE-DRILL"
# A second one, in a file rather than a row, because "the database was
# encrypted" and "the uploads were encrypted" are two claims.
FILE_CANARY="FILECANARY-$(openssl rand -hex 12)-HAMLANEH-RESTORE-DRILL"
# What a login reads. Not a real hash of anything; its job is to come back
# byte for byte.
# shellcheck disable=SC2016  # the $ signs are argon2 field separators, and the
# literal string surviving byte for byte is the whole point of the assertion.
PWHASH='$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHR2YWx1ZQ$Zm9yVGhlUmVzdG9yZURyaWxsT25seQ'

# ---------------------------------------------------------------------------
# server leg
# ---------------------------------------------------------------------------

server_leg() {
  local key_dir="$WORK/keys" backup_dir="$WORK/archives"
  local image
  # The exact image docker-compose.yml pins, read from it rather than copied,
  # so this test cannot drift from what the stack actually runs.
  image="$(sed -n 's/^ *image: \(postgres:[^ ]*\)$/\1/p' "$SCRIPT_DIR/docker-compose.yml" | head -n 1)"
  [ -n "$image" ] || { printf 'ERROR: could not read the postgres image from docker-compose.yml\n' >&2; exit 2; }

  mkdir -p "$key_dir" "$backup_dir"

  export HAMLANEH_BACKUP_KEY_FILE="$key_dir/backup.key"
  export HAMLANEH_BACKUP_DIR="$backup_dir"
  export HAMLANEH_COMPOSE_FILE="$COMPOSE_FILE"
  export HAMLANEH_BACKUP_NO_SCHEDULE=1
  unset HAMLANEH_HOME_DATA_DIR HAMLANEH_HOME_DB 2>/dev/null || true

  # A stand-in stack: the real db service, and a "server" that exists only to
  # own the uploads volume the way the real one does. The backup script never
  # execs into it — it resolves the volume by compose label and touches it from
  # a throwaway container — so a shell in this stand-in cannot hide a
  # dependency the real distroless server would not satisfy.
  cat > "$COMPOSE_FILE" <<EOF
name: $PROJECT
services:
  db:
    image: $image
    environment:
      POSTGRES_DB: hamlaneh
      POSTGRES_USER: hamlaneh
      POSTGRES_PASSWORD: $(openssl rand -hex 24)
    volumes:
      - db_data:/var/lib/postgresql/data
  server:
    image: $image
    entrypoint: ["sleep", "infinity"]
    volumes:
      - file_data:/var/lib/hamlaneh
volumes:
  db_data:
  file_data:
EOF

  section "server leg: bringing up a real PostgreSQL and a real uploads volume"
  docker compose -f "$COMPOSE_FILE" up -d >/dev/null
  local deadline=$((SECONDS + 120))
  # A real query, not pg_isready. The postgres image's first boot runs a
  # bootstrap server on the unix socket while initdb works, and pg_isready
  # answers "ready" against it — then it shuts down and the real server
  # starts, so a psql issued in that window dies with "the database system
  # is starting up" (measured: CI run 33485399012, 0.2s after the old wait
  # said ready). The wait now proves the exact thing the next line needs:
  # a session that can run a statement.
  until docker compose -f "$COMPOSE_FILE" exec -T db psql -U hamlaneh -d hamlaneh -tAc 'select 1' >/dev/null 2>&1; do
    [ "$SECONDS" -lt "$deadline" ] || { printf 'ERROR: postgres never became ready\n' >&2; exit 2; }
    sleep 2
  done
  printf '  postgres ready\n'

  psql_i() { docker compose -f "$COMPOSE_FILE" exec -T db psql -U hamlaneh -d hamlaneh "$@"; }
  in_files() { docker compose -f "$COMPOSE_FILE" exec -T server sh -c "$1"; }

  # Seed: a user whose password hash a login would read, and a message
  # carrying the canary.
  psql_i -v ON_ERROR_STOP=1 -q <<SQL
CREATE TABLE users (id int PRIMARY KEY, username text NOT NULL, password_hash text NOT NULL);
INSERT INTO users VALUES (1, 'amir', '$PWHASH');
CREATE TABLE messages (id int PRIMARY KEY, body text NOT NULL);
INSERT INTO messages VALUES (1, '$CANARY');
SQL

  # Uploaded bytes, in the uuid-fanout shape blobstore writes, owned by the uid
  # the read-only-rootfs server runs as.
  in_files "
    set -e
    mkdir -p /var/lib/hamlaneh/ab/cd /var/lib/hamlaneh/ef/01
    printf '%s' '$FILE_CANARY' > /var/lib/hamlaneh/ab/cd/0f8e1c22-1111-4444-8888-aaaaaaaaaaaa
    head -c 4096 /dev/urandom > /var/lib/hamlaneh/ef/01/0f8e1c22-2222-4444-8888-bbbbbbbbbbbb
    chmod -R 700 /var/lib/hamlaneh
    chown -R 65532:65532 /var/lib/hamlaneh"

  local before_files
  before_files="$(in_files 'cd /var/lib/hamlaneh && find . -type f -print | LC_ALL=C sort | xargs sha256sum')"

  section "server leg: taking the backup"
  local run_out
  run_out="$(bash "$BACKUP" run 2>&1)"
  assert_contains "run announces the new key loudly" "$run_out" "A NEW BACKUP KEY WAS CREATED"
  assert_contains "run warns that custody is not confirmed" "$run_out" "has not been copied off this machine"
  assert_contains "run verifies the archive it just wrote" "$run_out" "backup complete and verified"

  local key
  key="$(cat "$HAMLANEH_BACKUP_KEY_FILE")"
  assert_absent "the key never appears in run's own output" "$run_out" "$key"
  assert_eq "the key file is mode 600" "$(stat -c '%a' "$HAMLANEH_BACKUP_KEY_FILE")" "600"

  local archive
  archive="$(find "$backup_dir" -name 'hamlaneh-server-*.tar.gz.enc' | head -n 1)"
  if [ -z "$archive" ]; then
    bad "an archive was produced"
    return 0
  fi
  pass "an archive was produced: $(basename "$archive")"

  section "server leg: the negative — no plaintext canary in the archive"
  local scan_out scan_rc=0
  scan_out="$(bash "$BACKUP" scan "$archive" --canary "$CANARY" --canary "$FILE_CANARY" 2>&1)" || scan_rc=$?
  printf '%s\n' "$scan_out" | sed 's/^/    | /'
  assert_eq "the canary scan passes" "$scan_rc" "0"
  assert_contains "the message canary is absent from the ciphertext" "$scan_out" "canary absent from the ciphertext: $CANARY"
  assert_contains "the file canary is absent from the ciphertext" "$scan_out" "canary absent from the ciphertext: $FILE_CANARY"
  assert_contains "the positive control recovers the message canary" "$scan_out" "canary recovered from the decrypted archive: $CANARY"

  # Mutation: the scan must be able to fail. A gzip of the same plaintext hides
  # a literal string just as well as a cipher does, so a bare canary grep would
  # call an unencrypted archive clean. These two prove it does not.
  local fake="$WORK/fake-plain.tar.gz.enc"
  mkdir -p "$WORK/fakedir"
  printf '%s' "$CANARY" > "$WORK/fakedir/database.sql"
  tar -czf "$fake" -C "$WORK/fakedir" .

  local mut_out mut_rc=0
  mut_out="$(bash "$BACKUP" scan "$fake" --canary "$CANARY" 2>&1)" || mut_rc=$?
  assert_eq "mutation: a gzipped plaintext archive is rejected" "$mut_rc" "1"
  assert_contains "mutation: the scan names the missing encryption" "$mut_out" "not encrypted at all"
  # gzip hides a literal string as thoroughly as a cipher does, so this is the
  # check that keeps the canary grep from being decoration.
  assert_contains "mutation: the canary grep alone would have passed it" "$mut_out" \
    "canary absent from the ciphertext"
  assert_contains "mutation: the scan reports every failing check, not just the first" "$mut_out" \
    "the absences above prove nothing"

  tar -cf "$WORK/fake-uncompressed.tar.gz.enc" -C "$WORK/fakedir" .
  check "mutation: an uncompressed plaintext archive is caught" 1 "canary present in the ciphertext" \
    bash "$BACKUP" scan "$WORK/fake-uncompressed.tar.gz.enc" --canary "$CANARY"

  section "server leg: the negative — restore with the wrong key"
  local wrong_key="$WORK/wrong.key"
  ( umask 077; openssl rand -base64 32 > "$wrong_key" )
  check "restore with the wrong key fails with a clear error" 1 "This is what a wrong key looks like" \
    env HAMLANEH_BACKUP_KEY_FILE="$wrong_key" bash "$BACKUP" restore "$archive" --force

  # Checked, not assumed: the instance is exactly as it was.
  assert_eq "after the wrong-key restore the canary row is still there" \
    "$(psql_i -tAc "select body from messages where id = 1")" "$CANARY"
  assert_eq "after the wrong-key restore the uploaded files are untouched" \
    "$(in_files 'cd /var/lib/hamlaneh && find . -type f -print | LC_ALL=C sort | xargs sha256sum')" \
    "$before_files"
  assert_eq "after the wrong-key restore the application server is still running" \
    "$(docker compose -f "$COMPOSE_FILE" ps --status running --services | grep -c '^server$')" "1"

  section "server leg: the negative — a tampered archive"
  local tampered="$WORK/tampered.tar.gz.enc"
  cp "$archive" "$tampered"
  # Flip one byte well past the salted header.
  printf '\xff' | dd of="$tampered" bs=1 seek=512 count=1 conv=notrunc status=none
  check "a tampered archive is refused" 1 "" bash "$BACKUP" restore "$tampered" --force
  assert_eq "after the tampered restore the canary row is still there" \
    "$(psql_i -tAc "select body from messages where id = 1")" "$CANARY"

  section "server leg: the negative — a populated instance is not overwritten"
  check "restore into a populated instance is refused without --force" 3 "already holds data" \
    bash "$BACKUP" restore "$archive"

  check "a server archive is refused by a home instance" 3 "produces an instance that is neither" \
    env HAMLANEH_HOME_DATA_DIR="$WORK/not-a-home-instance" bash "$BACKUP" restore "$archive" --mode home --force

  section "server leg: the restore drill"
  # Lose everything, the way a dead disk does.
  psql_i -v ON_ERROR_STOP=1 -q -c 'DROP TABLE messages; DROP TABLE users;'
  in_files 'find /var/lib/hamlaneh -mindepth 1 -delete'
  assert_eq "the instance is empty before the drill" \
    "$(in_files 'find /var/lib/hamlaneh -type f | wc -l' | tr -d ' \r')" "0"

  local restore_out restore_rc=0
  restore_out="$(bash "$BACKUP" restore "$archive" 2>&1)" || restore_rc=$?
  assert_eq "the restore succeeds on an emptied instance" "$restore_rc" "0"
  assert_contains "the restore says what it did" "$restore_out" "restore complete"

  assert_eq "the pre-backup canary message is present" \
    "$(psql_i -tAc "select body from messages where id = 1")" "$CANARY"
  assert_eq "the existing user survives with the hash a login reads" \
    "$(psql_i -tAc "select username || ' ' || password_hash from users where id = 1")" \
    "amir $PWHASH"
  assert_eq "every uploaded file checksum matches" \
    "$(in_files 'cd /var/lib/hamlaneh && find . -type f -print | LC_ALL=C sort | xargs sha256sum')" \
    "$before_files"
  assert_eq "the uploaded files keep the uid the server reads them as" \
    "$(in_files 'stat -c %u /var/lib/hamlaneh/ab/cd/0f8e1c22-1111-4444-8888-aaaaaaaaaaaa' | tr -d ' \r')" \
    "65532"
  assert_eq "no staging directory is left inside the volume" \
    "$(in_files 'ls -a /var/lib/hamlaneh | grep -c hamlaneh-restore' | tr -d ' \r')" "0"

  section "server leg: retention"
  local i
  for i in 1 2 3 4 5; do
    : > "$backup_dir/hamlaneh-server-2020010${i}T000000Z.tar.gz.enc"
    : > "$backup_dir/hamlaneh-server-2020010${i}T000000Z.tar.gz.meta"
  done
  bash "$BACKUP" run --keep 2 >/dev/null 2>&1
  assert_eq "retention keeps exactly --keep archives" \
    "$(find "$backup_dir" -name 'hamlaneh-server-*.tar.gz.enc' | wc -l | tr -d ' ')" "2"
  assert_eq "retention drops the stale .meta sidecars with them" \
    "$(find "$backup_dir" -name 'hamlaneh-server-2020*.meta' | wc -l | tr -d ' ')" "0"

  section "server leg: key custody"
  assert_contains "key status starts unconfirmed" "$(bash "$BACKUP" key)" "NOT CONFIRMED"
  assert_eq "key --show prints the key and nothing else" "$(bash "$BACKUP" key --show)" "$key"
  bash "$BACKUP" key --confirm >/dev/null
  assert_contains "key status becomes confirmed" "$(bash "$BACKUP" key)" "confirmed by the operator"
  assert_absent "a later run no longer nags" "$(bash "$BACKUP" run 2>&1)" "has not been copied off this machine"

  check "verify reports a good archive" 0 "verifies" bash "$BACKUP" verify "$archive"
  check "verify with the wrong key does not" 1 "wrong key looks like" \
    env HAMLANEH_BACKUP_KEY_FILE="$wrong_key" bash "$BACKUP" verify "$archive"
}

# ---------------------------------------------------------------------------
# home leg
# ---------------------------------------------------------------------------

home_leg() {
  local root="$WORK/home"
  local data="$root/data" key_dir="$root/keys" backup_dir="$root/archives"
  mkdir -p "$data/ab/cd" "$key_dir" "$backup_dir"

  export HAMLANEH_BACKUP_KEY_FILE="$key_dir/backup.key"
  export HAMLANEH_BACKUP_DIR="$backup_dir"
  export HAMLANEH_HOME_DATA_DIR="$data"
  export HAMLANEH_BACKUP_NO_SCHEDULE=1

  section "home leg: a real SQLite database and a real blob tree"
  sqlite3 "$data/hamlaneh.db" \
    "CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT, password_hash TEXT);
     INSERT INTO users VALUES (1, 'amir', '$PWHASH');
     CREATE TABLE messages (id INTEGER PRIMARY KEY, body TEXT);
     INSERT INTO messages VALUES (1, '$CANARY');"
  printf '%s' "$FILE_CANARY" > "$data/ab/cd/0f8e1c22-3333-4444-8888-cccccccccccc"

  local before_files
  before_files="$( cd "$data" && find . -type f ! -name 'hamlaneh.db*' -print | LC_ALL=C sort | xargs sha256sum )"

  bash "$BACKUP" run --mode home >/dev/null 2>&1
  local archive
  archive="$(find "$backup_dir" -name 'hamlaneh-home-*.tar.gz.enc' | head -n 1)"
  if [ -z "$archive" ]; then
    bad "a home-mode archive was produced"
    return 0
  fi
  pass "a home-mode archive was produced: $(basename "$archive")"

  local scan_out scan_rc=0
  scan_out="$(bash "$BACKUP" scan "$archive" --canary "$CANARY" --canary "$FILE_CANARY" 2>&1)" || scan_rc=$?
  assert_eq "the home-mode canary scan passes" "$scan_rc" "0"
  assert_contains "the home-mode positive control recovers the canary" "$scan_out" \
    "canary recovered from the decrypted archive: $CANARY"

  section "home leg: the negatives"
  local wrong_key="$root/wrong.key"
  ( umask 077; openssl rand -base64 32 > "$wrong_key" )
  check "home restore with the wrong key fails with a clear error" 1 "This is what a wrong key looks like" \
    env HAMLANEH_BACKUP_KEY_FILE="$wrong_key" bash "$BACKUP" restore "$archive" --mode home --force
  assert_eq "after the wrong-key restore the home canary row is still there" \
    "$(sqlite3 "$data/hamlaneh.db" 'select body from messages where id = 1')" "$CANARY"

  check "home restore into a populated instance is refused without --force" 3 "already holds data" \
    bash "$BACKUP" restore "$archive" --mode home

  section "home leg: the restore drill"
  rm -rf "${data:?}"/*
  bash "$BACKUP" restore "$archive" --mode home >/dev/null 2>&1
  assert_eq "the pre-backup canary message is present" \
    "$(sqlite3 "$data/hamlaneh.db" 'select body from messages where id = 1')" "$CANARY"
  assert_eq "the existing user survives with the hash a login reads" \
    "$(sqlite3 "$data/hamlaneh.db" "select username || ' ' || password_hash from users where id = 1")" \
    "amir $PWHASH"
  assert_eq "every uploaded file checksum matches" \
    "$( cd "$data" && find . -type f ! -name 'hamlaneh.db*' -print | LC_ALL=C sort | xargs sha256sum )" \
    "$before_files"
  assert_eq "the restored database passes SQLite's own integrity check" \
    "$(sqlite3 "$data/hamlaneh.db" 'PRAGMA integrity_check;')" "ok"
}

# ---------------------------------------------------------------------------

case "$LEG" in
  server) server_leg ;;
  home) home_leg ;;
  both) server_leg; home_leg ;;
esac

printf '\n%d checks, %d failures\n' "$checks" "$failures"
[ "$failures" -eq 0 ]
