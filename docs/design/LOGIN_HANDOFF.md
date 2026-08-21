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

1. **Banner space is not reserved.** The foundation sheet reserves a 46px collapsed block for
   the notice banner; `login-default` is drawn without that gap. The artboards were followed,
   so the form column shifts ~35px when an error appears.
2. **Strength meter not built.** `login-force-password-change` draws four segments plus the
   label "Strong", but no scoring scale and no other level labels are defined anywhere in the
   mockup. Inventing them would be design authorship, so the meter is absent pending an answer.
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
9. **Persian pages render Latin runs in Vazirmatn's own Latin**, not Inter, because the delivered
   `--font-ui-fa` stack lists Vazirmatn first and its Latin face claims those codepoints. Faithful
   to the token, but it costs ~49 KB on Persian pages and changes mixed-script typography. Confirm,
   or reorder the stack in the token sheet.
