# Claude Design Prompt — Hamlaneh Authentication

You are a senior product designer and design-systems specialist working directly in Claude Design. Create a polished, high-fidelity, implementation-ready authentication experience for Hamlaneh. Work in the design canvas with editable text, vectors, components, variants, and clearly named artboards. Do not stop at wireframes, a moodboard, a prose description, or a code-only response.

Make one resolved, coherent design direction. Do not present several competing concepts. Where a minor visual detail is unspecified, make a deliberate choice, keep it consistent, and annotate the rationale in the handoff sheet. Do not add product features that are outside this prompt.

## 1. Scope

Design only the Login and password-reset surface described below. This is the current delivery priority.

Do not design Chat, Direct Messages, Admin, User Settings, Calls, meetings, onboarding, pricing, or marketing pages. Do not add public registration, a `Sign up` link, social login, SSO, `Remember me`, testimonials, feature lists, security badges, CAPTCHA, passkeys, or any other unrequested flow.

The canvas must contain one foundation/component sheet and these ten screen artboards. Nine are directly required by the product brief; `reset-request-confirmation` is a supplemental artboard that makes the brief's explicit confirmation state reviewable on its own:

1. `login-default`
2. `login-error`
3. `login-rate-limited`
4. `login-totp`
5. `login-force-password-change`
6. `reset-request`
7. `reset-request-confirmation`
8. `reset-new-password`
9. `login-default-dark`
10. `login-rtl-fa`

`reset-request-confirmation` is intentionally a separate artboard because it is a distinct security-sensitive state, even though it belongs to the reset-request flow. Build the nine brief-required artboards first, then the supplemental confirmation artboard and the full foundation sheet. If the design tool reaches a per-generation limit, continue in the same canvas rather than compressing, combining, or omitting required states.

Also create `auth-foundations-and-components` as a clearly labelled design-system and state sheet. Arrange the canvas in the order above so the flow is easy to review.

## 2. Product and brand context

Hamlaneh (`هم‌لانه`, Persian for “shared nest”) is an open-source, self-hosted team communication platform for chat and calls. Organizations install it on their own servers. The closest functional neighbours are Slack, Mattermost, and Rocket.Chat, but the visual identity must not feel like a Slack clone or a generic venture-backed corporate SaaS.

The product should feel:

- calm, warm, grounded, and quietly capable;
- trustworthy through clarity and restraint, not through shields, locks, or security theatre;
- owned by the organization using it, rather than rented from a distant platform;
- welcoming enough to express “shared nest” without becoming cute, rustic, childish, or literal.

This is the front door of a private company instance. Users receive their account from an administrator; public self-registration is off. The experience should reduce anxiety, make the next action obvious, and remain professional for daily use.

Do not use unverified marketing claims such as “unhackable”, “military-grade”, “100% secure”, “audited”, “encrypted”, or “compliance-ready”. If a supporting line is useful, use the existing neutral product descriptor `Self-hosted team communication`. Do not turn the login screen into a marketing landing page.

There is no approved product logo, wordmark artwork, illustration library, or brand palette in the repository yet. Treat the wordmarks `Hamlaneh` and `هم‌لانه` as live text and do not invent a permanent product logo. An abstract nested-curves motif may exist only as separate, removable decorative background art—not as a claimed brand mark. Avoid literal birds, eggs, branches, houses, handshake imagery, shields, padlocks, and stock illustrations of people.

## 3. Visual direction — “Quiet Nest”

Build a warm-minimal application UI with a single clear action, generous but controlled whitespace, and a tactile sense of calm. The design should have character at close range—excellent type, optical alignment, thoughtful borders, and nuanced surfaces—without decorative noise.

### Desktop composition

Use a stable two-zone authentication shell at `1440 × 900`:

- A restrained identity field on the logical start side, approximately 38–42% of the width.
- The authentication region on the logical end side, approximately 58–62% of the width.
- In English/LTR, the identity field is on the left and the form is on the right.
- In Persian/RTL, mirror the shell: identity field on the right and form on the left.
- Keep the form in a single column with a maximum content width around `420–440 px` and optically centre it slightly above the mathematical vertical centre.
- Preserve the same shell geometry and form position across all states so switching states does not feel like a different product.

The identity field may use a very subtle, low-contrast vector motif made from two or three nested curved strokes. It should suggest shelter, connection, and shared space abstractly, not depict a literal nest. Keep the motif quiet enough that it never competes with the form. Use flat or near-flat tonal surfaces; at most, use a barely perceptible monochromatic wash. Do not use loud gradients, glassmorphism, glowing blobs, 3D objects, bento cards, grain overlays, or oversized marketing typography.

The authentication region may use a lightly separated surface or card, but avoid a generic floating white card with a dramatic shadow. Prefer one subtle border/elevation level, precise spacing, and a composed relationship to the identity field. Use rounded corners with discipline: approximately `10–12 px` for controls and `20–24 px` for a large containing surface; do not make every element pill-shaped.

### Optional organization identity

Reserve a compact, clearly intentional slot for an organization logo above or near the product wordmark. Show it with a tasteful fictional organization mark in one annotated example, but keep the product wordmark visible. When no organization logo exists, the slot must collapse completely without leaving an empty box, placeholder, or awkward vertical gap. The layout must also tolerate a wide or square organization logo without changing the form width.

### Responsive behaviour

The source of truth is the `1440 × 900` desktop artboard, but annotate responsive behaviour for `1024`, `768`, and `375 px` widths:

- At narrow widths, collapse the large identity field into a compact brand header rather than hiding all brand context.
- At `375 px`, use approximately `24 px` inline gutters, a full-width form, and `min-height: 100dvh` behaviour with safe top and bottom padding.
- Keep the language switcher visible before authentication.
- Avoid horizontal scrolling, clipped focus rings, and content hidden by mobile keyboards or Tauri window chrome.
- If vertical space becomes tight, allow the page to scroll naturally; never compress controls below their accessible size.

## 4. Design tokens

Create semantic tokens and show both theme mappings on `auth-foundations-and-components`. These values are a disciplined starting palette; small luminance adjustments are allowed only when needed for verified contrast. Keep the roles and overall character intact.

### Light theme

- `color.canvas`: `#F7F6F2` — warm neutral page background
- `color.surface`: `#FFFFFF`
- `color.surface.subtle`: `#EEF2EF`
- `color.text.primary`: `#1C2724`
- `color.text.secondary`: `#5C6965`
- `color.border.subtle`: `#D7DEDA` — decorative separators only
- `color.border.control`: `#71807A` — meaningful control boundaries; at least 3:1 on the control surface
- `color.brand.primary`: `#235C55` — deep evergreen/teal
- `color.brand.hover`: `#194941`
- `color.brand.pressed`: `#123F39`
- `color.on-brand`: `#FFFFFF`
- `color.brand.soft`: `#DDECE7`
- `color.accent.warm`: `#A65332` — restrained terracotta; never a competing primary CTA
- `color.danger`: `#B42318`
- `color.danger.soft`: `#FEF0ED`
- `color.warning`: `#8A5B12`
- `color.warning.soft`: `#FFF4D6`
- `color.success`: `#237A57`
- `color.success.soft`: `#E4F4EC`
- `color.info`: `#27657A`
- `color.info.soft`: `#E7F3F6`
- `color.focus`: `#2F7D72`

### Dark theme

- `color.canvas`: `#111615` — near-black with a green-neutral undertone, not pure black
- `color.surface`: `#18201E`
- `color.surface.subtle`: `#1D2924`
- `color.surface.elevated`: `#202A27`
- `color.text.primary`: `#F2F5F4`
- `color.text.secondary`: `#ABB7B3`
- `color.border.subtle`: `#34413D` — decorative separators only
- `color.border.control`: `#6C7C76` — meaningful control boundaries; at least 3:1 on the control surface
- `color.brand.primary`: `#81C9BD`
- `color.brand.hover`: `#9ADACF`
- `color.brand.pressed`: `#6FB5AA`
- `color.on-brand`: `#0B2722`
- `color.brand.soft`: `#193A34`
- `color.accent.warm`: `#E3A17E`
- `color.danger`: `#FFB4AB`
- `color.danger.soft`: `#3B1D1B`
- `color.warning`: `#EBCB7A`
- `color.warning.soft`: `#332A13`
- `color.success`: `#75D0A4`
- `color.success.soft`: `#173329`
- `color.info`: `#8BCBDB`
- `color.info.soft`: `#173039`
- `color.focus`: `#9ADACF`

Use semantic roles rather than raw colour names in component annotations. Verify each theme independently. Dark mode must not be a simple inversion, must not use neon accents, and must preserve visible borders, disabled states, focus, errors, and elevation.

Use a `4 px` base grid with this spacing scale: `4, 8, 12, 16, 24, 32, 48, 64, 96`. Define named radii, border, elevation, icon-size, and motion tokens. Keep elevation restrained: base, raised, and overlay are enough.

## 5. Typography and iconography

Use open-license, bundleable fonts only. Use no runtime CDN.

- Latin UI: `Inter`
- Persian UI: `Vazirmatn`
- Fallback: a sensible system sans-serif stack
- Suggested weights: `400`, `500`, `600`, and sparing `700`
- Suggested UI scale: `12, 14, 16, 20, 28, 36 px`

Use live text everywhere, including the wordmark; never bake copy into an image. Tune Persian independently rather than inheriting Latin metrics blindly. Persian body text needs comfortable glyph density and line-height, approximately `1.65–1.75`; Latin body text may use approximately `1.45–1.6`. Check baseline alignment when Persian and Latin appear together in the language switcher.

Use one coherent open-license outline icon family with roughly `1.75 px` stroke and `18–20 px` visual icons inside at least `44 × 44 px` interactive areas. Lucide is a suitable default. In the handoff, identify the exact icon family, version, and license; if the family will not be installed as a dependency, include the final editable/exportable SVG paths. Do not use icon fonts. No emoji and no flag icons for language. Mirror only direction-bearing icons in RTL, such as back arrows; do not mirror eye, alert, check, or language icons.

## 6. Foundation and component sheet

On `auth-foundations-and-components`, show:

- Light and dark semantic colour tokens with foreground/background pairings.
- Type styles for English and Persian with real sample strings.
- Spacing, radius, border, elevation, icon, focus-ring, and motion tokens.
- The authentication shell, optional organization-logo slot, wordmark, and language switcher.
- Text field variants: empty, hover, focus, filled, autofill, error, disabled, and read-only if shown.
- Password field variants: concealed, revealed, focused, error, and disabled, with an accessible show/hide control.
- Primary button variants: default, hover, pressed, keyboard focus, disabled, and loading. The loading label must not change the button width.
- Link variants: default, hover, focus, and disabled where applicable.
- Notice banners: error, warning/rate-limit, success/confirmation, and neutral information. Each uses an icon plus text, never colour alone.
- Six-digit code input variants: empty, focused, partially filled, complete, error, disabled, and paste behaviour.
- Password-requirement item variants: unmet, met, and policy-driven value.
- A composed mini-frame for each async form—not only isolated buttons—showing loading and a recoverable error: sign-in, TOTP verification, forced password change, reset request, and new-password reset. Use generic, non-enumerating error copy and a clear retry/edit path.

Use short, meaningful motion only. Interaction feedback should appear within about `100 ms`; ordinary state transitions should be `150–220 ms`, and no transition should exceed `300 ms`. Animate opacity and transform rather than layout dimensions. Do not shake the form on errors. Respect reduced-motion preferences and never block input while an animation plays.

Annotate these behaviours on the same sheet:

- Pressing Enter submits the active form.
- Validate fields on blur and again on submission; do not show aggressive errors on every keystroke.
- During submission, prevent duplicate requests and use stable-width loading labels: `Signing in…`, `Verifying…`, `Setting password…`, `Sending…`, and `Resetting…`.
- After an invalid submission, move focus to the first invalid field or the alert summary as appropriate.
- Preserve the identifier after failed sign-in; never expose credential details.
- Password visibility controls retain focus and update their accessible name between Show and Hide.
- Language switching applies immediately, updates language and direction, mirrors the shell, and preserves typed values where possible.
- Annotate intended autocomplete semantics: identifier `username`, login password `current-password`, new passwords `new-password`, reset email `email`, and TOTP `one-time-code`.

## 7. Screen specifications and exact copy

All English screen artboards are `1440 × 900`, LTR, and light theme unless the name says otherwise. Use real text, not lorem ipsum. The product and organization identity, language switcher, form width, and main spatial anchors must stay consistent.

### `login-default`

Include:

- Product wordmark: `Hamlaneh`
- Optional supporting line: `Self-hosted team communication`
- Heading: `Sign in`
- Calm helper copy: `Use the account provided by your administrator.`
- One identifier field only, labelled exactly: `Username or email`
- Password field labelled: `Password`
- Accessible show/hide password control
- Primary button: `Sign in`
- Link: `Forgot password?`
- Visible language switcher: `EN / فا`, with English selected
- Optional organization-logo slot as specified above

Do not add a separate email field, separate username field, `Sign up`, `Remember me`, social providers, or promotional content. Visible labels must remain present when fields are filled; placeholders are optional examples, never label replacements.

### `login-error`

Use the same shell and preserve submitted context. Add a form-level inline error banner with the exact text:

`Incorrect username or password.`

The message is deliberately generic and must never reveal whether the username/email or password was wrong. Do not mark only one field as the cause. Pair the danger colour with an alert icon and readable text, reserve space to minimize layout jump, and annotate that the banner is announced as an alert. The user must have a clear path to edit and submit again.

### `login-rate-limited`

Use the same shell. Add a non-dismissable notice with the exact text:

`Too many attempts. Try again in a few minutes.`

Disable the identifier field, password field, password toggle, and submit button. Keep their contents legible enough to understand the state, while clearly non-interactive. Do not show a close icon. Keep the language switcher visible. The state cannot rely on colour alone.

### `login-totp`

This is the second step after a correct password. Include:

- Heading: `Two-step verification`
- Instruction text exactly: `Enter the code from your authenticator app`
- A labelled six-digit one-time-code input
- Primary button: `Verify`
- Secondary text action: `Back`
- The language switcher and stable instance identity

Use either one visually segmented semantic input or six tightly coordinated cells, but design it to support full-code paste, automatic advance, intuitive backspace, keyboard navigation, and mobile one-time-code autofill. Show error and loading variants on the component sheet; do not add a resend action because authenticator codes are generated in the app. One-time codes must remain logically left-to-right even inside an RTL interface.

### `login-force-password-change`

This state appears after an administrator-created account signs in with a temporary password. Include:

- Heading: `Set a new password`
- Helper copy: `Your temporary password must be replaced before you continue.`
- Field: `New password`
- Field: `Confirm new password`
- Show/hide controls
- A clear inline requirements checklist and restrained strength feedback
- Primary button exactly: `Set password and continue`

Password rules are organization policy. For the mockup, `At least 12 characters` may be used as representative instance data, but annotate the number as policy-driven, not a universal hard-coded product rule. Show mismatch, loading, and completed-requirement variants on the component sheet. Do not use colour as the only strength or requirement signal.

### `reset-request`

Include:

- Heading: `Reset your password`
- Helper copy: `Enter the email address associated with your account.`
- Field: `Email address`
- Primary button exactly: `Send reset link`
- Link: `Back to sign in`
- Visible language switcher and stable instance identity

Design the button loading state so repeated submission is prevented and feedback is immediate.

### `reset-request-confirmation`

This state must use the exact enumeration-safe confirmation copy regardless of whether the account exists:

`If that address exists, a reset link is on its way.`

Do not confirm the account, display a user identity, or change the message based on existence. Present it as a calm confirmation—not a celebratory marketing success screen—and provide `Back to sign in`. Keep the surrounding shell and identity stable.

### `reset-new-password`

This is reached from the email link. Include:

- Heading: `Create a new password`
- Field: `New password`
- Field: `Confirm new password`
- Show/hide controls
- The same policy-driven requirement treatment used in the forced-change flow
- Primary button exactly: `Reset password`
- A clear route back to sign-in after success

On the component/state sheet, also show how an invalid or expired link is handled with a concise recovery action such as `Request a new link`; do not leave a broken or blank form.

### `login-default-dark`

Create a faithful dark-theme counterpart of `login-default`, not a redesign. Preserve geometry, hierarchy, content, spacing, and brand character. Use the dark semantic tokens above and independently verify text, border, icon, input, focus, hover, disabled, and button contrast. Avoid pure black, neon green, excessive glow, or transparent glass surfaces.

### `login-rtl-fa`

Create a full Persian/RTL mirror of `login-default`, not a merely translated LTR composition. Use these exact live-text strings:

- Product wordmark: `هم‌لانه`
- Optional supporting line: `ارتباط تیمی خودمیزبان`
- Heading: `ورود`
- Helper copy: `با حسابی که مدیر سامانه برای شما ساخته است وارد شوید.`
- Identifier label: `نام کاربری یا ایمیل`
- Password label: `گذرواژه`
- Show-password accessible label: `نمایش گذرواژه`
- Hide-password accessible label: `پنهان کردن گذرواژه`
- Primary button: `ورود`
- Forgot-password link: `گذرواژه را فراموش کرده‌اید؟`
- Language switcher: `EN / فا`, with Persian selected

Mirror the identity field, form region, alignment, padding logic, and direction-bearing icons. Use Vazirmatn and check that the Persian lines breathe naturally. Keep email addresses, URLs, and one-time codes isolated in LTR direction within the RTL page; use `dir=auto` for a username-or-email value where appropriate. Do not reverse Latin email characters, digits, or punctuation. The focus order and reading order must match the RTL visual order.

### Persian localization reference for the other authentication states

Include this live-text reference on `auth-foundations-and-components` so later RTL state artboards can be produced without guessing. Preserve Persian half-spaces and punctuation.

- `Incorrect username or password.` → `نام کاربری یا گذرواژه نادرست است.`
- `Too many attempts. Try again in a few minutes.` → `تلاش‌های زیادی انجام شده است. چند دقیقهٔ دیگر دوباره امتحان کنید.`
- `Two-step verification` → `تأیید دومرحله‌ای`
- `Enter the code from your authenticator app` → `کد برنامهٔ احراز هویت را وارد کنید.`
- `One-time code` → `کد یک‌بارمصرف`
- `Verify` → `تأیید`
- `Back` → `بازگشت`
- `Set a new password` → `تعیین گذرواژهٔ جدید`
- `Your temporary password must be replaced before you continue.` → `گذرواژهٔ موقت شما باید پیش از ادامه تغییر کند.`
- `New password` → `گذرواژهٔ جدید`
- `Confirm new password` → `تکرار گذرواژهٔ جدید`
- `Password requirements` → `شرایط گذرواژه`
- `At least {minimum} characters` → `دست‌کم {minimum} نویسه`
- `Set password and continue` → `تعیین گذرواژه و ادامه`
- `Reset your password` → `بازنشانی گذرواژه`
- `Email address` → `نشانی ایمیل`
- `Send reset link` → `ارسال پیوند بازنشانی`
- `Back to sign in` → `بازگشت به صفحهٔ ورود`
- `If that address exists, a reset link is on its way.` → `اگر این نشانی وجود داشته باشد، پیوند بازنشانی برای آن ارسال می‌شود.`
- `Create a new password` → `ساخت گذرواژهٔ جدید`
- `Reset password` → `بازنشانی گذرواژه`
- `Request a new link` → `درخواست پیوند جدید`

## 8. Accessibility and interaction acceptance criteria

The final design must explicitly satisfy all of the following:

- WCAG AA: at least `4.5:1` contrast for normal text, `3:1` for large text and meaningful interactive control boundaries/icons. Decorative separators may use lower contrast only when they communicate no state or boundary needed to identify a control.
- Every interactive control has a visible keyboard focus state, ideally a `3 px` ring with adequate offset and contrast.
- Keyboard tab order follows the visual reading order in both LTR and RTL.
- Visible labels are associated with inputs; placeholders are never the only labels.
- Icon-only controls have accessible names and tooltips where useful.
- Interactive targets are at least `44 × 44 px`, exceeding the product minimum of `40 px`.
- Errors, warnings, success, selected, and disabled states never rely on colour alone.
- Form-level errors and status messages have appropriate alert/live-region annotations without stealing focus unnecessarily.
- On submit, disable duplicate submission and show an inline loading indicator and stable label.
- The layout survives browser zoom and text scaling to `200%` without clipped controls or overlapping copy.
- Motion respects reduced-motion preferences.
- All copy remains editable/localizable; no text is baked into imagery.
- Language switching remains available before sign-in and does not use country flags.
- No destructive action exists in this scope. Do not invent one.

## 9. Implementation handoff requirements

The implementation target is React + TypeScript + Vite + Tailwind CSS 4, later wrapped by Tauri. There is currently no approved component library or icon dependency. The design must be implementable without proprietary UI kits, remote font calls, or runtime CDNs.

Use reusable components and variants rather than detached one-off copies. Suggested component names:

- `AuthShell`
- `InstanceIdentity`
- `OrganizationLogoSlot`
- `ProductWordmark`
- `LanguageSwitcher`
- `AuthForm`
- `TextField`
- `PasswordField`
- `PrimaryButton`
- `NoticeBanner`
- `OtpInput`
- `PasswordRequirements`
- `BackLink`

Provide annotations for:

- exact artboard dimensions and core form width;
- logical start/end spacing rather than hard-coded left/right assumptions;
- responsive behaviour at `1024`, `768`, and `375 px`;
- component variants and their state names;
- semantic colour/token names suitable for CSS variables and Tailwind theme tokens;
- typography metrics for both Inter and Vazirmatn;
- the exact font release/source, license, variable-versus-static files, WOFF2 asset plan, and weight mapping for Inter and Vazirmatn so both can be self-hosted without a CDN;
- the exact icon family/version/license or final exportable SVG paths;
- focus, hover, pressed, loading, disabled, error, and autofill behaviour;
- which directional icons mirror in RTL;
- organization-logo present/absent behaviour.

Do not require generated raster artwork for the core layout. Keep decorative art as editable vectors, mark it decorative, and ensure the page still looks complete if that art is removed.

## 10. Final self-review before delivery

Before considering the canvas complete, inspect every artboard at `100%` and answer these checks through the design itself:

- Are all ten screen artboards present and named exactly?
- Is `auth-foundations-and-components` complete enough for faithful implementation?
- Is there exactly one username-or-email field and no registration path?
- Are the credential error and reset confirmation enumeration-safe and verbatim?
- Are rate-limited controls disabled and the notice non-dismissable?
- Is TOTP exactly six digits with clear paste/focus behaviour?
- Does the organization-logo slot disappear cleanly when empty?
- Is the dark version a true token-mapped counterpart?
- Is the Persian artboard a genuine spatial mirror with correct real Persian copy and bidi handling?
- Are all targets, focus states, contrasts, loading states, and error states clear?
- Does the result feel calm, warm, self-hosted, and distinct from a generic SaaS login?
- Can a React/Tailwind implementer reproduce it without guessing about spacing, tokens, type, or states?

Deliver the finished high-fidelity canvas and a concise handoff note. Do not replace the requested artboards with a textual design proposal.
