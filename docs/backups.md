# Backups and restore

**This document is about the *operator's* backup of an instance: the database and the uploaded
files, encrypted under a key you hold.**

It is not [ADR 010](adr/010-encrypted-backups.md). ADR 010 is a different thing with a confusingly
similar name: the *user's* encrypted key backup, sealed in their browser under a recovery key this
server can never read, carrying their recorded trust decisions. That one is already built, it is
each user's own, and no administrator can open it or reset it. This one is yours, it holds the
whole instance, and losing its key loses the instance.

| | ADR 010 — user key backup | This document — operator backup |
|---|---|---|
| Holds | one user's trust decisions | the database and every uploaded file |
| Sealed by | the user's recovery key | the operator's backup key |
| Openable by | that user only | whoever holds `/etc/hamlaneh/backup.key` |
| Lost key means | verify your contacts again | the archives are noise |

Everything below is `deploy/hamlaneh-backup.sh`.

---

## The short version

```sh
sudo deploy/hamlaneh-backup.sh run          # take one now; installs the daily timer
sudo deploy/hamlaneh-backup.sh key --show   # copy this off the machine. Now.
sudo deploy/hamlaneh-backup.sh key --confirm
sudo deploy/hamlaneh-backup.sh list
sudo deploy/hamlaneh-backup.sh restore /var/backups/hamlaneh/hamlaneh-server-….tar.gz.enc
```

Defaults, all overridable by environment variable (`hamlaneh-backup.sh help` lists them):

| | |
|---|---|
| Archives | `/var/backups/hamlaneh` |
| Key | `/etc/hamlaneh/backup.key`, mode 600 |
| Schedule | daily at 03:00 with up to 30 minutes of jitter, systemd timer (cron fallback) |
| Retention | the newest 14 archives |
| Cipher | AES-256-CBC, PBKDF2-SHA-256, `openssl` |

## Custody: the part that actually goes wrong

A key that has only ever existed on the machine it protects is not a key, it is a delay. The disk
dies, the key dies with it, and the archives that survived are noise.

So the key is loud when it is created — a banner, on the first run — and it warns on **every**
backup until you run `key --confirm`. Nothing verifies that you really stored it; the marker only
silences the warning. That is the honest limit of what a script can check.

Store the key the way you store the recovery codes of something that matters: a password manager,
an offline encrypted volume, a sealed envelope. **Not** next to the archives. An archive and its
key in the same bucket is one bucket.

`deploy/.env` is a *second* thing to keep, and it is not in the backup (see below). Keep both, and
keep them apart from the archives.

## What is in an archive

An archive is `hamlaneh-<mode>-<UTC timestamp>.tar.gz.enc` plus a plaintext `.meta` sidecar. The
sidecar exists so you can tell which key opens which archive without opening anything; it carries
the format, the mode, the timestamp and the key fingerprint, and nothing an attacker holding the
archive would not already have.

Sealed inside the ciphertext:

| Member | What it is |
|---|---|
| `manifest` | format, mode, creation time, key fingerprint |
| `checksums` | SHA-256 of every other member |
| `database.sql` / `database.sqlite` | the whole database (`pg_dump --clean --if-exists`, or SQLite's online `.backup`) |
| `files.tar` | every uploaded byte, with the ownership the server reads them as |
| `files.sha256` | per-file SHA-256, so "the checksums match" is a thing you can check rather than hope |

`checksums` is *inside* the ciphertext on purpose. Nobody without the key can rewrite it to match
tampered content, and no restore acts on an archive whose checksums have not verified in full —
which is what makes a wrong key a refusal instead of a half-restore.

### What is **not** in an archive, and what that costs you

This is the section to read before you need it.

- **`deploy/.env` — the instance secrets.** Deliberately excluded, following the rule
  [`docs/hardening.md`](hardening.md) already states: the key must not travel with the ciphertext,
  and an archive that carried every secret would *be* the instance. Restoring onto a machine with a
  freshly generated `.env` gives you a working instance with three specific losses:
  `HAMLANEH_AUDIT_KEY` changes, so every restored audit row stops verifying (the rows are there and
  readable; the instance can no longer vouch for them); `HAMLANEH_FILE_URL_KEY` changes, so already
  minted file URLs break until clients re-fetch (cheap); `POSTGRES_PASSWORD` must match whatever
  the restored stack uses. **Keep an offline copy of `deploy/.env`.**
- **Caddy's certificates** (`caddy_data`). A real domain re-issues from ACME automatically. A
  `localhost` or bare-IP install generates a *new* internal CA, so anyone who trusted the old one
  must trust the new one.
- **LiveKit.** There is nothing to back up: live room state is the truth and it is ephemeral
  ([ADR 005](adr/005-calls-and-meetings.md)). Calls in progress end. Nothing is lost that outlives
  a call.
- **Anything written since the last archive.** Daily schedule, so up to 24 hours. Change
  `HAMLANEH_BACKUP_AT` and the timer if that is too much.
- **The application itself.** A restore expects a stack of the same version or newer — migrations
  run forward on start. Restoring a dump into an *older* binary is not supported.

### A restore rewinds the server, and some rows are dangerous when rewound

A restore is a rollback of everything the database holds, which includes decisions that were made
*after* the backup:

- a session revoked after the backup is **valid again** after the restore;
- a password changed after the backup is **back to the old one**;
- a user disabled, or a device removed, after the backup **reappears**.

After any restore that recovers from a compromise rather than from a dead disk, re-do the
revocations. Nothing in the archive knows they happened.

## Does an MLS-encrypted channel survive a restore?

Yes — and it is worth being exact about what "survive" means, because the server is MLS-blind
([ADR 006](adr/006-mls-library-and-boundaries.md)) and cannot possibly back up what it cannot read.

What comes back: the channel, its membership, every encrypted message's **bytes**, every encrypted
attachment's bytes, and the public MLS material the server holds. All of it byte for byte —
`files.sha256` and `checksums` say so.

What decides whether any of it is *readable* is client-side key material that this backup never
held and could never hold, because the server never had it:

- A member whose device still has its local MLS state reads the restored history exactly as before.
  For them, nothing happened.
- A member joining on a new device gets no history — same as before the restore. A database restore
  does not undo forward secrecy, and it does not hand anyone a key they did not have.
- If **every** member of a channel lost their device state, the restored ciphertext is unreadable,
  permanently, by everyone including you. The operator backup did its job perfectly and the messages
  are still gone. That is the design working, not a bug in it, and it is the price of the server
  being unable to read your messages.

There is one more consequence, and it is the sharp one. MLS ratchets forward; a restore moves the
server's copy of a group backwards. Clients that had advanced past the restored epoch are ahead of
the server, and the group has to converge again through a fresh commit from a member. More
importantly, **a device removed from a group after the backup is a member again in the restored
rows** — the group-membership half of the rollback hazard [ADR 008](adr/008-key-verification.md)
flagged for the user blob. If you restore after a compromise, remove that device again, in the app,
after the restore.

## Restoring

Restore verifies the whole archive before it touches anything, so a wrong key, a truncated download
or a flipped bit all fail with the instance exactly as it was. It then refuses a populated instance
unless you pass `--force`.

```sh
# Server mode. The stack must be up: the database is what is being written to.
sudo deploy/hamlaneh-backup.sh restore /var/backups/hamlaneh/hamlaneh-server-….tar.gz.enc
```

In order: decrypt, check every sealed checksum, refuse if this instance already holds data, stop
the application server (the database and Caddy stay up — Caddy answers 502 for the minute this
takes), unpack the verified files *beside* the live ones inside the same volume, restore the
database in **one transaction** (`--single-transaction -v ON_ERROR_STOP=1`, so it either replaces
the schema or changes nothing), swap the files in, start the server.

Home mode is the same shape with one thing the script cannot do for you: **stop the Hamlaneh
process first.** There is no supervisor to ask. Restoring under a live writer is how a database
ends up with two histories.

```sh
HAMLANEH_HOME_DATA_DIR=~/.local/share/hamlaneh \
  deploy/hamlaneh-backup.sh restore ~/backups/hamlaneh-home-….tar.gz.enc
```

Home mode needs `sqlite3` on `PATH` and says so rather than falling back: a live SQLite file copied
byte for byte is not a backup, it is a torn page waiting to be discovered at restore time. The
backup uses SQLite's online `.backup`, which is safe against a live writer; the restore is not.

Exit codes: `0` fine, `1` something failed, `3` refused on purpose (a populated instance, a
home archive aimed at a server instance).

## The drill

Run this on a **fresh machine**, not the live one. An untested restore is a hope.

```sh
# on the live instance
sudo deploy/hamlaneh-backup.sh run
sudo deploy/hamlaneh-backup.sh scan \
  /var/backups/hamlaneh/hamlaneh-server-….tar.gz.enc --canary 'a phrase from a recent message'
# copy the archive AND deploy/.env AND the backup key to the fresh machine, separately

# on the fresh machine
sudo ./install.sh --domain <domain>
sudo deploy/hamlaneh-backup.sh restore <archive> --force
# then, in a browser: log in as an existing user, open the channel, find the canary message,
# download a file that was uploaded before the backup
```

`scan` is the scripted negative from the Phase 4 gate, and it is four checks rather than one,
because the obvious one alone is worthless: a plain gzip hides a literal string exactly as well as
a cipher does, so grepping an archive for a canary and finding nothing would call an unencrypted
archive clean. `scan` therefore also asserts the archive is not a gzip or a tar, that it carries
openssl's salted header, and — the check that matters most — that the canary **comes back** once
the key is applied. Without that last one, "the canary is not in this file" is also true of every
file that is not your backup.

`deploy/hamlaneh-backup.test.sh` runs all of this automatically against a real PostgreSQL container,
a real docker volume and a real SQLite database, including the two mutations that prove `scan` can
fail. It is a CI gate.

## Retention and the failure where backups take the instance down

The newest 14 archives are kept. Pruning runs *before* a backup as well as after, so yesterday's
pile cannot stop today's, and a free-space check refuses to start a dump that would not fit —
loudly, before anything is written, rather than filling the disk and taking the instance with it.

`--keep <n>` for one run, `HAMLANEH_BACKUP_KEEP` permanently. `hamlaneh-backup.sh disable` turns the
schedule off and says plainly that nothing will prune the archives again.

## Known gaps

- **The schedule is on by default only after the first run.** `run` installs the timer when it is
  absent and it can, so a manual first backup is also the moment the schedule starts existing. For
  it to be true from the very first `docker compose up`, `deploy/install.sh` has to call
  `hamlaneh-backup.sh enable` — one idempotent line, in a file this slice does not own.
- **Off-machine copying is yours.** The script writes to a local directory. Getting archives to
  another machine — `rsync`, `rclone`, a mounted volume — is deliberately not built in, and a
  backup that never leaves the host is only a defense against `DROP TABLE`, not against the host.
- **The drill's last step is manual.** "Existing users log in" is proved here one layer down: the
  users row and its argon2id hash come back byte for byte, which is what a login reads. The
  browser step above is what closes it.
