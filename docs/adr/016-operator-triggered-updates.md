# ADR 016 — The dashboard asks for an update; it never holds the power to apply one

**Status:** accepted — 2026-09-08
**Extends:** [ADR 015](015-admin-origin.md) (the admin surface and its own port), and the
auto-update machinery ROADMAP Phase 4 shipped as `deploy/hamlaneh-update.sh`.

## Context

Phase 4 shipped automatic updates as a systemd timer that runs the updater four times a day
on the security channel. It works, and it is invisible. An operator cannot see that a
release exists, cannot see whether the last run succeeded, and cannot apply an update at a
moment of their choosing — a maintenance window, or the hour an advisory lands. On the one
instance this project actually runs, the timer had been failing every day for a week and
nothing in the product said so.

So the ask is a control in the admin dashboard: *an update is available — apply it.*

What makes that harder than it sounds is a boundary the updater already went out of its way
to draw. From `install_timer` in `deploy/hamlaneh-update.sh`:

> A systemd timer rather than a compose sidecar: a sidecar that swaps images needs the docker
> socket mounted into a container, which hands anything inside that container root on the
> host — an insecure default in a project whose first principle is that defaults carry the
> security load.

The server container is `read_only`, drops every capability, runs as a non-root user, and
mounts exactly one volume. The authority to replace the running image lives on the host and
must stay there. The dashboard, meanwhile, is served *by* that container.

This ADR is about how a click crosses that line without carrying any authority across with
it.

## Decision

### 1. The request is a signal, not a command

The server writes a request file. The host unit reads it, and runs the updater with **its
own fixed argument vector** — the same one the timer runs. The file names no version, no
repository, no channel, and no flags. The only field the host unit reads out of it is a
`kind`, which must be one of exactly two literals:

| `kind` | the host unit runs |
|---|---|
| `check` | `hamlaneh-update.sh --check` |
| `apply` | `hamlaneh-update.sh` |

Anything else — a third literal, a missing field, unparseable JSON — is refused and recorded,
and nothing is executed. No value from the file ever reaches a command line.

This is the whole security argument, so it is worth stating what the alternative would have
cost. If the request could name a version, it could name an *older* one, and the natural next
field is `force`. `--force` is the flag that switches off the anti-rollback check — the
control Phase 4's test gate exists to prove ("an older validly-signed release is rejected
unless explicitly forced"). An endpoint that can pass it turns a stolen admin session into a
downgrade to any signed release with a known vulnerability. Signature verification would pass.
The instance would be genuinely, verifiably running an old and exploitable Hamlaneh.

With the parameterless signal, the widest thing an attacker holding an admin session can do
is make the instance apply a signed release from the pinned repository that the timer would
have applied within six hours anyway. That is not an escalation, and it is the property the
design is built to keep.

### 2. The handoff is a file both sides can see

A named volume, `update_state`, mounted read-write into the server container at
`/var/lib/hamlaneh-update` and read on the host through the volume's mountpoint. A sibling of
the uploads volume rather than a directory inside it: nesting one mount inside another works
and then surprises somebody restoring a backup, and `HAMLANEH_DATA_DIR` is walked by the
backup script. Three files, all JSON, all written atomically by rename:

| file | written by | says |
|---|---|---|
| `watcher.json` | the host unit, when installed and on every run | that something is listening, and when it last was |
| `request.json` | the server | a `kind` and a request id |
| `status.json` | the host unit | what the last run did, and what it found |

No new listener, no socket, no daemon, and nothing that outlives a run. A systemd `.path`
unit triggers on the request file appearing, so the latency between the click and the work
is a fraction of a second rather than the timer's six hours.

### 3. The control does not exist unless something is listening

`hamlaneh-update.sh --install-timer` also installs the watcher and stamps `watcher.json`.
Where systemd does not exist — home mode on a host without it, the case
`hamlaneh-backup.sh` already handles by falling back to cron — nothing stamps that file, and
the server publishes `self_update_available: false`. The dashboard then says updates are
managed outside this instance and draws no button.

This follows `password_reset_available` exactly (Phase 1.1b): a capability the instance
cannot actually perform is never offered. A button that silently does nothing is worse than
no button, because the operator believes the instance is patched.

### 4. The updater cleans up after itself, and never touches a volume

Every successful run ends by removing what the update orphaned: the image the retag left
untagged, stopped containers, and the build cache. Written as an explicit list of commands
rather than `docker system prune`, and with **no `--volumes` on any path** — those volumes are
the database, the uploaded files and the certificate store, and one careless flag there is the
whole instance.

Two runs deliberately do **not** prune:

- A run that **rolled back**. The previous image is the thing rollback restores; it is
  untagged at that moment and would be exactly what a dangling-image prune removes.
- A run that **failed**. Whatever it left behind is evidence.

A no-op run — the common case, four times a day — does prune, which is what keeps a
long-lived host clean without a second timer.

## Consequences

- **The dashboard can show the truth about updates**, including that the last run failed,
  which is what this instance needed and did not have.
- **An admin session cannot cause a downgrade**, cannot reach an arbitrary repository, and
  cannot pass a flag. The blast radius of the new endpoint is "apply the release the host
  was already going to apply."
- **The container gains no privilege.** No docker socket, no host mount beyond a state
  directory, `read_only` intact.
- **A second volume** joins the compose stack. It holds no user data and is safe to delete;
  losing it costs the record of the last run.
- **`--check` becomes machine-readable.** It printed prose; it now also writes `status.json`.
  The prose is unchanged, because operators read it.
- **The version contract gets sharper teeth, and one real install fails it today.** The
  updater refuses to run when the server cannot state a `vX.Y.Z` version, which is correct
  and is why the timer on the project's own server has failed every day since it was
  installed: that instance was built from a git checkout, so its binary reports `dev`. The
  dashboard now surfaces that as its own state — *this instance was not installed from a
  release, so it cannot be updated in place* — rather than as silence. Fixing it is an
  install-path question, not an updater one, and is tracked separately.
