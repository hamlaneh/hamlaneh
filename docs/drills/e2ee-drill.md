# The compromised-server drill

**What it proves:** that a person holding everything the server holds — its database, its disk,
its keys, and its power to mint tokens for its own media rooms — gets ciphertext and nothing
else. It is Phase 3's first test-gate item, and it is the drill the whole phase exists to pass.

Two halves, and they are not the same kind of exercise. The **message half is automated** and
runs in CI; the **media half is manual**, because it needs somebody to stand in the server's
position deliberately. Both are written here so the audit trail is one document rather than a
test file plus an oral tradition.

## Why a packet capture is not the drill

The obvious version — capture traffic at the SFU, show it does not decode — proves nothing, and
it is worth saying why in the file somebody will reach for when they want to shorten this.

LiveKit media is SRTP. Every hop is already encrypted, so a captured payload is unreadable
whether or not media E2EE exists at all: the drill would pass identically against a build that
had never heard of MLS. It measures SRTP, which nobody doubted.

The adversary this phase names (PLAN §6.1, adversary 3) does not sit on the wire. It sits **after
SRTP terminates**, inside the server, where media is decrypted for routing — and it can mint
itself a join token for any room, because minting tokens is the server's job. So the drill takes
that position on purpose: join the room as the server could, and try to listen.

That distinction is the reason ROADMAP's gate item 1(b) was reworded; the note under the gate
records it.

## Half one — messages (automated)

Covered by `webapp/e2e/specs/e2ee-messaging.e2e.ts`, which runs against the real compose stack in
CI. What it asserts, and why each part is there:

1. A canary is typed into the real composer of an encrypted channel and read on a second
   browser — so the thing under test is a message that actually worked, not a failed send that
   trivially left no plaintext.
2. The stored row is read from the real table: `content = ''` and `mls_ciphertext IS NOT NULL`.
   The contract's invariant, checked against the database rather than the API that promised it.
3. A full `pg_dump` from inside the database container must not contain the canary.
4. **The control**: the same dump must contain a plaintext `control` string sent to an ordinary
   channel at the same moment. Without it, a scan that found nothing would be indistinguishable
   from a scan that could not find anything, and the pass would be vacuous.

To run it by hand: `npm run e2e -- e2ee-messaging` from `webapp/`. It starts and stops its own
stack under its own project name and cannot touch a running instance's volumes.

## Half two — media (manual)

**What you need:** a stock install, two browsers signed in as two accounts who share an encrypted
channel, and shell access to the host — which is exactly what the adversary is assumed to have.

**Step 1 — a real call.** Start a call in the encrypted channel from both browsers. Confirm each
side sees and hears the other. A drill against a call nobody could hold proves nothing.

**Step 2 — take the server's position.** On the host, mint a join token for that room using the
instance's own LiveKit API key and secret from its `.env`, as the server does. Join the live room
with a bare LiveKit client — no Hamlaneh session, no MLS state, no key provider. This is the
operator, or anybody who has taken the operator's place.

**Step 3 — try to listen.** Subscribe to the participants' tracks. What must happen: the tracks
arrive, and the frames do not decode. Expect decrypt failures and no renderable video or audible
audio. Record what you saw.

**Step 4 — the control, and do not skip it.** Repeat steps 1–3 against a **conference** room
(`conf-`), which is outside E2EE by design (ADR 006 decision 3). The same no-key subscriber must
decode that room fine — video renders, audio plays.

The control is the whole difference between a result and a wish. A `chan-` room that fails to
decode means nothing on its own: a typo in the token, the wrong room name, a subscription that
never started, or a probe that was broken all along would each produce the same silence. The
conference decoding proves the probe works, so the silence in the encrypted room is a fact about
the encryption rather than about the probe.

**Step 5 — the mid-call membership case.** With the call still running, remove one of the two
people from the channel. The removed participant's media must stop being decodable to the
remaining member once the remaining member's client has merged the removal commit and rotated
the key. This is the media form of the property ADR 007 restored for messages, and it is the
reason the key is derived per epoch rather than fixed for the call (ADR 009 decision 3).

## What this drill does not prove

Stated here rather than left for a reader to assume the opposite, per PLAN §2.4:

- **Metadata is not protected.** The SFU sees who is in a call, when, for how long, and who is
  speaking. The drill says nothing about that and neither may any UI string.
- **Conferences are not end-to-end encrypted at all** — that is what step 4 demonstrates, and it
  is a design decision rather than a gap.
- **It does not test a hostile directory.** A server that lies about whose key is whose is ADR
  008's residue, closed by the verification ceremony and not by this drill.
- **It is a point-in-time result.** It says the build you ran it against behaved correctly; it is
  not a substitute for the cryptography audit PLAN §6.8 gates the word "secure" on.

## Recording a run

Keep the result the way the NAT drill's is kept: date, the commit it ran against, the two
browsers and the host, what step 3 showed, what step 4 showed, and anything surprising. A drill
whose result nobody wrote down did not happen.
