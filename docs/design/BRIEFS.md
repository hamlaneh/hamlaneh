# Hamlaneh — Screen Design Briefs

Functional requirements per screen, written for the design pipeline: the user feeds the
relevant section (plus §0) to ChatGPT to produce a Claude Design prompt; the resulting mockups
land in [STATUS.md](STATUS.md) and Claude implements them faithfully.

These briefs define **what each screen must contain and do** (content, components, states).
Visual identity — layout, color, typography, spacing, mood — is entirely the designer's
territory. Where a brief mentions a component, the designer decides what it looks like.

**Priority order:** ~~§1 Login~~, ~~§2 Chat shell~~, ~~§3 Admin dashboard~~, ~~§4 User settings~~
(**all delivered 2026-08-21**) → §5 Calls (Phase 2, not yet needed) → the three chat screens the
chat set omitted (create channel, invite picker, DM picker — see STATUS.md).

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

---

*Maintained alongside the product: when a screen's scope changes in ROADMAP.md, its brief
changes here in the same commit (Definition of Done).*
