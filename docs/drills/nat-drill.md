# The NAT drill

**What it proves:** that two people on networks which break naive WebRTC can hold a real meeting
on a stock Hamlaneh install. It is Phase 2's first test-gate item, and it is manual because the
thing under test is a pair of real networks, which CI does not have.

The automated relay-only test in CI proves the relay *path* works. This drill proves the
*deployment* works — that the ports are open, the external IP is right, and a person on a hostile
network can actually be heard.

## Before you start

The instance must be a **stock install**: `install.sh` on a real host, with only a domain or an IP
answered at the prompt. Nothing hand-edited. If you tuned something to make the drill pass, the
drill proved nothing about what other people will install, and the tuning belongs in the installer
or in the hardening guide instead.

Confirm the three ports are open in the host's **cloud** firewall, not only on the host — the
installer prints them at the end of every run and cannot reach a security group:

- `3478/udp` — TURN
- `7881/tcp` — ICE over TCP
- `7882/udp` — media

## Choosing the two networks

The point is a pair that defeats direct connection, so at least one side must be behind something
that does not do simple port mapping. In rough order of usefulness:

- **Mobile hotspot on a carrier using CGNAT.** The best single choice — most carriers, most of the
  time, and it needs no cooperation from anybody.
- **Corporate or campus network with an outbound firewall.** The realistic enterprise case, and
  the one most likely to find something the hotspot does not.
- **A symmetric-NAT home router.** Less common than it was; verify rather than assume.

What does *not* count: two machines on the same LAN, or two on ordinary home connections. Those
connect directly and prove nothing about TURN.

Record which pair you actually used in the log below. "A hotspot and an office" is not a record —
the carrier and the firewall vendor are what another person needs to repeat it.

## The drill

Five continuous minutes, video and screen share, both directions.

1. Sign in on both machines, as two different accounts.
2. Start a call from a DM.
3. Turn on video on both sides. Confirm each side **sees and hears** the other — not that the tile
   appeared, that the picture moves and the sound arrives.
4. Share a screen from one side; confirm the other sees it. Then reverse it.
5. Leave both running for **five unbroken minutes**. Watch for freezes, audio dropouts, and
   reconnect banners.
6. While it runs, confirm the media is actually relayed on the hostile side rather than having
   found a direct path anyway — otherwise the drill tested the easy case. In Chrome,
   `chrome://webrtc-internals`, the active connection's selected candidate pair: the local
   candidate type should be `relay` on at least one side. If both sides say `host` or `srflx`,
   the networks were not hostile enough — pick a harder pair and run it again.

## What counts as a pass

All of:

- Five minutes with no reconnect and no freeze longer than a moment.
- Both directions of audio and video, and screen share seen by the other side.
- At least one side relayed.
- Nothing about the instance was changed to make it work.

Anything short of that is a failure with a finding, and the finding is worth more than the pass.
Write it down even if you fix it in the same sitting.

## The log

Append a row per run. A drill nobody recorded is a drill nobody can repeat.

| Date | Networks (be specific) | Relayed side | Result | Finding |
|---|---|---|---|---|
| | | | | |

## If it fails

In the order worth checking:

1. **Ports.** The most common cause by a wide margin, and it fails silently — a call that never
   connects looks identical to a call that is broken. Check the cloud firewall, not the host.
2. **The advertised address.** If the node advertises an address no client can reach, nothing
   connects from outside. Drill 001 covers what the relay does with ports and why the relay range
   is deliberately unpublished; the escape hatch for an unusual topology — an elastic IP behind a
   gateway that will not hairpin — is `node_ip`.
3. **Only-TLS-on-443 networks.** ADR 005 names this as an accepted gap: the default install has no
   TURN over TLS, because Caddy owns the certificate and the port. If the hostile side permits only
   443, this drill will not pass and is not expected to. That is a known limitation, not a bug —
   record it as such and move on.
