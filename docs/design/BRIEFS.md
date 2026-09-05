# Hamlaneh — Screen Design Briefs

Functional requirements per screen, written for the design pipeline: the user feeds the
relevant section (plus §0) to ChatGPT to produce a Claude Design prompt; the resulting mockups
land in [STATUS.md](STATUS.md) and Claude implements them faithfully.

These briefs define **what each screen must contain and do** (content, components, states).
Visual identity — layout, color, typography, spacing, mood — is entirely the designer's
territory. Where a brief mentions a component, the designer decides what it looks like.

**Priority order:** ~~§1 Login~~, ~~§2 Chat shell~~, ~~§3 Admin dashboard~~, ~~§4 User settings~~
(**all delivered 2026-08-21**), ~~§5 Calls~~ (delivered 2026-08-29), ~~the three chat screens the
chat set omitted~~ (create channel, invite picker, DM picker — delivered as the §2 addenda)
→ **§3 addendum, the admin surfaces drawn by nothing** (five of them, all shipping unstyled or
unbuilt) → the E2EE surfaces STATUS.md lists as `awaiting-design`.

---

## 0. Shared foundation (applies to every screen — include with every prompt)

**Product context.** Hamlaneh (هم‌لانه, "shared nest") is a self-hosted team communication
platform (chat + calls), open source, installed by companies on their own servers. Closest
neighbors: Slack, Mattermost, Rocket.Chat — but Hamlaneh's identity is *calm, trustworthy,
owned-by-you*; it should not read as a corporate SaaS clone. The name means "shared nest" in
Persian — warmth and belonging are on-brand.

**The design system already exists — build on it, do not restart it.** The auth set was
delivered on 2026-08-21 as the *Quiet Nest* system: tokens (colour, type scale, spacing,
radius, elevation, motion) live in `webapp/src/tokens.css`, the written contract in
[LOGIN_HANDOFF.md](LOGIN_HANDOFF.md), and the canvas at
<https://claude.ai/design/p/185f4552-36fc-489e-a9b1-c012ab74f6d6>. Every new screen set reuses
those tokens, the same Inter/Vazirmatn pairing, the same focus and disabled treatments, and
extends the existing component vocabulary rather than inventing a parallel one. Feed the
tokens file and the handoff to the design pipeline along with the section below.

**Hard requirements every screen must satisfy:**

1. **Bilingual + RTL.** Every screen ships in English (LTR) and Persian (RTL). The RTL version
   is a true mirror (navigation, icons with direction, alignment) — not just translated text.
   Design the primary artboards in English; deliver at least one Persian/RTL artboard for the
   most layout-critical screen of the set so mirroring intent is unambiguous.
2. **Fonts must be bundleable** (self-hosted product, no CDNs at runtime). Recommended pairing:
   Inter (Latin) + Vazirmatn (Persian) — both open source; the designer may choose others with
   an open license. Persian text must never be an afterthought: check line-height and glyph
   density with real Persian strings.
3. **Light and dark theme.** Both, from the start. Deliver both variants at least for Login
   and the Chat shell; other screens may ship light-first with tokens that map to dark.
4. **Desktop-first, responsive.** Primary artboard 1440×900. The same UI is wrapped by a
   desktop app (Tauri) and must degrade gracefully to mobile web (375 wide) — a mobile artboard
   is welcome for the Chat shell, optional elsewhere.
5. **Accessibility.** WCAG AA contrast; visible focus states on every interactive element;
   never color as the only signal; hit targets ≥40px.
6. **States are part of the design.** Every async surface needs: loading, empty (with guidance,
   not a blank void), and error states. Destructive actions get confirmation affordances.
7. **No text baked into images.** All copy is real text (it gets translated).
8. **Design tokens.** Use a consistent spacing scale and named colors — the implementation is
   Tailwind-based and tokens transfer directly.

**Deliverable format:** Claude Design canvas with clearly named artboards, one per screen
state (e.g. `login-default`, `login-error`, `login-totp`, `chat-default-dark`, `chat-rtl-fa`).

---

## 1. Login — DELIVERED (2026-08-21; see LOGIN_HANDOFF.md)

**Purpose:** the front door of a company's private instance. Calm, minimal, instills trust.
There is **no self-registration** (it's off by default) — no "Sign up" link. Users get accounts
from their admin.

**Artboards needed (7):**

| Artboard | Content |
|---|---|
| `login-default` | Product name/wordmark "Hamlaneh", identifier field (accepts username *or* email — one field, label "Username or email"), password field with show/hide toggle, "Sign in" button, "Forgot password?" link, language switcher (EN ⁄ فا) visible without signing in |
| `login-error` | Same + inline error banner: "Incorrect username or password." (deliberately generic — never reveals which was wrong) |
| `login-rate-limited` | Same + non-dismissable notice: "Too many attempts. Try again in a few minutes." Fields disabled |
| `login-totp` | Second step after correct password: 6-digit one-time code input (auto-advancing boxes or single field — designer's call), "Verify" button, "Back" link, note text "Enter the code from your authenticator app" |
| `login-force-password-change` | Admin created the account with a temporary password: new password + confirm fields, inline password-strength/requirements hints, "Set password and continue", and a low-emphasis "Sign out" escape link (for someone who signed into the wrong account) |
| `reset-request` | "Forgot password" flow step 1: email field + "Send reset link" + back-to-login. Confirmation state text: "If that address exists, a reset link is on its way." (same message whether the account exists or not) |
| `reset-new-password` | Flow step 2 (from the emailed link): new password + confirm + requirements hints + "Reset password" |

**Also show:** the `login-default` artboard in dark theme and one Persian/RTL variant.

**Notes for the designer:** the page may carry a subtle instance identity later (org logo set
in the admin dashboard) — leave a slot where an org logo could appear above/near the wordmark
without breaking the layout when absent.

---

## 2. Chat shell (main app) — DELIVERED (2026-08-21; see CHAT_HANDOFF.md)

**Purpose:** where people live all day. One screen, three permanent regions + one overlay.

**Regions & components:**

- **Sidebar (start side):** org name (+ org logo slot); channel list grouped into *Channels*
  (public `#` / private lock icon) and *Direct messages* (avatar + presence dot: online/away/
  offline); unread = bold + count badge; mention badge visually distinct from plain unread;
  "+" affordances to create/join a channel and start a DM; current-user footer: avatar,
  display name, presence, buttons for settings and (if admin) the admin dashboard.
- **Channel header:** channel name + topic (truncating), member count, search input (searches
  messages & files), channel actions menu.
- **Message list:** day separators; consecutive messages by the same author group under one
  avatar/name/timestamp; markdown rendering (bold/italic/code/code-block/links/lists/quotes);
  edited marker ("edited"); deleted placeholder ("Message removed"); hover/long-press actions:
  edit, delete, copy link; **"New messages" divider** for unread position; file attachments:
  image thumbnail card + generic file card (name, size, download); link-preview card (title,
  description, thumbnail).
- **Composer (bottom):** multiline input that grows; attach-file button; send button; hint row
  for markdown; disabled state with reason when disconnected.
- **Connection banner (overlay, top):** "Reconnecting…" / "Back online" — WebSocket drops are
  a real, designed state.

**Artboards needed (8):** `chat-default` (busy channel, light), `chat-default-dark`,
`chat-empty-channel` (freshly created — guidance + invite affordance), `chat-loading-history`,
`chat-reconnecting`, `chat-dm` (direct message view), `chat-search-results` (results panel or
overlay — designer's call), `chat-rtl-fa` (full Persian mirror of `chat-default` — this is the
RTL-critical screen of the whole product), plus a `chat-mobile` 375-wide variant (sidebar
becomes drawer).

---

## 3. Admin dashboard — DELIVERED (2026-08-21; see ADMIN_HANDOFF.md)

**Purpose:** the control room. Installed with the product, reached from the app on a separate
path, admin-only. Because public registration is off, **every user is born here** (or via an
invite link generated here). Should feel calm, powerful, and administrative — clearly a
different mode from chat, but the same family.

**Sections (own nav within the dashboard):**

1. **Users:** table — avatar, username, display name, email, role (Admin/Member), status
   (Active/Deactivated), created date; row actions: deactivate (with confirm — states that all
   their sessions die), reactivate, force password reset, change role. Primary action button
   **"Create user"** → modal/form: username, email (optional), display name, temporary
   password (with "generate" button), role, language (en/fa); after create: success state
   showing the credentials once, with copy button and "they must change this at first login".
2. **Invites:** create invite link (expiry picker, single-use), list of open invites
   (link-copy, created-by, expires, revoke), empty state.
3. **Org settings:** org name, org logo upload (shown on login + sidebar), default language
   (en/fa), registration mode (invite/admin-only — the default — vs open, with a warning on
   open), security policies: enforce 2FA for everyone (toggle + consequence note), session
   lifetime, password minimum length.
4. **Audit log:** filterable table — time, actor, action, target, source IP; export button;
   read-only.

**Artboards needed (7):** `admin-users` (populated), `admin-users-empty` (fresh install —
guidance to create the first users), `admin-create-user` (modal open), `admin-invites`,
`admin-org-settings`, `admin-audit-log`, `admin-users-dark`.

---

## 3 addendum. Admin surfaces the delivered set does not draw

Everything in this section is **built and shipping unstyled today**, except the org logo, which
is not built at all. These are not new ideas: four of them arrived after 2026-08-21, when the
admin set was drawn, and one (the logo) was asked for in §3 above and never came back. Each
currently renders as plain semantic HTML with no delivered treatment borrowed, because the
pipeline forbids inventing one — so each is a real screen an administrator meets, wearing
nothing.

Per-surface status, and the questions each artboard has to answer, are in
[STATUS.md](STATUS.md) under the row named in each heading. This section is the other half:
what the screen must contain.

**Read first:** [ADMIN_HANDOFF.md](ADMIN_HANDOFF.md) — all of this extends the delivered admin
set (260px rail, 40px gutter, the `ADMINISTRATION` kicker, the records-table treatment) rather
than starting a parallel one.

### 3a. Org logo (STATUS row "Org logo")

The one thing §3 asked for that was never built, and the reason it is here rather than in the
implementation queue: it cannot be built until it is drawn, because it appears on two screens
that already exist and neither has a slot for it.

- **Admin → Org settings:** the control. Current logo (or the absence of one), a file picker,
  and remove. Everything else on that screen saves on blur; an upload does not, so it needs a
  between-state of its own.
- **Sign-in:** where §1 of this document asked for "a slot where an org logo could appear
  above/near the wordmark without breaking the layout when absent". The delivered auth set drew
  the product name as text and no slot, so this is that request, restated with the constraint
  made explicit: **the absent case is the common case** — every install has no logo on day one,
  and most will never set one.
- **Chat sidebar:** §2 lists "org name (+ org logo slot)" in the sidebar. Same absent-case rule,
  plus a second one: the org *name* is already the first thing in the sidebar, so a logo beside
  it is either identity or duplication and the artboard has to pick.

Product facts the design cannot change:

- The sign-in page is **unauthenticated**, so the logo is a public asset fetched before any
  session exists. It cannot use the signed, hour-lived, cookie-less files origin every other
  upload uses (ADR 003). Anyone who can reach the sign-in page can fetch it.
- Uploads are made on the admin origin and displayed on the app origin (ADR 015), so the
  reference is rooted or absolute, never relative to where it was set.
- One asset, unless the artboard says otherwise: a dark-ink logo on the dark canvas is
  invisible, and asking for two files is a real product decision, not a detail.

States: absent (both display screens, and the admin control), chosen-but-not-yet-uploaded,
uploading, uploaded, refused (wrong type, too large, wrong aspect), removed.

### 3b. Organization encryption mode (STATUS rows "Organization encryption mode", "Encryption-mode switch dialog")

On `admin-org-settings`, and the **one setting on that screen with a ceremony**: everything else
there saves as you type, and this one has its own endpoint, its own audit entry, and a
confirmation, because it decides what every conversation created afterwards is
([ADR 011](../adr/011-encryption-mode.md)).

The section must contain:

- What the instance does **now**, as a sentence, not a label — "every new conversation is
  end-to-end encrypted" rather than "Mode: strict".
- A permanent count of the conversations the current mode does **not** describe, **shown at
  zero as well as above it**. A figure that appears only when non-zero teaches an administrator
  that silence means the mode covers everything, which is the one thing it never means.
- Two choices, **Strict** and **Compliance**, each with a line saying what it does.
- **Compliance is shown and unavailable.** Not hidden — hiding it teaches nobody what the
  product will offer — and not offered, because it is honest only once encryption at rest,
  retention and compliance export exist, and a mode delivering nothing but the absence of E2EE
  is the dishonest toggle. Its reason sits beside it and must not read as an error: nothing is
  broken.

The **switch confirmation** is a separate surface and carries three or four load-bearing
sentences, none of which can be cut to fit a one-line confirm dialog:

1. Nothing already stored changes. No conversation is converted, in either direction.
2. The new mode begins with what is created after it.
3. Choosing Compliance: a complete export of the past is **impossible**, and that is the
   product working — the server holds no key for what is already encrypted. This has to read
   as the guarantee, not as a failure.
4. How many conversations will sit outside the mode being **chosen** (not the one in force).

States: current mode strict, current mode compliance, the confirmation in each direction, the
switch failing, and the count at zero.

### 3c. Provisioning tokens (STATUS row "SCIM provisioning tokens")

A **fifth nav row**, appended after "Audit log" so the drawn four keep their positions, and it
has no glyph of its own — it currently borrows the shield from the settings Security rail.

The only credential in the product that belongs to a machine rather than a person: an identity
provider's sync engine authenticates with it, so there is no cookie and no CSRF header
anywhere near it.

Must contain: a sentence saying what these are for; a way to mint one, taking an optional note
naming what it is for; the minted value **shown exactly once** (the server keeps only a hash of
it — the same show-once step `admin-create-user` and the invite link already use); a list of
existing tokens with note, created, and last used; and revoke, with the confirm that invite
revocation already uses.

The design problem: **`Last used` is empty on every token nobody has configured yet**, which is
unremarkable an hour after minting and a real signal a month later, and the screen cannot tell
those apart.

States: no tokens yet (fresh install, and the common case), tokens listed, the show-once panel
open, revoke confirmation, and the list failing to load.

### 3d. Just-in-time provisioning (STATUS row "Just-in-time provisioning toggle")

A switch inside the **account-creation panel** on `admin-org-settings`, which that artboard
draws as a radio group and one warning note and nothing else. It belongs there rather than in
Security because it answers the same question the registration mode answers — how an account
comes into existence here — but it is not part of that choice and must not be greyed out when
the radio group saves.

It carries three hint lines under one label: what it does, that it is *not* the registration
setting, and — on an instance with no identity provider configured — that there is nothing for
it to govern. That is one more hint line than any drawn control has.

### 3e. The whole admin set at 375 (STATUS row "Admin dashboard at phone width")

Worth answering in the same pass rather than a second one. The set is drawn at 1440 with a
260px rail and a 40px gutter, which leaves roughly 75px of table on a phone. Below 899 it
currently stacks — the rail becomes a horizontal strip above the content, the gutter drops to
16px, the document scrolls as one — which is a stopgap, not a design.

What a phone artboard has to settle: whether a records table is a table at all or a stack of
rows; where the org identity, the exit and the signed-in-as footer go when three stacked blocks
of chrome sit above the first row; and whether the row menu, 260px wide and anchored to the
row's end edge, becomes a sheet.

---

## 4. User settings — DELIVERED (2026-08-21; see SETTINGS_HANDOFF.md)

**Purpose:** personal preferences and self-service security. Reached from the sidebar footer.

**Sections:**

1. **Profile:** avatar upload/remove, display name, email (read-only if admin-managed).
2. **Language:** English / فارسی choice (applies immediately, flips direction).
3. **Security:** change password (current + new + confirm); **two-factor authentication**:
   off-state with "Set up" → setup flow (QR code artboard + manual key fallback, then
   6-digit confirmation, then one-time recovery codes list with download/copy and a "store
   these safely" warning) → on-state with "Disable" (requires password);
   **Active sessions:** list of devices/sessions — device/browser, approximate location or IP,
   last active, "current" badge, per-row "Sign out" + "Sign out everywhere else".
4. **Appearance:** theme choice — Light / Dark / System.

**Artboards needed (6):** `settings-profile`, `settings-security` (2FA off), `settings-2fa-setup`
(QR step), `settings-2fa-recovery-codes`, `settings-sessions`, `settings-security-dark`.

---

## 5. Call / meeting view — Phase 2 (**design now**; Phase 2 started 2026-08-27)

Architecture these screens sit on: `docs/adr/005-calls-and-meetings.md`. Read it for what is and
is not built — several things a call UI usually has are deliberately absent, and drawing them
would describe a product that does not exist.

### Screens

| Artboard | What it is |
|---|---|
| `call-prejoin` | The step between clicking Join and being in the room |
| `call-grid` | Everyone in the call, nobody sharing |
| `call-screenshare` | Somebody is sharing; faces demoted to a rail |
| `call-banner` | The strip in a channel saying a call is happening, for people not in it |
| `call-ring` | The 1:1 incoming-call toast |
| `meet-guest` | The page a conference link opens for somebody with no account |
| `call-rtl-fa` | Full Persian mirror of `call-grid` — the direction check, not a translation |

### `call-prejoin`

Camera preview, microphone and camera toggles, device pickers for both, and Join. This screen
exists because joining a call with the wrong camera on is the mistake everyone makes once.

It must also carry the case where permission was refused or no device exists — a person on a
desktop with no webcam still joins, audio-only, and the screen has to say so without reading as
an error.

### `call-grid`

Participant tiles with active-speaker emphasis. What a tile shows when the camera is **off** is
the important half, because in a real call most tiles are: name, and an avatar or initials — the
identity treatment from the chat sidebar is the obvious source, and whether it is borrowed or
restated is the designer's call.

Per-tile states: speaking, muted, camera off, connection poor, and reconnecting. Muted and
speaking are the two a person scans for constantly, so they need to survive at the smallest tile
size the grid produces.

**Control bar:** microphone, camera, screen share, participants, and leave. Leave is
destructive-adjacent and must not sit where a mis-click reaches it — it ends the call for the
person clicking, not for everyone, and the difference should be unmistakable.

**Not drawn, because not built** (ADR 005): no raise-hand, no reactions, no moderator controls —
there is no channel role model to hang them on — no recording indicator, no chat panel inside the
call (chat stays in the channel behind it), no participant count badge on a call in progress
beyond what the banner carries.

The grid must answer what happens at 2, 3, 5, and roughly 12 participants, and what it does
beyond that. A phone-width layout is a separate question the artboard has to answer explicitly
rather than by shrinking.

### `call-screenshare`

Share large, faces in a rail. Two things to decide: where the rail goes at 1280 versus at phone
width, and what the sharer themself sees — the "you are sharing" state is the one people forget
and then leak a window they meant to close.

### `call-banner`

A call is happening in this channel and the reader is not in it. It carries who is in the call
and a way to join. It appears and disappears on live events, so it needs an entry that does not
shove the message list.

This is the only call surface a person sees without having chosen to be in a call, so it is the
one most able to annoy.

### `call-ring`

Somebody is calling in a DM. Caller identity, accept, dismiss.

**Deliberately thin, per ADR 005:** there is no decline-versus-busy distinction, no missed-call
message afterwards, and no ring timeout — dismissing is dismissing the toast, and the caller
learns nothing about why. Draw what that honestly is rather than implying a state machine behind
it.

### `meet-guest`

The page a conference link opens for somebody with **no account on this instance** — the only
unauthenticated screen in the product besides sign-in.

It asks for a display name and joins. It must not look like a sign-up, and must not imply the
visitor is getting an account, because they are not: they get one room and nothing else.

Also needed: the state where the link is dead. Unknown, expired and revoked all answer
identically and the screen cannot tell them apart — one honest sentence and a way out, in the
shape `RedeemInviteScreen`'s unusable state already uses.

### `call-rtl-fa`

Full Persian mirror of `call-grid`. The control bar order, the screen-share rail side, and the
active-speaker emphasis all have a direction; the artboard is where that is settled rather than
guessed in CSS.

**Numerals:** ASCII digits, per the locked correction of 2026-08-21 (`CHAT_HANDOFF.md`).
Participant counts and call durations are app-generated numbers and follow it.

### States every screen needs

Connecting, reconnecting after a drop, and the call ending because the server restarted — ADR 005
says a LiveKit restart ends every call, so "the call ended and it was not you" is a real state, not
an edge case.

Empty conference: a link-holder arrives before anybody else. Waiting alone in a room is a state,
and it should not read as broken.

## Key verification (Phase 3, slice 3.3 — [ADR 008](../adr/008-key-verification.md))

Two surfaces, both currently unbuilt and both carrying a security meaning that the visual design
has to keep honest rather than soften.

### `verification-sheet`

Per person, reached from a conversation. Shows the safety number: sixty decimal digits in twelve
five-digit groups, identical on both people's screens, plus a QR for same-room comparison. The
number is the whole point, so it is the largest thing on the screen and must be readable aloud
over a phone call — grouping and spacing carry that, not decoration.

Three states it must draw, and they are not interchangeable: **unverified** (a number to compare,
no badge), **pinned** (this device recorded these keys on first sight without a ceremony — real,
weaker, and it must not look like verified), and **verified** (the humans compared and matched).
The visual distance between pinned and verified is a security property: a design that renders
them alike would make an unceremonied acceptance look like a proof.

**Numerals:** ASCII digits in both locales, per the locked correction — a safety number read
aloud in Persian has to be the same string the other person sees.

### `verification-changed`

Replaces the composer when a member's device keys have changed since this device accepted them.
It has to say who, and what changed — a new device, or a replaced key — because those read very
differently to a person deciding whether to worry.

Two exits, and no third: run the ceremony, or accept explicitly. The design must not offer a
dismiss, a "not now", or anything that returns the composer without a decision — ADR 008 calls
each of those "silently encrypt to the new key wearing a delay". Reading and receiving continue
normally in this state, so the warning belongs where the composer was, not over the history.

The prompt about **your own account** (a device was registered to you; is it yours?) is the same
component pointed at the reader, and it is the loudest one in the slice.

## Media E2EE (Phase 3, slice 3.4 — [ADR 009](../adr/009-media-e2ee.md))

Three surfaces, and all three exist to make a claim precise rather than to decorate a call.

### `call-encrypted-indicator`

On a DM or channel call: the media is end-to-end encrypted and the server relays what it cannot
read. What it must **not** imply is metadata protection — the SFU still sees who is in the call,
when, and who is speaking. The design problem is that a true claim about content sitting beside
an absent claim about metadata reads, to most people, as a claim about both.

### `conference-plain-label`

A conference is not end-to-end encrypted, in either mode, because a guest has no key of their
own. The join surface has to say the server can access audio and video, and the unqualified word
"encrypted" may not appear on it — the call still uses TLS and SRTP, and saying "encrypted"
without saying which kind is the overclaim §2.4 forbids. The design problem is stating that
plainly without making a working, deliberate feature look broken or second-rate.

### `call-publish-blocked`

The mid-call form of slice 3.3's warning: somebody's device keys changed, so this device stops
publishing until the person decides. Its two exits are the verification ceremony's, unchanged.
The design problem is specific to a call: the user's own camera and microphone have just stopped
for a reason that is not an error and not a network failure, and the screen has to say so fast
enough that they do not start debugging their hardware.

---

*Maintained alongside the product: when a screen's scope changes in ROADMAP.md, its brief
changes here in the same commit (Definition of Done).*
