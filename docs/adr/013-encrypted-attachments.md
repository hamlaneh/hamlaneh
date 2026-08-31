# ADR 013 — Encrypted attachments: the key rides the message, the blob stays opaque, and a file is readable exactly when its message is

**Status:** proposed · 2026-08-31 · **Scope:** Phase 3, encrypted-attachments slice

> Written against the code rather than a model of it. The wrapper's own `decrypt` doc
> (`webapp/src-mls/src/lib.rs`) states the fact this whole design turns on: a message "from
> before this device joined, or from an epoch whose secrets have been dropped, cannot be opened
> by anyone holding this state" — old epochs are not merely inconvenient to reach, they are
> deleted, and ADR 010 already canonized that as "absent from the universe of keys". Two more
> verified facts shape the decision: the files origin serves anything without proven dimensions
> as an opaque, sandboxed download already (`files_origin.go` — octet-stream, `attachment`
> disposition, nosniff, `sandbox` CSP), so ciphertext needs no new serving mode; but the same
> file's `Cross-Origin-Resource-Policy: same-origin` on opaque blobs plus the absence of any
> CORS header means the app's own JavaScript cannot `fetch()` those bytes to decrypt them —
> reading ciphertext cross-origin is a real serving change, not a given. And migration 0007
> requires `char_length(filename) BETWEEN 1 AND 255`, which is why the metadata placeholder
> below is a constant rather than an empty string.

## Context

Strict is the default and only selectable mode (ADR 011), so every conversation on a fresh
install is born encrypted — and an encrypted conversation refuses attachments at three
choke points that all route through one function (`writeE2EEAttachmentsUnsupported` in
`server/internal/httpserver/e2ee.go`: the send path, the edit path, and the upload route).
The refusal was the honest placeholder for this slice; now that Strict is universal it means
**file sharing is unreachable on every new install** while the README leads with it. That is a
launch blocker, and this ADR closes it.

The nearest precedent is ADR 009: call media is keyed from the MLS exporter, every member
deriving the same secret from the epoch's key schedule, the key rotating with the epoch. The
slice brief asked three confirm-or-refute questions — whether that approach survives a file's
lifetime, what opens a file sent three epochs ago, and whether the server must be able to tell
an encrypted attachment from a plaintext one. Each decision below answers one. The boundaries
this ADR inherits whole: the server stays MLS-blind (ADR 006), the per-channel `e2ee` flag is
immutable and the write path enforces the mode in both directions (migration 0017, `e2eeBody`),
files are channel-scoped with signed expiring URLs as the only credential (ADR 003), and no
hand-rolled cryptography, ever (§6.9).

## Decision 1 — the exporter does not survive a file's lifetime; a per-file key carried inside the message does

Q1 refuted, and the refutation is the system's own guarantee rather than a preference. A call
frame is sealed and opened inside one epoch; ADR 009's design leans on that — old media keys
age out of a keyring within seconds and are never persisted. A file inverts every one of those
properties: it is stored once and opened months later, across hundreds of epochs, by devices
that have merged every commit in between. Deriving the upload-epoch exporter at read time is
impossible by construction: forward secrecy deletes each epoch's secrets when the next one
merges — that deletion is the property the phase exists to create, and the wrapper's `decrypt`
documents its consequence as an ordinary condition, not an error. The exporter keys the
*present*; a file needs a key for the *past*.

The two ways to keep an exporter scheme alive were examined and refused:

- **A re-wrap treadmill** — wrap each file key under the current epoch's exporter, and have
  some member re-wrap every stored key at every epoch change — is a member-run key-management
  layer of exactly the class ADR 009 decision 1 refused for media: it elects whoever happens to
  be online as a keying authority, needs a catch-up protocol for epochs nobody was awake for,
  and hands the server a growing table of wrapped keys whose maintenance is a protocol §6.9
  bars us from designing.
- **Retaining old exporter secrets client-side** so the past stays derivable is not a smaller
  concession but a bigger one: it repeals forward secrecy for the whole group's key schedule to
  serve one feature, weakening messages to strengthen files.

What survives is the pattern every deployed E2EE messenger uses for attachments (Signal's
attachment pointers, Matrix's encrypted files): **a fresh random key per attachment, carried
inside the ciphertext of the message that shares the file.** The MLS application message is a
channel ADR 009 itself certified as end-to-end secure; what it refused was a *shared, rotating*
key distributed over it, because rotation, re-minting and joiner catch-up are the hand-rolled
key management. A per-file key has no lifecycle to manage: minted once by the uploader's
device, sealed once into the message, never rotated, never re-minted, dead when its one object
is deleted. It is content, not key management — exactly as much protocol as the message text
itself, and its confidentiality is the message's by construction.

Mechanics, fixed here so no implementation chooses (§6.9's bar is cleared the same way ADR 010
cleared it — WebCrypto natives, standard constructions, zero new dependencies, and the wasm
wrapper is untouched, so ADR 006's glue-only rule survives again):

- **Key:** 16 random bytes per attachment (`crypto.getRandomValues`), AES-128-GCM — matching
  the MLS ciphersuite's AES-128-GCM strength, the same reasoning ADR 009 used to keep
  LiveKit's `keySize` at 128. One key covers both blob variants.
- **Blob format:** `nonce (12 random bytes) ‖ AES-GCM ciphertext‖tag`, fresh nonce per variant.
  One key never seals two plaintexts under one nonce, and a single-use key makes nonce misuse
  structurally unreachable rather than avoided.
- **Domain separation:** the AAD is `hamlaneh attachment original v1` for the original and
  `hamlaneh attachment thumb v1` for the thumbnail. A server that swaps the two blobs of an
  attachment — the one substitution the shared key would otherwise permit — fails the tag.
  Any other substitution already fails it: a different attachment has a different key.
- **Client pre-encryption pipeline mirrors ingest** (ADR 003's rules, now running where the
  plaintext is): for the four image types, metadata segments are stripped before encryption —
  segment removal, pixels untouched, because a photo must not carry GPS coordinates to its
  readers and the server can no longer do the stripping; a thumbnail is derived at ≤512px long
  edge, JPEG for photographs and PNG where alpha survives, and encrypted under the same key
  with the thumb AAD. Non-images are encrypted byte for byte.
- **Payload format v1**, the plaintext a client encrypts when a message carries files:
  the sentinel `"U+0000hamlaneh-msg-v1U+0000"` — a NUL byte, the ASCII tag, a NUL byte —
  followed by UTF-8 JSON
  `{ "text": …, "attachments": [{ "id", "key", "name", "type", "size", "width"?, "height"? }] }`
  — `key` base64, `id` the server-assigned attachment id, the rest the *real* metadata the
  server never sees. A message with no files stays the bare string it is today, so there is no
  flag day: readers dispatch on the leading NUL, which no composed message can begin with, and
  render a payload that claims the sentinel but does not parse as the honest cannot-display
  state rather than as text. Ten attachments of entries this size plus a 4000-character text
  fit far inside the existing 32 KiB ciphertext cap; the cap does not move.

## Decision 2 — what opens an old file is the key inside the message; its lifetime is the message's, and that is the stated trade

Q2 confirmed in its premise — yes, the answer is a key kept somewhere — and the somewhere is
chosen so that no new custody class enters the threat model. The key lives in exactly two
places. Server-side it exists only inside the stored MLS ciphertext of the attaching message,
unreadable to the server forever, deleted when the message is deleted (the envelope column is
erased in place today; the blob and its row go with it). Client-side it lives wherever the
decrypted message body lives: today that is session memory (the webapp resolves encrypted
bodies per session and persists no plaintext), and when the roadmap's own-message-history slice
lands its store carries the attachment entries with the text — a requirement this ADR places on
that slice explicitly, because a history store that restores the words but not the keys would
resurrect a conversation whose files are all dead links. ADR 010 already reserved the backup
section that store will ride in.

Is a long-lived key a thing this design refuses elsewhere? No — it refuses **server-reachable
and org-held** long-lived keys: no escrow, no reset, no key the operator could be compelled to
produce (ADR 010 decisions 2 and 4). Client-held long-lived secrets are already the design's
currency: verification records, the non-extractable backup key handle, and the planned history
plaintext itself. The per-file key joins that class and no other.

The honest statement of what changed, for the threat model rather than the changelog:
**attachments get message-grade secrecy, not epoch-grade secrecy.** The invariant this design
buys is single and checkable — *a file is openable exactly when the message that shared it is
readable, by exactly whoever can read it* — and both edges of that invariant are product facts:

- A member's device that can read the message opens the file months and hundreds of epochs
  later: key from the readable message, ciphertext refetched from the files origin under an
  ordinary signed URL. Nothing rotates, because nothing needs to: removal from the group ends a
  member's access to *future* messages and therefore future files, and what they already read
  they already had — the same concession messages make, no wider.
- A device that cannot read the message cannot open the file. A member who joins after the file
  was sent cannot decrypt the attaching message (the join boundary), so **new members cannot
  read old files**, and the README must say so as plainly as it says new members cannot read
  old messages — this is E2EE's join boundary applying to files because files are message
  content, not a defect to soften. Same for a fresh device with no restored history: until the
  history slice ships, a reload ends the readable life of past messages and their files
  together. Files are never *more* reachable than the words beside them, and never less.

Forward-secrecy accounting, stated: compromise of a member device that holds readable history
yields the old file keys along with the old words — one blast radius, not a new one — and
fetching the blobs additionally requires a live session to mint signed URLs. The server-side
ciphertext plus a key that never rotates means the operator who later obtains a member's
readable history can decrypt stored blobs from it; that is exactly the messages story, extended
to bytes the server was always going to store. Deleting the message (or the retention of the
future history store deleting its plaintext) is what forward secrecy means for a file.

## Decision 3 — opacity does the serving, the channel flag does the policing, and one real serving change is named

Q3 mostly refuted — the boundary needs **no new marker**: `channels.e2ee`, immutable and
already read by every choke point involved, fully determines an attachment's regime, because an
attachment is channel-scoped for life (upload names the channel; the claim binds to the row's
own channel; nothing re-homes a file). No column is added and no migration ships. But "opacity
is enough" is wrong in three specific places, and this decision fixes each:

1. **Upload must refuse plaintext metadata, not just plaintext bytes.** A filename and a
   declared content type are content — `salary-review.pdf` in a database column defeats the
   ciphertext beside it — and the server's image pipeline (sniff, strip, thumbnail) has nothing
   true to do to bytes it cannot read. So on an e2ee channel the upload takes ciphertext as
   produced by Decision 1, with the multipart filename equal to the literal placeholder
   `encrypted` and the declared content type absent or `application/octet-stream`; anything
   else is refused 400 with the new code `e2ee_metadata_in_clear`. Refusal rather than silent
   scrubbing, for `e2eeAtBirth`'s reason: a client that sent a real filename is leaking by bug,
   and coercion would hide the bug while the leak (already in transit to the adversary under
   test) recurs forever. The stored row carries the placeholder name, the octet-stream type,
   the ciphertext size, no dimensions, and `has_thumbnail` from whether a thumb part arrived —
   which is what makes serving need no change at all: `files_origin.go` already serves any row
   without proven dimensions as an opaque sandboxed download, so ciphertext rides the exact
   path an unproven blob rides today, 404s and all.
2. **The app must be able to read the bytes it decrypts.** Today an opaque blob is
   download-by-navigation only: `Cross-Origin-Resource-Policy: same-origin` and no CORS header
   mean the app origin's `fetch()` of the files origin is blocked — correct for a world where
   opaque bytes are only ever saved, fatal for one where the client decrypts them. The files
   origin therefore adds `Access-Control-Allow-Origin: *` to its blob responses. This is safe
   for the same reason the origin exists: it is deliberately cookie-less, so there is no
   ambient authority for CORS to launder — the signed URL is the entire credential, and CORS
   reveals nothing to a script that a bearer of the URL could not already fetch. `*` rather
   than the app origin because there is no credentialed state to scope, and a configured origin
   would be one more deploy-time string to get wrong in bare-IP and home modes.
   `deploy/verify-defaults.sh` gains the probe.
3. **File search must not surface the placeholder.** The filename search index would match
   every encrypted file to the literal query "encrypted"; the search query excludes e2ee
   channels' attachments instead (one predicate on the join it already makes). E2ee files are
   absent from server-side search — consistent with their words, which were never searchable —
   and a client-local index is the history slice's concern, not built here.

The read path, fixed as client rules with the force of the serving rules they replace, because
the ingest protections the server can no longer provide do not disappear — they move, and their
adversary changes from "uploader attacks the server" to "sender attacks the reader":

- Decrypted bytes are untrusted sender input. They are rendered inline only as the four proven
  image types, only via `<img>`/object URL, decided from the *decrypted* metadata; everything
  else is a save-only card using the download attribute. A decrypted `blob:` URL is **never**
  navigated to — a blob URL inherits the app origin, so navigating to sender-controlled HTML
  would be handing the sender the origin the files origin exists to protect. The e2e suite must
  prove script never executes from a decrypted upload, the same proof the plaintext suite
  already runs against the serving headers.
- Download-then-decrypt is whole-blob: single-shot AES-GCM authenticates everything before one
  byte is shown, and at the 25 MiB cap that is an acceptable memory shape. The named ceiling:
  no Range requests and no streaming playback of encrypted media; if the cap ever rises to
  where that hurts, the upgrade is a chunked STREAM-style construction, adopted then, not
  half-built now.

The write path end to end: the client uploads ciphertext (parts `file`, then optionally
`thumb`, ≤1 MiB, e2ee channels only — refused on plaintext channels where the server derives
its own), collects the returned attachment ids, and sends the message with those ids in the
existing `attachment_ids` field beside the `mls` envelope — the combination the contract
currently refuses and this ADR legalizes. The server validates exactly what it validates for a
plaintext send: membership and authz at upload before any byte is read, the caps, the claim of
every named id inside the message's own transaction against the channel the row was born in
(migration 0020's ordering included), idempotent resend claiming nothing. The
`e2ee_attachments_unsupported` code and its single writer are deleted; the orphan sweep,
signed-URL minting and expiry, and the 404 discipline are untouched. Edits do not alter the
attachment set (the edit request carries no ids today, and that stays); an e2ee edit's
re-encrypted payload **must re-carry the attachment entries**, because the stored ciphertext is
replaced whole and readers of the edited message would otherwise hold cards without keys — a
client rule with its own test.

Metadata conceded to the server, named per §6.1's discipline rather than waved at: that a file
exists, its ciphertext size (plaintext + 28 bytes, so effectively the size), upload time,
uploader, channel, which message claims it, and the count per message. Size padding is
deliberately not built — the same class ADR 010 deferred for backup blobs, priced and named,
pad-to-bucket ready if an audit ever prices the leak higher.

## Deliberately not decided

- **The history store's format** for carrying attachment entries — that slice's, under ADR
  010's blob section; this ADR only fixes that it must carry them.
- **Size padding** — refused above, named for a future audit.
- **Chunked encryption / streaming playback** — the named ceiling above; designed if
  `max_file_size_bytes` ever rises materially.
- **Client-local file-name search over decrypted metadata** — the history slice's, if ever.
- **Compliance-mode files** — no interaction exists to decide: e2ee attachments occur only in
  e2ee conversations, which Compliance mode cannot create (ADR 011), and plaintext channels
  keep the ADR 003 pipeline whole.
- **Multi-device and encrypted-attachment interplay** — inherits the messages answer wholesale
  (the key is message content; whatever lets a second device read the message lets it open the
  file); nothing separate to design.
- **Screens, cards and copy** — the design pipeline's: BRIEFS.md rows for the encrypted file
  card and its cannot-display state, mockups, STATUS.md; unstyled functional plumbing until
  mockups land, per CLAUDE.md.

## Contract and schema changes this ADR implies (the freeze list)

No further decisions are needed to implement these; the orchestrator freezes them before any
implementation agent spawns.

1. **`docs/api/openapi.yaml`:**
   - `SendMessageRequest.mls` description: `attachment_ids` is now permitted beside `mls` on an
     e2ee channel; the ids must name opaque uploads to the same channel; keys and real metadata
     travel inside the ciphertext (payload v1). The `e2ee_attachments_unsupported` sentence is
     deleted.
   - `POST /api/v1/channels/{channelId}/files`: document the e2ee-channel rules — multipart
     part `file` must carry filename exactly `encrypted` and content type absent or
     `application/octet-stream`, violations 400 `e2ee_metadata_in_clear` (new code); an
     optional `thumb` part may follow `file` (client-derived encrypted thumbnail, ≤ 1 MiB,
     400 `invalid_request` beyond it or on a plaintext channel). Effective request-body cap on
     e2ee channels is `max_file_size_bytes` + 1 MiB to admit the thumb; `max_file_size_bytes`
     itself is unchanged and applies to the uploaded (cipher)bytes, so a client budgets
     plaintext ≤ cap − 28.
   - `Attachment` schema: note the placeholder semantics of e2ee rows (`filename` =
     `encrypted`, `content_type` = `application/octet-stream`, `size_bytes` = ciphertext size,
     no dimensions; the card renders from decrypted metadata).
   - Error-code registry: add `e2ee_metadata_in_clear`; retire `e2ee_attachments_unsupported`.
2. **Migrations: none.** Verified against 0007's DDL — the placeholder satisfies the filename
   and content-type CHECKs; no column is added in either driver.
3. **Files origin** (outside openapi.yaml by ADR 003's design, changed at its code and its
   checks): blob responses gain `Access-Control-Allow-Origin: *`; `deploy/verify-defaults.sh`
   probes it; the app origin's CSP `connect-src` gains the files origin.
4. **Search:** the files-search query excludes attachments of e2ee channels, both drivers.
5. **Authz matrix: no new rows** — no new endpoint or WS type; the existing upload and send
   rows cover the changed behavior. `/security-review` is mandatory regardless: the slice
   touches upload, download and the e2ee boundary, and changes the contract.
6. **Client constants fixed here,** with shared test vectors per ADR 008's discipline: the two
   AAD strings, the payload sentinel `U+0000hamlaneh-msg-v1U+0000`, AES-128-GCM, 12-byte
   nonce-prefixed blob format, the `encrypted` filename placeholder, ≤512px thumbnails with
   the JPEG/PNG alpha rule, and metadata-segment stripping before encryption for the four
   image types.

## Same-PR updates this ADR requires (on acceptance)

Per CLAUDE.md "changing a decision": a PLAN.md §12 row (encrypted attachments: per-file
AES-GCM key carried inside the attaching message's ciphertext, opaque channel-scoped blobs,
message-grade secrecy with the join boundary applying to files — ADR 013). ROADMAP Phase 3:
the encrypted-attachments bullet becomes this slice with a pointer here. ADR 003 gains a
one-line note that ADR 013 extends its model to e2ee channels (placeholder metadata, CORS on
the blob routes, client-side ingest rules); ADR 009 decision 1 gains the one-line note that a
static per-file key sent as message content is outside its refusal, per this ADR — the courtesy
annotations ADRs 005/006/009 established. `docs/OVERVIEW.md` is untouched by the ADR itself;
the implementing slice updates the file-sharing line to say encrypted conversations share files
end-to-end encrypted, with the new-member limitation stated. The README's file-sharing claim is
brought honest in the same slice: files in encrypted conversations are E2EE, and history before
join — files included — is unreadable by design.
