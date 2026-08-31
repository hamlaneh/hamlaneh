# Hamlaneh WebSocket protocol — realtime contract for backend and frontend

Companion to [`openapi.yaml`](openapi.yaml), which owns the REST half of the contract and
carries the `GET /api/v1/ws` upgrade endpoint. Everything above the upgrade — frames,
operations, events, resume, close codes — is specified here, because OpenAPI cannot describe a
bidirectional frame protocol.

**This file is machine-read.** §10 is a table with one row per WebSocket operation, delimited by
HTML comment markers. The authz-completeness gate parses it and fails when an operation has no
entry in the WS authz registry (`server/internal/authztest`), exactly as the OpenAPI gate does
for REST endpoints. The OpenAPI parser cannot see this file, so §10 is the only place the WS
surface is enumerated for CI. Adding an operation means adding a row **and** a registry entry in
the same commit.

Change process is the same as the API contract: only the orchestrator edits this file, before
implementation agents are spawned.

---

## 1. Endpoint and handshake

```
GET /api/v1/ws
Upgrade: websocket
Connection: Upgrade
Origin: https://<instance public origin>
Cookie: hamlaneh_session=…
```

**Authentication is the session cookie on the upgrade request.** A token never appears in the
URL — query strings land in proxy logs, browser history and `Referer` headers. There is no
`?token=` parameter and there will not be one.

**Origin is checked strictly.** The `Origin` header must be present and must equal the
instance's configured public origin. A missing `Origin`, a mismatched one, or a `null` one is
rejected with HTTP `403` before the upgrade completes. This is the cross-site
WebSocket-hijacking defense: `SameSite` cookies do not protect WebSocket handshakes in every
browser, so the Origin check carries that load. No wildcard, no substring match, no
"same registrable domain" relaxation.

**The socket binds to the session family, not to one access token.** Refresh rotates the access
token every 15 minutes (see `sessions` in migration 0002); a socket bound to the token would
drop on every rotation. Binding to the family means the socket survives rotation and dies with
the family. Revoking a family — logout, remote device revocation, password change, offboarding —
**closes every socket in that family within 10 seconds** with close code `4401`. The server
enforces this with a periodic revocation sweep, not only on the next inbound frame, so an idle
socket cannot outlive its session.

**No permessage-deflate.** The server does not negotiate WebSocket compression. Frame sizes
are small (§2), and a shared compression context mixing one user's text with another's is a
length side-channel this protocol simply refuses to have rather than reason about per-frame.

**CSRF:** the `X-Hamlaneh-CSRF` double-submit header that guards mutating REST requests cannot
be sent on a browser WebSocket handshake. The Origin check above is the equivalent control for
this transport.

**Reserved: one-time ticket flow (not implemented).** Non-browser clients that cannot present a
cookie (a future mobile or CLI client) will exchange a session for a single-use, short-lived
ticket at `POST /api/v1/ws/ticket` and pass it in the `Sec-WebSocket-Protocol` header — never in
the query string. That endpoint does **not** exist in Phase 1.2, is deliberately absent from
`openapi.yaml`, and must not be implemented ahead of a slice that needs it. It is written down
here so nobody invents a query-string token when the need arrives.

### Protocol handshake

The first frame the client sends after the socket opens **must** be `hello`. Any other frame
first closes the socket with `4400`. A socket that sends nothing within 10 seconds of opening is
closed with `4400`.

```jsonc
// client -> server
{"type":"hello","id":"c1","ts":"2026-08-21T09:12:00Z",
 "data":{"protocol_version":1,"resume":[{"chan":"…uuid…","seq":417}]}}

// server -> client
{"type":"hello_ok","id":"c1","ts":"2026-08-21T09:12:00Z",
 "data":{"protocol_version":1,"user_id":"…uuid…","session_family_id":"…uuid…",
         "heartbeat_interval_seconds":30,"max_frame_bytes":65536,
         "resumed":[{"chan":"…uuid…","seq":417}],
         "resync":["…uuid…"]}}
```

The version is negotiated **once**, in this exchange, and never renegotiated on a live socket.
The client offers `protocol_version`; the server answers with the version it will speak. If the
server cannot speak the offered version it closes with `4400` and the close **reason**
`unsupported_protocol_version`, rather than silently downgrading. (A WebSocket close frame
carries a code and a reason string and nothing else — there is no `data` object to put it in.)
Protocol version is `1`.

A **second `hello` on a live socket closes it with `4400`.** "Negotiated once" is the rule;
re-offering a version is a client that has lost track of its own connection, and the safe answer
is to end it rather than guess which negotiation is in force.

`resume` is optional and covered in §5. On a fresh connect it is omitted.

---

## 2. Frame envelope

Every frame in both directions is a single JSON object, sent as one WebSocket **text** frame.
Binary frames are rejected (`4400`).

| Field | Type | Required | Meaning |
|---|---|---|---|
| `type` | string | yes | Operation or event name from §3 / §4. |
| `id` | string ≤64 | no | Correlation id, chosen by the client, echoed by the server on the reply and on `error`. Required on request/response operations, meaningless on events. |
| `chan` | uuid | conditional | The channel the frame concerns. Required on every channel-scoped operation and event. |
| `seq` | integer | no | Per-channel sequence number. Present only on ordered, replayable server events (§5). Never sent by the client. |
| `ts` | date-time | yes | RFC 3339 UTC. The server's `ts` is authoritative; a client `ts` is advisory and never trusted for ordering, expiry, or storage. |
| `data` | object | yes | The payload. `{}` when there is nothing to carry — never `null`, never a bare scalar. |

**Size limits.** A single frame is at most **64 KiB**, matching the REST body cap from slice
1.1a. A larger frame closes the socket with `4413` — the server does not attempt to read it.
Fragmented messages are reassembled only up to the same cap. Message content is capped at 4000
characters by the contract, so 64 KiB leaves generous headroom and no legitimate frame comes
close.

**Unknown types are tolerated, not fatal.** A receiver that does not recognise `type` **ignores
the frame** and keeps the socket open. This is what lets a server add an event before every
client has been updated, and lets an old client survive a newer server. Unknown *fields* inside
a known `data` object are ignored the same way. The one exception is the very first frame, which
must be `hello`.

**Malformed frames are fatal.** Invalid JSON, a missing `type`, a missing `ts`, a `data` that is
not an object, or a channel-scoped frame without `chan` closes the socket with `4400`. There is
no partial-parse recovery: an ambiguous frame is a bug or an attack, and neither deserves a
best-effort interpretation.

**A known field of the wrong type inside `data` is not fatal** — the frame is ignored and the
socket stays open, the same treatment an unknown `type` gets. The envelope is what has to be
trustworthy; a `presence` whose `state` arrived as a number is one unusable instruction, not an
unusable connection. The exception is `hello`: it is the frame that establishes what the socket
*is*, so an unusable payload there closes with `4400`.

---

## 3. Client → server operations

| `type` | `chan` | Reply | Payload |
|---|---|---|---|
| `hello` | — | `hello_ok` | `{protocol_version, resume?}` — §1. |
| `subscribe` | required | `subscribed` | `{}`. Opts this socket into the channel's ephemeral events (`typing`). |
| `unsubscribe` | required | `unsubscribed` | `{}`. |
| `typing` | required | none | `{}`. "I am typing in this channel, now." Fire-and-forget. |
| `presence` | — | none | `{state}` — `online` or `away`. The client reports its own idleness; `offline` is server-derived and cannot be claimed. |
| `ping` | — | `pong` | `{}`. Client-initiated liveness probe (browsers cannot send protocol-level ping frames). |
| `pong` | — | none | `{}`. Answer to a server `ping` — §6. |

Notes that matter:

- **The channel is the envelope's `chan`, never a field inside `data`.** Every channel-scoped
  event carries it at the top level, and no payload repeats it. Some older readers in this repo
  accept both, which is tolerance rather than permission — a payload that carries its own `chan`
  is two sources for one fact, and the envelope is the one.
- **Membership is checked on every channel-scoped operation, every time**, against the database
  or an invalidated cache — never against a membership set captured at connect. A user removed
  from a channel mid-socket stops being able to act on it immediately.
- **`subscribe` to a channel you are not a member of is an `error` frame with code
  `channel_not_found`**, never `forbidden` — the same non-leaking answer the REST paths give.
  The socket stays open; a wrong subscribe is a client bug, not an attack worth dropping the
  connection over. Repeated failures are rate-limited (§8).
- **Sending, editing and deleting messages is REST, not WebSocket.** One write path means one
  authz choke point, one validation implementation, one idempotency key, and one place for the
  rate limiter. The socket is the delivery channel; `POST /api/v1/channels/{id}/messages` is the
  submission channel. A queued message from the reconnect state is retried over REST with its
  original `client_msg_id`.
- **Read positions are REST too** (`PUT /api/v1/channels/{id}/read`), for the same reason. The
  socket carries the resulting `read_position` **event** to the user's own other sockets so a
  second tab or a desktop app clears its unread badge — that is own-device sync, and it is the
  only read-state traffic in the protocol.
- **There are no cross-user read receipts anywhere in this protocol.** No frame tells user A
  that user B read anything. Nothing in the design shows another person's read state, and the
  privacy default is to not build the capability.

---

## 4. Server → client events

Delivery scope is either **membership** (automatic on every channel the user belongs to, no
subscribe needed — the sidebar's badges depend on it) or **subscription** (only for channels
this socket has subscribed to).

| `type` | Scope | `seq` | Payload |
|---|---|---|---|
| `hello_ok` | socket | no | §1. |
| `subscribed` | socket | no | `{}` — echoes `id` and `chan`. |
| `unsubscribed` | socket | no | `{}` — echoes `id` and `chan`. |
| `message_created` | membership | yes | `{message}` — the `Message` schema from `openapi.yaml`. |
| `message_updated` | membership | yes | `{message}` — an edit, or a server-side enrichment (a link preview arriving). `edited_at` is set only by a real edit; enrichment must not stamp somebody's message "(edited)" for a card the server added. |
| `message_deleted` | membership | yes | `{message}` — soft delete; `deleted_at` set, `content` empty. Sent as the full message so the placeholder keeps its position and metadata. |
| `channel_created` | membership | no | `{channel}` — you created it, or somebody invited you. The sidebar adds a row. |
| `channel_updated` | membership | yes | `{channel}` — topic changed, or `member_count` moved. |
| `member_added` | membership | yes | `{chan, user}` — `UserSummary`. |
| `member_removed` | membership | yes | `{chan, user}` — `UserSummary`. Delivered to the members that remain. |
| `channel_removed` | own user | no | `{chan}` — this channel is gone *for you*: you were removed, or you left from another device. Drop the sidebar row and any subscription. |
| `read_position` | own user | no | `{chan, message_id, read_at}` — sent only to the *same user's* other sockets. |
| `typing` | subscription | no | `{user_id}` — expires client-side after ~5 s; there is no `typing_stopped`. |
| `presence` | membership, DM channels only | no | `{user_id, state}` — `online`/`away`/`offline`. |
| `call_started` | membership | no | `{started_by, participants}` — a call is now happening here. Membership scope rather than subscription is what makes a DM peer's client ring without having subscribed to anything. `started_by` exists only on this event: a ring is strictly live, and `GET .../call` cannot rebuild one after a reconnect because it does not carry who started the call. |
| `call_updated` | membership | no | `{participants}` — somebody joined or left, or started sharing a screen. |
| `call_ended` | membership | no | `{}` — the last participant left. Sent immediately on that departure, not when the room is eventually reaped, so a banner never claims a call that ended five minutes ago. |
| `mls_commit` | membership | no | `{epoch}` — a commit was accepted at this epoch; fetch the blob with `GET …/mls/commits?after_epoch=<your epoch>`. Notification only, deliberately: commit blobs can approach 256 KiB, which no frame under the 64 KiB cap can carry, and the commit log is durable where the replay buffer is not — so the event says something changed and REST says what is true, the same doctrine as calls. Missing this event costs nothing but latency: every client refetches the log on reconnect and on channel open. |
| `mls_welcome` | own user | no | `{}` — a Welcome awaits at least one of your devices; fetch `GET /api/v1/users/me/mls/welcomes`. Delivered to all the user's sockets because a Welcome is encrypted to one device's key package — a sibling device receives bytes it cannot open, so the fan-out reveals nothing. |
| `resync` | socket | no | `{chan}` — this channel's replay buffer could not satisfy your resume; backfill over REST (§5). |
| `ping` | socket | no | `{}` — §6. |
| `pong` | socket | no | `{}` — answer to a client `ping`. |
| `error` | socket | no | `{code, message}` — same envelope semantics as the REST `Error` schema: a stable machine code plus an English message the client localizes by code. Echoes `id` when the failing frame carried one. |

Notes that matter:

- **Presence is DM-scoped.** The design draws presence in exactly two places — the DM rows in
  the sidebar and the DM header — so `presence` events are delivered for the DM channels the
  user is a member of, and nowhere else. A member of `#deploys` learns nothing about who else is
  online. This is a privacy floor, not an optimization.
- **The three presence states**: `online` means a live socket exists; `away` means a client
  reported itself idle; `offline` means no socket after a short grace period (so a reload or a
  tunnel blip does not flash the peer offline).
- **A `typing` event is never echoed to its own sender.** The author already knows; rendering
  "you are typing" would be a bug baked into the transport.
- **`typing` has a protocol but no designed UI yet.** The frames are specified and implemented so
  the transport is settled; whether and how a typing indicator is drawn is a design question
  that has not been answered. Nothing renders it in Phase 1.2.
- **Removal is two events because the audiences are disjoint.** The members that remain get
  `member_removed` — they are still members, so `membership` scope reaches them. The removed
  user is *not* a member any more, so no membership-scoped event can legally reach them; their
  own sockets get `channel_removed` instead, which tells them only about their own state and
  names nothing that is now none of their business. On removal the server also drops the
  removed user's subscriptions to that channel itself — it must not wait for the client to be
  polite about it.
- **Events never leak across membership.** A socket receives an event for a channel only while
  its user is a member of that channel at send time. This is the WS half of the IDOR matrix and
  is tested per channel-scoped event type, not merely per endpoint.
- **`message_created` is not a delivery guarantee.** It is a fast path. Correctness comes from
  the REST history, which the client reconciles against on every resume (§5).

---

## 5. Reconnect and resume

Each channel carries a monotonically increasing **`seq`**, assigned by the server, incremented
once per ordered event on that channel (the `seq: yes` rows in §4). Ephemeral events carry no
`seq` and are never replayed: a typing indicator or a presence blip from thirty seconds ago is
worthless, and a call event from five minutes ago is worse than worthless — it would paint a
banner for a call nobody is in. Clients reconcile call state against
`GET /api/v1/channels/{id}/call` on opening a channel and after a reconnect. The events say
something changed; REST says what is true — as of the moment it was asked. A read is a round
trip, and a client that applies its answer unconditionally will undo any call event that arrived
while it was in flight, with no reconciliation point left to put the banner back. So an answer
the socket overtook is dropped rather than applied: whichever of the two spoke last wins, and an
event that landed after the read was issued is the later of them.

The server keeps a bounded per-channel **replay buffer**: the more recent of the last **256
events** or the last **5 minutes**. It is memory only. It is not durable, not a queue, and not a
source of truth — Postgres is.

On reconnect the client sends the highest `seq` it has processed for each channel it cares
about:

```jsonc
{"type":"hello","id":"c1","ts":"…",
 "data":{"protocol_version":1,"resume":[{"chan":"A","seq":417},{"chan":"B","seq":9}]}}
```

For each requested channel the server either

- **replays** every buffered event after `seq`, in order, before any live event for that channel
  — the channel is listed in `hello_ok.data.resumed`; or
- **cannot** (the client is further behind than the buffer reaches, the server restarted, or the
  socket landed on a different process) — the channel is listed in `hello_ok.data.resync`, and
  the client backfills over REST with
  `GET /api/v1/channels/{id}/messages?after=<after_cursor>` until the page returns no
  `after_cursor`. A `resync` event can also arrive mid-socket for a single channel.

**Resume is not a back door around membership.** The `resume` list is the client's claim about
what it once saw, and the server re-checks membership for every requested channel at `hello`
time: a channel the user is not a member of *now* — removed while disconnected, or never a
member at all — is never replayed. It is listed in `resync`, which reveals nothing (the list is
derived from the client's own request), and the REST backfill answers 404 exactly as it would
for any non-member, at which point the client drops the channel. Replayed events are ordinary
sends under §4's membership rule; buffering an event earlier never grandfathers a socket into
receiving it.

Falling back to REST is the **normal** path, not the failure path. A client that treats `resync`
as an error will be wrong most of the time it reconnects after a long sleep.

### Exactly once, in both directions

**Inbound (server → client): upsert by message id.** Every message-bearing event carries the
full `Message` with its stable `id`. A client that already holds that id replaces its copy
instead of appending. Replay overlap, a duplicated broadcast, and a REST backfill that overlaps
the replay window are therefore all harmless. Never key rendering on arrival order.

**Outbound (client → server): the idempotent resend key.** A message composed while the socket
was down is queued locally with a client-generated `client_msg_id` (the design's dashed "Waiting
to send" treatment) and retried over REST **with that same id, verbatim, on every attempt**.
The server's unique `(channel_id, author_id, client_msg_id)` makes the retry a lookup: a first
send is `201`, a resend is `200` with the message that already exists. Generating a fresh id on
retry defeats the whole mechanism and produces exactly the duplicate-message bug this design
exists to prevent.

The two halves compose: a message can be sent over REST, echoed by `message_created` on the
socket, and appear again in a resume replay and a REST backfill, and the client still shows it
once.

---

## 6. Heartbeat

The server sends `{"type":"ping"}` every **30 seconds** (the value is advertised in
`hello_ok.data.heartbeat_interval_seconds`, so it can move without a protocol version bump). The
client answers `{"type":"pong"}`. A socket that misses **two consecutive** pings — no `pong` and
no other inbound frame for ~75 seconds — is closed with `4408`.

Clients may also send `{"type":"ping"}` at any time and get `{"type":"pong"}` back; browsers
cannot send protocol-level ping frames, so this app-level pair is how a browser probes a socket
that has gone quiet behind a dead intermediary.

Heartbeats exist because a TCP connection through a proxy, a NAT, or a sleeping laptop can look
open for many minutes after it has stopped carrying bytes. Silence is not health.

---

## 7. Session revocation

Every socket records its session **family id** at connect. The server closes sockets with
`4401` — within **10 seconds** — when the family is revoked by any of: logout, remote device
revocation, refresh-token reuse detection (family revocation), password change (all other
families), or an admin offboarding the user (Phase 1.4).

Ten seconds is a hard budget, tested, not an aspiration. The check is a sweep on a timer, so a
socket that is receiving nothing and sending nothing still dies on schedule. On `4401` the
client stops reconnecting and returns to sign-in; retrying a revoked session in a loop is both
useless and a rate-limit trigger.

---

## 8. Close codes

Failures **before** the upgrade completes are HTTP status codes on the handshake response —
`401` (no or invalid session), `403` (Origin missing or not allowed), `429` (connect flood).
There is no socket yet, so there is no close code. Failures **after** the upgrade are WebSocket
close codes:

| Code | Meaning | Client should |
|---|---|---|
| `1000` | Normal closure (the client navigated away or signed out). | Not reconnect. |
| `1001` | Going away — server shutdown or deploy. | Reconnect with backoff; this is routine. |
| `4400` | Protocol error: malformed frame, binary frame, first frame not `hello`, unsupported protocol version. | Not blind-retry — this is a client bug. Log it. |
| `4401` | Session expired or revoked (§7). | Stop reconnecting; return to sign-in. |
| `4408` | Heartbeat timeout (§6). | Reconnect with backoff and resume. |
| `4413` | Frame exceeded the 64 KiB cap. | Not retry the frame. |
| `4429` | Rate limited. **Reserved, and unreachable by this design** — see the note below. | Reconnect after the backoff, slower. |

**Reconnect backoff** is exponential with full jitter, starting at 1 s, capped at 30 s. The
"No connection. Retrying in 8 s." banner in the design counts down this timer, so the client
owns the schedule and shows it rather than hiding it.

**`4429` is reserved and nothing sends it.** The close-code table lists it because a rate limit
that terminated a live socket would need one, but no such rule exists here: the connect budget
decides *before* the upgrade, when there is no socket to close and an HTTP `429` is the answer,
and the per-frame budgets deliberately keep the socket open and reply with an `error` frame —
a subscribe-storm is a client bug, not grounds to drop a connection somebody is reading in. The
code stays in the table so a future rule that does close a socket has one to use, and so nobody
reinvents a different number for it.

**Rate limits** apply per session family and per IP to: WebSocket connect and reconnect,
`subscribe` (a subscribe-storm is a cheap way to hammer membership checks), and `typing`.
Exceeding the per-frame budgets yields an `error` frame with code `rate_limited`; exceeding the
connect budget yields `429` on the handshake. Message send and
search are rate-limited on their REST endpoints.

---

## 9. Worked example

```jsonc
// open, authenticate, resume two channels
c->s {"type":"hello","id":"1","ts":"…","data":{"protocol_version":1,
        "resume":[{"chan":"A","seq":417},{"chan":"B","seq":9}]}}
s->c {"type":"hello_ok","id":"1","ts":"…","data":{"protocol_version":1,"user_id":"…",
        "session_family_id":"…","heartbeat_interval_seconds":30,"max_frame_bytes":65536,
        "resumed":[{"chan":"A","seq":417}],"resync":["B"]}}

// A replays; B is too far behind and is backfilled over REST
s->c {"type":"message_created","chan":"A","seq":418,"ts":"…","data":{"message":{…}}}
// client: GET /api/v1/channels/B/messages?after=<after_cursor>

// enter channel A: opt into its ephemeral events
c->s {"type":"subscribe","id":"2","chan":"A","ts":"…","data":{}}
s->c {"type":"subscribed","id":"2","chan":"A","ts":"…","data":{}}

// send is REST, delivery is WS; the echo carries the same client_msg_id
// client: POST /api/v1/channels/A/messages {"client_msg_id":"…","content":"On it."} -> 201
s->c {"type":"message_created","chan":"A","seq":419,"ts":"…","data":{"message":{…}}}

// heartbeat
s->c {"type":"ping","ts":"…","data":{}}
c->s {"type":"pong","ts":"…","data":{}}

// a wrong subscribe does not drop the socket
c->s {"type":"subscribe","id":"3","chan":"Z","ts":"…","data":{}}
s->c {"type":"error","id":"3","chan":"Z","ts":"…",
        "data":{"code":"channel_not_found","message":"No such channel."}}
```

---

## 10. Machine-readable operation table

CI parses the table between the markers below. Every row must have a matching entry in the WS
authz registry (`server/internal/authztest`); the completeness gate fails on a row without an
entry and on an entry without a row. Columns are fixed: `op`, `direction`, `scope`, `authz`.

- `direction` — `c2s` (client → server operation) or `s2c` (server → client event).
- `scope` — `transport` (the handshake itself), `socket` (concerns this connection only),
  `channel` (carries `chan`), or `user` (concerns the authenticated user).
- `authz` — the rule the registry must assert:
  - `public` — no session (nothing qualifies today; reserved).
  - `session` — any authenticated socket.
  - `member` — the user must be a member of `chan` **at the moment the frame is sent or
    received**; a non-member must receive `channel_not_found`, never `forbidden`, and must never
    receive the event.
  - `member-dm` — as `member`, and only for channels of kind `dm`.
  - `self` — delivered only to sockets of the same user.

<!-- ws-operations:begin -->
| op | direction | scope | authz |
|---|---|---|---|
| connect | c2s | transport | session |
| hello | c2s | socket | session |
| subscribe | c2s | channel | member |
| unsubscribe | c2s | channel | member |
| typing | c2s | channel | member |
| presence | c2s | user | session |
| ping | c2s | socket | session |
| pong | c2s | socket | session |
| hello_ok | s2c | socket | session |
| subscribed | s2c | channel | member |
| unsubscribed | s2c | channel | member |
| message_created | s2c | channel | member |
| message_updated | s2c | channel | member |
| message_deleted | s2c | channel | member |
| channel_created | s2c | channel | member |
| channel_updated | s2c | channel | member |
| member_added | s2c | channel | member |
| member_removed | s2c | channel | member |
| channel_removed | s2c | channel | self |
| read_position | s2c | channel | self |
| typing | s2c | channel | member |
| presence | s2c | channel | member-dm |
| mls_commit | s2c | channel | member |
| mls_welcome | s2c | user | self |
| resync | s2c | channel | member |
| call_started | s2c | channel | member |
| call_updated | s2c | channel | member |
| call_ended | s2c | channel | member |
| ping | s2c | socket | session |
| pong | s2c | socket | session |
| error | s2c | socket | session |
<!-- ws-operations:end -->

`typing`, `ping` and `pong` appear once per direction; the registry keys on `(op, direction)`,
not on `op` alone.
