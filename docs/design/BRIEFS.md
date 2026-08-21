# Hamlaneh — Screen Design Briefs

Functional requirements per screen, written for the design pipeline: the user feeds the
relevant section (plus §0) to ChatGPT to produce a Claude Design prompt; the resulting mockups
land in [STATUS.md](STATUS.md) and Claude implements them faithfully.

These briefs define **what each screen must contain and do** (content, components, states).
Visual identity — layout, color, typography, spacing, mood — is entirely the designer's
territory. Where a brief mentions a component, the designer decides what it looks like.

**Priority order:** §1 Login (needed now, Phase 1.1 is starting) → §2 Chat shell (Phase 1.2)
→ §3 Admin dashboard (Phase 1.4) → §4 User settings (Phase 1.3–1.5) → §5 Calls (Phase 2, not
yet needed).

---

## 0. Shared foundation (applies to every screen — include with every prompt)

**Product context.** Hamlaneh (هم‌لانه, "shared nest") is a self-hosted team communication
platform (chat + calls), open source, installed by companies on their own servers. Closest
neighbors: Slack, Mattermost, Rocket.Chat — but Hamlaneh's identity is *calm, trustworthy,
owned-by-you*; it should not read as a corporate SaaS clone. The name means "shared nest" in
Persian — warmth and belonging are on-brand.

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

## 1. Login — PRIORITY 1 (needed now)

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
| `login-force-password-change` | Admin created the account with a temporary password: new password + confirm fields, inline password-strength/requirements hints, "Set password and continue" |
| `reset-request` | "Forgot password" flow step 1: email field + "Send reset link" + back-to-login. Confirmation state text: "If that address exists, a reset link is on its way." (same message whether the account exists or not) |
| `reset-new-password` | Flow step 2 (from the emailed link): new password + confirm + requirements hints + "Reset password" |

**Also show:** the `login-default` artboard in dark theme and one Persian/RTL variant.

**Notes for the designer:** the page may carry a subtle instance identity later (org logo set
in the admin dashboard) — leave a slot where an org logo could appear above/near the wordmark
without breaking the layout when absent.

---

## 2. Chat shell (main app) — Phase 1.2

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

## 3. Admin dashboard — Phase 1.4

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

## 4. User settings — Phase 1.3–1.5

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

## 5. Call / meeting view — Phase 2 (do NOT design yet — listed for completeness)

Participant grid with active-speaker emphasis; screen-share layout (share large, faces rail);
control bar: mic, camera, screen share, leave (distinct/destructive), participants, chat
side-panel toggle; pre-join screen (camera preview, device pickers, mic/cam toggles, "Join");
states: connecting, reconnecting, participant muted/camera-off tiles. Full brief will be
written when Phase 2 starts.

---

*Maintained alongside the product: when a screen's scope changes in ROADMAP.md, its brief
changes here in the same commit (Definition of Done).*
