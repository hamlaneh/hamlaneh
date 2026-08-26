# ADR 003 — File serving, signed URLs, and the egress guard

**Status:** accepted, 2026-08-26 · **Owner:** orchestrator · **Scope:** Phase 1.3

## Context

Phase 1.3 adds the two riskiest capabilities in the product so far: accepting bytes from users
(uploads) and fetching URLs users typed (link previews). The first invites stored XSS, content
smuggling, decompression bombs and disk exhaustion; the second is a server-side request forge
waiting to be aimed at the instance's own network. The contract (openapi.yaml v0.4.0) fixes the
API shapes; this ADR fixes the security model those shapes rest on, so four implementation
agents cannot each invent a slightly different one.

## Decisions

**One authorization rule for files.** An upload is scoped to a channel at upload time
(`POST /api/v1/channels/{channelId}/files`, membership checked there), and a file is readable
exactly by the members of its channel. No file exists outside a channel; a file never attached
to a message is swept after 24 hours. This keeps files inside the existing matrix model —
non-member gets the same 404 as everywhere else — instead of adding a second ACL system.

**Serving is by signed, expiring URL — the URL is the credential.** The files origin
(`files.<domain>`, provisioned by Caddy, routed to the same Go binary) is deliberately
cookie-less, so uploaded content can never script against the app origin and a request for it
carries no ambient authority. Authorization therefore happens when the URL is *minted*, not when
it is served: every serialization of an `Attachment` for an entitled reader signs fresh URLs
(HMAC-SHA256 over id, variant, expiry; key generated at install/first-run, never committed),
TTL one hour. Stale or tampered URLs answer 404. The cost is accepted and stated: anyone handed
a fresh URL can fetch the bytes for up to an hour — the same property a pre-signed S3 URL has —
and revoking membership does not recall URLs already minted.

**Bytes are sniffed; labels are decoration.** The declared content type is stored as the card's
label. Only images (jpeg, png, webp, gif) are ever served inline, and only after the bytes sniff
as that type (mismatch → 415). Everything else — SVG, HTML, XML, PDF, anything — is an opaque
blob served with `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff`, and a
sandboxing CSP, on both serving modes. Bare-IP/home mode (no separate origin available) serves
from the app origin with the same three headers; the Playwright suite must prove script never
executes in either mode.

**Images are stripped at ingest, not at serving.** EXIF and other metadata segments are removed
from the stored original (metadata-segment removal, not re-encoding — pixels stay untouched),
because the original is downloadable and a photo shared in a chat must not quietly carry GPS
coordinates. Thumbnails (≤512px long edge) are derived from the stripped original. Dimension
limits are enforced *before* decode (header-declared pixels ≤ 40MP) so an image bomb costs a
header parse, not a decode.

**Blobs live on the filesystem, keyed by id.** Under one data root (volume in compose, data dir
in home mode), path derived only from the server-generated UUID — client filenames never touch
the filesystem, which is what makes path traversal structurally impossible rather than filtered.
Postgres holds metadata only.

**The egress guard validates the IP it dials, not the name it resolved.** Link-preview fetches
run through one guarded HTTP client: DNS is resolved and the *dialed* address checked against
the blocklist (loopback, RFC 1918, link-local 169.254/16 and fe80::/10, CGNAT 100.64/10,
unique-local fc00::/7, multicast, unspecified) — checked in the dialer itself so a DNS rebind
between resolve and dial changes nothing. Redirects re-run the guard per hop, capped at 3.
Timeouts and size caps: 5s total, 512 KiB of HTML, 5 MiB of preview image. Preview images are
fetched through the same guard, stored as bounded derivatives, and served from the files origin
— a reader's browser is never made to fetch a stranger's server, and the `img-src 'self'` CSP
already refuses it. Only ports 80/443 are dialed.

**Preview enrichment is asynchronous and observable.** Send returns immediately; the first
http(s) URL in the content is fetched off the request path, and a successful preview updates the
message and emits `message_updated` — which is why ws-protocol §4's description of that event
now says "an edit, or a server-side enrichment"; `edited_at` is set only by real edits.

## Consequences

- A new migration (0007) adds `attachments` and relaxes `messages_content_shape` to allow empty
  content on live messages — an image with no caption is an ordinary message; the "text or
  files, never neither" rule is enforced in the send transaction, which is also where
  attachment ids are claimed atomically.
- Lock order gains `attachments` after `messages`.
- The files origin's routes live outside openapi.yaml on purpose: the completeness gate would
  demand session-principal matrix rows for endpoints whose whole design is to carry no session.
  Their tests live with the serving code and in the e2e suite instead.
- `GET /api/v1/instance` publishes `max_file_size_bytes` (default 25 MiB) so clients refuse
  doomed uploads before spending the bandwidth.
