# Drill 001 — What LiveKit's embedded TURN actually does with ports

**Run:** 2026-08-27 · **Against:** `livekit/livekit-server:v1.13.6` in the Phase 2 slice-1 compose
stack · **Why:** ADR 005 slice 1 exists to falsify three assumptions before the rest of Phase 2 is
built on them.

**Outcome:** assumptions 1 and 3 hold as written. Assumption 2 is wrong in its wording — there
*is* a relay port range and ADR 005 does not mention it — but right in its conclusion: the range
needs no publishing, proven by two real browsers exchanging media under forced relay with only the
three ADR ports open. The port list does not grow.

Everything below is observed output from a running stack, not documentation.

## 1. Embedded TURN runs cert-less over UDP — CONFIRMED

`turn: {enabled: true, udp_port: 3478}` and nothing else. No `domain`, no `cert_file`, no
`key_file`, no `tls_port`. The server starts:

```
service/turn.go:220  Starting TURN server  {"turn.relay_range_start": 30000,
  "turn.relay_range_end": 40000, "turn.per_user_relay_allocation_limit": 12, "turn.portUDP": 3478}
```

A certificate is demanded only on the `tls_port` path; `turn.go` returns `TURN domain required`
only when `TLSPort > 0`. A **real RFC 5766 Allocate** against the published `3478/udp`, using the
credentials LiveKit issued to a participant over the signal connection, succeeded:

```
advertised_ice_server        = turn:103.1.213.222:3478?transport=udp
allocate (no credentials)    = error, as it must be
turn_realm                   = livekit   (challenged with a nonce)
allocate (with credentials)  = SUCCESS
```

There is exactly one ICE server in the JoinResponse and no separate `stun:` entry — UDP TURN
doubles as STUN. Credentials are per-participant and expiring, minted by LiveKit, never by us.

## 2. It relays in-process, needing no further published ports — NOT AS WRITTEN

**In-process: true.** The relay is inside the LiveKit process. No second container, no coturn, no
sidecar.

**"No additional port range": false.** Allocations come from a real port range, `relay_range_start`
30000 to `relay_range_end` 40000, which ADR 005 does not mention. Sockets appear on demand. During
two live allocations, inside the container:

```
UNCONN  172.22.0.2:7882    <- media mux            (published)
UNCONN     0.0.0.0:34458   <- TURN relay socket    (NOT published)
UNCONN     0.0.0.0:30445   <- TURN relay socket    (NOT published)
UNCONN           *:3478    <- TURN                 (published)
```

and the address handed to the client was `XOR-RELAYED-ADDRESS = 103.1.213.222:30445` — the node's
**external** IP, on a port that is not published. `turn.go` confirms the shape:
`RelayAddressGeneratorPortRange{RelayAddress: nodeIP, Address: bindAddr, MinPort: 30000, MaxPort: 40000}`.
The socket binds on the bind address; the *advertised* address is the node IP.

That address is what the peer — our own SFU, in the same container — must send ICE checks to. In
Docker **bridge** mode it cannot. Sending from inside the container to each candidate address on an
unpublished port:

```
send to 127.0.0.1:39999    -> DELIVERED to the in-container listener
send to 172.22.0.2:39999   -> DELIVERED   (the container's own address)
send to 172.22.0.1:39999   -> LOST        (a HOST-owned address, port not published)
```

`172.22.0.1` is the bridge gateway, i.e. an address the host owns — the local analogue of a VPS's
public IP, which is what `use_external_ip` makes the node IP. A packet addressed there leaves the
container, reaches the host, and finds nothing listening, because the relay socket lives in the
container's namespace and no DNAT rule exists for an unpublished port.

The mirror direction does work, because that port *is* published — hairpin DNAT carries it back in:

```
sent    172.22.0.2.38501 > 172.22.0.1.7882
seen    172.22.0.1.38501 > 172.22.0.2.7882    (delivered to the media mux)
```

So the client→relay→SFU direction completes and the SFU→client-relay-candidate direction does not.
Which left one question deciding the whole thing: **does ICE nominate a working pair on the
client's checks alone?** Measured below. It does.

For completeness: LiveKit's own firewall documentation lists 3478/udp, 7881/tcp and 7882/udp and
does **not** list the relay range, and its deployment guides run the container on host networking,
where the question disappears because the node IP is an address of that namespace.

## 2b. Does media actually flow under forced relay? — YES, and the range is a non-issue

Two headless Chromium contexts, both joining one room through `livekit-client`, both forced to
gather **relay candidates only**. Bridge mode, only the three published ports, relay range
unpublished throughout.

```
                 T+6s                              T+12s
alice  ice=connected conn=connected  nominated=true  state=succeeded
       local  = relay/udp  relayProtocol=udp  172.31.99.1:37438     <- unpublished relay port
       remote = host/udp                      172.31.99.1:7882      <- published media mux
       inbound audio   53 270 B / 290 pkt  ->  111 156 B / 592 pkt
       inbound video  220 322 B / 258 pkt  ->  752 508 B / 776 pkt
bob    ice=connected conn=connected  nominated=true  state=succeeded
       local  = relay/udp  relayProtocol=udp  172.31.99.1:37927     <- unpublished relay port
       remote = host/udp                      172.31.99.1:7882
       inbound audio   51 928 B / 301 pkt  ->  107 205 B / 602 pkt
       inbound video  227 496 B / 264 pkt  ->  728 413 B / 756 pkt
```

1. **Connected**, both peers, no ICE failure.
2. **Nominated pair: local candidate type `relay`** over UDP, on ports 37438 and 37927 — inside
   the 30000–40000 range and never published. Remote is the SFU's published `7882` host candidate.
3. **`inbound-rtp` grows on both sides**, audio and video, roughly 1 Mbit/s per peer. Real media,
   not a handshake that merely looks green.

The control, run against that same live stack so the result cannot be a false positive — the
failure mode was still present while media flowed:

```
172.31.99.2:39999  (container's own IP, unpublished)  -> DELIVERED
172.31.99.1:39999  (node IP, unpublished — relay range) -> LOST
172.31.99.1:7882   (node IP, published)               -> DELIVERED (hairpin DNAT)
```

So the SFU genuinely could not originate a packet to either client's relay candidate, and the call
worked anyway. The mechanism is the one ICE is built on: the browser's relay allocation sends
checks to the SFU's published port, the SFU answers the **source address it observed**, the pair
validates and is nominated, and media rides that same 5-tuple in both directions. Nothing ever
needs to address `<nodeIP>:<relayPort>` from outside.

**Conclusion: the relay range needs no publishing, and ADR 005's three ports stand.** Recording the
reason is worth a paragraph in the ADR, because the range is real and the next person to read
`ss -lnup` will find sockets on it.

One boundary worth naming: this holds wherever the node IP is an address the host owns, which is
the normal VPS shape. Where it is not — an AWS Elastic IP behind a NAT gateway that will not
hairpin — the relay's outbound to `<nodeIP>:7882` fails too, but so does ordinary non-relay media,
so it is not a relay-specific risk and `node_ip` is the existing escape hatch.

### What the harness changed, and why that does not invalidate it

A test that passes by altering the thing under test proves nothing, so: three harness-only
overrides, none touching the published port list, the hardening, the UDP mux, embedded TURN or
auto-create.

1. `use_external_ip: false` + `rtc.node_ip: 172.31.99.1`, with the edge subnet pinned so that
   address is known before boot. The shipped default discovers the public IP by STUN; on this
   machine that is a CGNAT address no local browser can reach. The gateway is the faithful local
   stand-in precisely because it is **host-owned**, like a VPS's public IP — which is what makes
   the relay port unreachable from the container, the property under test. The control above
   confirms it stayed unreachable.
2. `turn.allow_restricted_peer_cidrs: [172.31.99.0/24]`. LiveKit's TURN refuses to relay toward
   private peer IPs; in production the SFU's candidate is public and permitted by default. Without
   this the drill would have failed on the wrong thing.
3. `iceTransportPolicy: 'relay'` forced in the `RTCPeerConnection` constructor rather than trusting
   the library to pass it through — which is why `local.type == "relay"` in the numbers above is
   evidence and not an assumption.

Signalling went straight to `ws://livekit:7880` from inside the compose network rather than
through Caddy, to keep TLS out of a media test. The `/rtc` proxy path is proven separately, by
`verify-defaults.sh` and by a real signal session driven through `wss://localhost/rtc`.

## 3. The whole configuration comes from the environment — CONFIRMED

`LIVEKIT_CONFIG` carries the entire config body and `LIVEKIT_KEYS` the credential pair. Nothing is
mounted and nothing is written. After a real room join and two TURN allocations:

```
status=running health=healthy restarts=0 readonly=true user=65532:65532 mounts=0 tmpfs=map[]
caps_dropped=[ALL]
writes/permission errors in the log: 0
touch /probe     -> Read-only file system
touch /tmp/probe -> Read-only file system
```

**Zero writable directories** — not even `/tmp`. The container needs no volume, no tmpfs and no
config mount.

## Two smaller things worth knowing

- **External-IP discovery costs a STUN round trip at boot** (~5s here) and, on a host with no
  reachable public address, logs `could not validate external IP` and falls back to the discovered
  IP anyway. Harmless, but it is why the healthcheck has a 20s `start_period`.
- **A localhost or LAN install advertises a node IP no local browser can reach**, because
  `use_external_ip` finds the public address. Local end-to-end call testing will need
  `rtc.node_ip` overridden; that belongs to slice 4's harness, not to the shipped default.

## Reproducing 2b

Harness override, applied as a second `-f` on top of `deploy/docker-compose.yml`. It is kept out
of `deploy/` on purpose: compose auto-loads `docker-compose.override.yml`, and a harness file
sitting there would silently become production config.

```yaml
services:
  livekit:
    environment:
      LIVEKIT_CONFIG: |
        port: 7880
        bind_addresses:
          - ""
        rtc:
          tcp_port: 7881
          udp_port: 7882
          use_external_ip: false
          node_ip: 172.31.99.1
        turn:
          enabled: true
          udp_port: 3478
          allow_restricted_peer_cidrs:
            - 172.31.99.0/24
        room:
          auto_create: true
        logging:
          level: info
networks:
  edge:
    ipam:
      config:
        - subnet: 172.31.99.0/24
          gateway: 172.31.99.1
```

The driver runs in `mcr.microsoft.com/playwright:v1.62.1-noble` attached to `hamlaneh_edge`, with
`--use-fake-device-for-media-stream --use-fake-ui-for-media-stream`. It mints both join tokens
itself from `HAMLANEH_LIVEKIT_API_KEY`/`_SECRET` (HS256, `roomJoin`/`canPublish`/`canSubscribe`
on one room), serves the page from `http://127.0.0.1:8000` inside the container — a secure origin,
so `RTCPeerConnection` and `getUserMedia` are permitted while plain `ws://` is still not mixed
content — and shims `RTCPeerConnection` to force `iceTransportPolicy: 'relay'` and collect every
instance for `getStats()`.

When slice 4 turns this into the relay-only CI test, the two pieces worth keeping are the
constructor shim (it makes relay-only a measured fact rather than a trusted option) and the
control probe (without it, a green run cannot be distinguished from a relay port that happened to
be reachable).
