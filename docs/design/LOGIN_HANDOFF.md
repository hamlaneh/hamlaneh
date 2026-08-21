# Login & password reset — design handoff

Delivered 2026-08-21. Verbatim copy of `handoff/LOGIN_HANDOFF.md` from the Claude Design
canvas so the repo is self-sufficient for implementation agents.
Canvas: <https://claude.ai/design/p/185f4552-36fc-489e-a9b1-c012ab74f6d6>

Mockup: `Hamlaneh Auth.dc.html` (Claude Design canvas).
Tokens: `handoff/auth-tokens.css` — copied verbatim into `webapp/src/tokens.css`.
Brief: `docs/design/BRIEFS.md` §1.

## What is on the canvas

In review order: a working `live-shell`, then ten artboards, then the foundation sheet, then
the current unstyled implementation for reference.

| Artboard | Notes |
|---|---|
| `login-default` | 1440×900, LTR, light. Organization slot collapsed. |
| `login-error` | Generic credential error, identifier preserved, neither field marked. |
| `login-rate-limited` | Non-dismissable warning; identifier, password, toggle and submit disabled. |
| `login-totp` | Six cells, `dir="ltr"`, no resend. |
| `login-force-password-change` | Requirements checklist + four-segment strength, `Set password and continue`. |
| `reset-request` | Email field, `Send reset link`, `Back to sign in`. |
| `reset-request-confirmation` | Enumeration-safe copy, no identity shown. |
| `reset-new-password` | Same requirement treatment, `Reset password`. |
| `login-default-dark` | Token-mapped counterpart, identical geometry. |
| `login-rtl-fa` | Full spatial mirror, Vazirmatn, real Persian copy. |
| `auth-foundations-and-components` | Tokens, type, all component states, five composed async frames, behaviour, Persian reference, font/icon plan. |

## Geometry

- Shell 1440×900. `InstanceIdentity` 576px (40%), auth region 864px (60%).
- Form column 424px, optically raised 44px above the mathematical centre.
- Language switcher 26px from the top, 30px from the logical end edge.
- Identity panel padding 64px. Controls 48px tall, targets 44×44 minimum.
- Same geometry in every state — switching state never moves the page.

## Components

`AuthShell` · `InstanceIdentity` · `OrganizationLogoSlot` · `ProductWordmark` ·
`LanguageSwitcher` · `AuthForm` · `TextField` · `PasswordField` · `PrimaryButton` ·
`NoticeBanner` · `OtpInput` · `PasswordRequirements` · `BackLink`

Every state on the canvas is a variant of these thirteen.

## Decisions made where the brief left them open

- **Forgot-password link** sits on the password label row, at the logical end. Keeps the form
  to one column and the CTA last.
- **OTP** is six discrete cells (60×60, 8px gap) rather than one segmented field — paste
  distributes across all six, backspace walks back, focus lands on the last pasted cell.
- **Decorative motif**: three nested arcs, `border.subtle`-adjacent tone, bottom of the
  identity panel, `aria-hidden`, mirrored with `scaleX(-1)` in RTL. Delete it and the panel
  still reads as finished.
- **Disabled state** is carried by a dashed 1px boundary plus tone, so it never relies on colour.
- **Organization example**: "Sanjab Cooperative", a fictional two-square mark. The slot renders
  nothing when absent — no box, no reserved height.
- **Strength meter** is four segments plus a text label; the label is the accessible signal.

## Implementation notes

- Autocomplete: identifier `username`, sign-in password `current-password`, new passwords
  `new-password`, reset email `email`, code `one-time-code`.
- Enter submits. Validate on blur and on submit, never per keystroke. Submission blocks
  duplicates and swaps to a stable-width busy label: `Signing in…`, `Verifying…`,
  `Setting password…`, `Sending…`, `Resetting…`.
- Credential failure: `role="alert"`, identifier kept, password cleared, focus to the alert.
- Reset confirmation: `role="status"` — does not steal focus.
- Password minimum (12 on the artboards) is instance policy served with the form, not a constant.
- Fonts self-hosted: Inter and Vazirmatn, both OFL 1.1. No CDN. Weights: 400 body, 500 labels
  and links, 600 headings and buttons.
- Icons: Lucide (ISC), inline SVG, 24×24 viewBox, 1.75 stroke, `currentColor`. Ten glyphs.
  Only `arrow-left` mirrors in RTL.
- Persian: line-height 1.65–1.75, no negative tracking. Email, URLs and codes stay LTR inside
  the RTL page; identifier field is `dir="auto"`.
- Responsive: ≥1280 as drawn · 1024 panel to 34% · <900 panel collapses to a brand header ·
  375 24px gutters, full-width form, `min-height:100dvh` with safe-area padding.

## Not in scope

No self-registration, no `Sign up`, no social login or SSO, no `Remember me`, no CAPTCHA,
no passkeys, no marketing copy, no security claims. The only supporting line is
`Self-hosted team communication`.

## Implementation status (maintained by the repo, not the designer)

- Slice 1.1a re-skin: `AuthShell`, `InstanceIdentity`, `OrganizationLogoSlot`,
  `ProductWordmark`, `LanguageSwitcher`, `AuthForm`, `TextField`, `PasswordField`,
  `PrimaryButton`, `NoticeBanner`, `PasswordRequirements`; artboards `login-default`,
  `login-error`, `login-rate-limited`, `login-force-password-change`, `login-default-dark`,
  `login-rtl-fa`.
- Deferred to slice 1.1b (endpoints do not exist yet): `OtpInput`, `BackLink`, and the
  `login-totp`, `reset-request`, `reset-request-confirmation`, `reset-new-password` artboards.
  Also deferred: `NoticeBanner` success/info tones, the four unused icons, enabling the
  Forgot-password link, and a password-policy endpoint to feed the requirements list.

### Open questions back to the designer (implementation deviated only where forced)

1. ~~**Banner space is not reserved.**~~ **RESOLVED 2026-08-21 — artboard wins.** The foundation
   sheet reserves a 46px collapsed block; `login-default` is drawn without it. Decision: follow
   the artboard; the form may shift ~35px when an error appears. Update the foundation sheet.
2. **Strength meter — RESOLVED 2026-08-21, with four follow-ups below:** the drawn visual (four segments + text label) is
   the design; the missing pieces are scoring logic and the three unnamed labels, which the
   design owner delegated to implementation. Built with a transparent length-and-variety
   heuristic and four labels; it is a rough guide, never a security claim (CLAUDE.md principle 4).
   If the designer later specifies a scale, it replaces this one.
3. **Requirements list reduced to one item.** "Not one of your last 3 passwords" and "Different
   from your username" are not enforced by the server and the first is not client-checkable;
   rendering them would be a false claim. Only "At least 12 characters" ships. The component
   takes a list, so the rest drop in when the policy exists.
4. **Forgot-password link** has no endpoint until 1.1b; it renders in the design's own disabled
   link state rather than as a dead link.
5. **Two controls the artboards omit** but the flow requires: a `Current password` field on the
   forced-change screen (the API requires `current_password`) and a low-emphasis sign-out escape
   (BRIEFS §1). Both use treatments drawn elsewhere in the mockup.
6. **Dark motif stroke**: artboard 9 draws `#2F3E39`; the token sheet and the mockup's own live
   shell both say `#26332F`. The token value was used.
7. **Disabled link tone** is drawn as `#8A9490`, which no token carries; `--color-border-disabled`
   (`#9BA8A2`) was substituted, lowering contrast on canvas from ~2.9:1 to ~2.3:1. Either add the
   tone to the token sheet or confirm the substitution.
8. **`login-rate-limited` contradicts the async-behaviour panel.** The artboard disables the
   identifier, password, toggle and submit; the behaviour panel requires every failure to "always
   leave an edit-and-retry path". With no `Retry-After` in the contract, obeying the artboard
   literally leaves the form permanently dead until a page reload. Implemented: the notice and the
   disabled submit are kept, the fields stay editable, and touching either field clears the notice.
   Update the artboard annotation or bless the deviation.
9. ~~**Persian pages render Latin runs in Vazirmatn's own Latin.**~~ **RESOLVED 2026-08-21 —
   stay faithful to the token.** Latin runs inside Persian pages keep Vazirmatn, matching the
   delivered `--font-ui-fa` stack, at a cost of ~49 KB per Persian page. Do not reorder the stack.

### Strength meter — decisions taken where the artboard is silent

Built 2026-08-21 from artboard 5. Points for the designer to confirm or correct:

1. **"Strong" is level 3 of 4, not the top.** The artboard fills three of four segments for
   `Strong`, so a fourth level must exist or the last segment could never fill. Implemented
   Weak / Fair / Strong / Very strong (level *k* fills *k* segments), which reproduces the drawn
   state exactly. If `Strong` was meant to be the top level, only the fourth label and one
   threshold change.
2. **The empty state is not drawn.** Rendered as four unfilled segments with the title and no
   level word, so the column does not move on the first keystroke ("same geometry in every
   state"). Confirm.
3. **No fill transition** was specified, so none was authored, even though motion tokens exist.
4. **Persian strength copy has no reference row** in the mockup's localization table; the four
   Persian labels (ضعیف / متوسط / قوی / بسیار قوی) were written to match the designer's habit of
   complete noun phrases.

The scoring rule is implementation, not design: length past the instance minimum plus
character-class variety, documented in `webapp/src/auth/passwordStrength.ts`. It is advisory
only — it never blocks submission and never claims a password is safe.
