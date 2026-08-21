# User settings — design handoff

Delivered 2026-08-21. Verbatim copy of `handoff/SETTINGS_HANDOFF.md` from the Claude Design
canvas so the repo is self-sufficient for implementation agents.
Canvas: <https://claude.ai/design/p/185f4552-36fc-489e-a9b1-c012ab74f6d6>

Mockup: `mockups/Hamlaneh Settings.dc.html`.
Tokens: reuses `webapp/src/tokens.css` unchanged — no new colour, type or spacing tokens.
Brief: `docs/design/BRIEFS.md` §4.

## What is on the canvas

Six artboards plus `settings-components`.

| Artboard | Notes |
|---|---|
| `settings-profile` | Avatar, display name, read-only username, admin-managed email. |
| `settings-security` | 2FA off, password change with the instance policy beside it, sessions summarised. |
| `settings-2fa-setup` | Step 1 of 3. QR placeholder with the manual key always visible. |
| `settings-2fa-recovery-codes` | Step 3 of 3. Warning above ten numbered codes; download, copy, print. |
| `settings-sessions` | Four devices: browser, approximate location with IP, last active, current badge. |
| `settings-security-dark` | Token-mapped counterpart, including the dimmed chat behind. |
| `settings-components` | Panel geometry, the Language and Appearance sections in full, 2FA step 2 and on-state, session and save edge cases, behaviour. |

`Language` and `Appearance` have no artboard of their own — both are single-choice panels, and
they are drawn at full fidelity on `settings-components` §02 instead of padding the count.

## Settings are a panel, not a page

Admin replaces the chat shell because it is a mode you enter. Settings float over it because
they are a detour: the conversation stays visible, and Escape puts you back in it. The panel is
`surface` at overlay elevation over the same scrim the chat drawer uses.

Panel 1040 × 720, radius 16 · header 68 · nav 220 with 44px rows · content padding 28/32 ·
form column 420–440 · scrim `text.primary` at 42%.

## Decisions made where the brief left them open

- **Panel over dimmed chat**, not a full page with its own nav — that would make settings and
  admin feel identical when they are different kinds of place.
- **One artboard per 2FA step**, with the step stated in words and in dots.
- **The manual 2FA key is always visible** under the QR, never behind a "Can't scan?"
  disclosure — someone without a working camera should not have to hunt for it.
- **QR is a labelled placeholder.** I cannot generate a scannable code; the artboard says the
  real one is rendered server-side from the secret shown beneath it.
- **Recovery codes** are two numbered columns of ten, as selectable text — never an image, so
  screen readers can read them and password managers can grab them.
- **Session rows** carry device, browser, approximate location with the IP, last active, and a
  current-device badge. The current row cannot sign itself out from its own row.
- **Typed fields need Save; single choices commit on selection** and show an inline Saved mark.
  Stated once here so the rule is not guessed per field.

## Implementation notes

- Escape closes the panel and restores focus to the sidebar gear; with unsaved edits it raises
  a leave-without-saving dialog first.
- The section nav is a tab list — arrow keys move between sections, content is the tab panel.
- Changing your password does **not** end other sessions, and the panel says so beside the form.
- 2FA setup never leaves a half-configured account: it stays off until the code verifies and the
  recovery codes are acknowledged.
- A wrong code in step 2 does not restart setup — the secret stays valid, the cells clear, focus
  returns to the first.
- Disabling 2FA re-asks for the password. If the org requires 2FA, the control is **absent**,
  not disabled with a tooltip.
- Regenerating recovery codes invalidates the entire previous set and says so.
- Locations are IP-estimated and labelled as approximate.

## Reuse

`OtpInput`, `PasswordField`, `NoticeBanner`, `PrimaryButton` and the radio group come from the
auth set unchanged. The toggle, select and confirm dialog come from admin. Genuinely new:
`SessionRow`, `RecoveryCodeList`, `ThemePreview`.

New icons: monitor · smartphone · app-window · upload · printer · languages · sun · user.

## Not in scope

Notification preferences, per-channel mute, blocked users, data export, account deletion.
None are in §4.

## Backend dependencies (added by the repo, not the designer)

This set cannot ship on the current backend. It needs, in order:

- **Slice 1.1b** — TOTP enrol/verify/disable, recovery-code generation and regeneration, and the
  `OtpInput` component this design reuses from the auth set. Until then the whole Security →
  Two-factor path has no endpoints.
- **Session list endpoints** — ROADMAP 1.1 "Sessions remainder": list the caller's session
  families with device, approximate location, last-active and current-device flags, plus
  per-family revocation. The `settings-sessions` artboard is a direct rendering of that list.
- **Profile update** — display name and avatar upload. Avatar upload additionally depends on the
  Phase 1.3 upload pipeline (content-type enforcement, EXIF strip, cookie-less serving origin).
- **Password policy** — the instance minimum shown beside the password form comes from the admin
  dashboard setting (ADMIN_HANDOFF: "The password minimum set here feeds every password screen"),
  which needs the policy endpoint recorded in ROADMAP 1.1.

Language and Appearance are the only sections implementable today: both are client-side
preferences already wired (`src/i18n`, `src/theme.ts`).
