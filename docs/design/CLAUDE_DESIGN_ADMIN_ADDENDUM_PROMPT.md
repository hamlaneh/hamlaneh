# Claude Design Prompt — Hamlaneh Admin Addendum

> Paste everything below the rule into Claude Design, together with these three attachments:
> `webapp/src/tokens.css`, `docs/design/ADMIN_HANDOFF.md`, `docs/design/BRIEFS.md`.
> Generated from BRIEFS.md §0 + §3 addendum on 2026-09-05.

---

You are a senior product designer and design-systems specialist working directly in Claude Design. Extend an existing, delivered design system with the admin surfaces it does not yet cover. Work in the design canvas with editable text, vectors, components, variants, and clearly named artboards. Do not stop at wireframes, a moodboard, a prose description, or a code-only response.

Make one resolved, coherent design direction. Do not present several competing concepts. Where a minor visual detail is unspecified, make a deliberate choice, keep it consistent, and annotate the rationale in the handoff sheet.

## 1. This is an extension, not a new system

The Hamlaneh design system already exists. It is called **Quiet Nest** and four screen sets were delivered on 2026-08-21: authentication, chat, admin dashboard, and user settings. The tokens are attached as `tokens.css`; the admin set's written contract is attached as `ADMIN_HANDOFF.md`.

Reuse the delivered vocabulary. Do not restart the system, do not introduce a second type scale, a second spacing rhythm, a second radius family, or a parallel component set. Every colour must resolve to an existing token unless a new one is unavoidable, and any new token must be declared in the handoff sheet with its reason.

The delivered admin geometry, which everything here sits inside:

- 260px navigation rail, 40px content gutter, 44px nav row.
- One `ADMINISTRATION` kicker, once, in the rail.
- `Back to chat` above the org identity, first in the tab order.
- A records-table treatment already drawn for users, invites and the audit log.

## 2. Scope

Design **only** the surfaces listed in §4. Every one of them is already built and shipping to real users as unstyled semantic HTML, except the org logo, which is not built at all and cannot be built until it is drawn.

Do not design: the chat shell, direct messages, calls, meetings, user settings, the sign-in form itself, onboarding, pricing, or marketing pages. Do not redraw the users table, the invites screen, the audit log or the create-user modal — they are delivered and in production.

Do not add product features. Every control below exists in shipped code; a control you invent is a control that does not exist and will be built wrong or not at all.

## 3. Product context and the honesty rule

Hamlaneh (`هم‌لانه`, Persian for "shared nest") is an open-source, self-hosted team communication platform. Organizations install it on their own servers. The admin dashboard is the control room: because public registration is off by default, every user account is born here.

The tone is calm, warm, grounded and quietly capable. Trustworthy through clarity and restraint — never through shields, padlocks, or security theatre.

**The honesty rule, which governs half of this brief.** Several of these screens make security claims, and the product's engineering principles forbid overclaiming: the words `unhackable`, `military-grade`, `100% secure`, `audited` and bare `encrypted` without saying which kind are all banned in shipped copy. More importantly, several of these screens have to state a limitation *without making a working, deliberate feature look broken*. Where a brief below says "this must not read as an error", that is a hard requirement, not a preference.

## 4. Artboards

Fourteen artboards plus one foundation sheet. Build them in this order.

### Group A — Org settings, redrawn (3 artboards)

The delivered `admin-org-settings` artboard draws: org name, default language, an account-creation panel (a radio group plus one warning note), and a security panel. Three things have to join it.

**A1. `admin-org-settings-v2`** — the whole screen, with all three additions in place, at 1440.

**A1a — the encryption mode block.** This is the one setting on the screen with a **ceremony**: everything else there saves the moment you leave the field, and this one has its own confirmation, because it decides what every conversation created afterwards is. It must contain:

- What the instance does *now*, as a sentence rather than a label — "every new conversation is end-to-end encrypted", not "Mode: strict".
- A **permanent count** of the conversations the current mode does not describe, shown **at zero as well as above it**. A figure that appears only when non-zero would teach an administrator that silence means the mode covers everything, which is the one thing it never means. Design the zero state as a first-class case, not as an afterthought.
- Two choices, **Strict** and **Compliance**, each with a line saying what it does.
- **Compliance is shown and unavailable.** Not hidden — hiding it teaches nobody what the product will offer — and not selectable, because the server-side half it promises does not exist yet and a mode delivering nothing but the absence of encryption would be a dishonest toggle. Its reason sits beside it and **must not read as an error**. Nothing is broken; a feature is not finished.

Decide, and annotate: does a setting with a ceremony belong on the same screen as a dozen that save on blur at all? A panel of its own, a divider, or a separate page are all legitimate answers.

**A1b — the just-in-time provisioning switch.** A toggle that belongs *inside* the account-creation panel, which the delivered artboard draws as a radio group and one note and nothing else. It answers the same question the registration mode answers — how an account comes into existence here — but it is not part of that choice and must not grey out when the radio group is saving.

It carries **three hint lines under one label**: what it does, that it is *not* the registration setting, and — on an instance with no identity provider configured — that there is nothing for it to govern. That is one more hint line than any delivered control carries. Solve the alignment: the radio group's controls are indented by their radio and a switch is not.

**A1c — the org logo control.** Current logo, or its absence; a file picker; remove. Everything else on this screen saves on blur and an upload does not, so it needs a between-state of its own.

**A2. `admin-encryption-switch-to-compliance`** — the confirmation dialog, the harder direction.

It carries **four load-bearing paragraphs**, none of which can be cut to fit a one-line confirm:

1. Nothing already stored changes. No conversation is converted, in either direction.
2. The new mode begins with what is created after it.
3. A complete export of the past becomes **impossible** — the server holds no key for what is already encrypted. **This has to read as the guarantee, not as a failure.** It is the product working exactly as designed.
4. How many conversations will sit outside the mode being *chosen* (not the one in force).

Decide, and annotate: can a confirm dialog carry four paragraphs, or is this a full screen, a two-step flow, or a typed confirmation?

**A3. `admin-encryption-switch-to-strict`** — the same dialog, the other direction, where paragraph 3 does not appear. Show whether the two directions share one treatment.

### Group B — Provisioning tokens (3 artboards)

A **fifth navigation row**, appended after `Audit log` so the delivered four keep their positions. It currently borrows the shield glyph from another screen's rail; **draw it a glyph of its own.**

These are the only credential in the product that belongs to a **machine** rather than a person: an identity provider's sync engine authenticates with one.

**B1. `admin-scim-empty`** — no tokens yet. This is a fresh install and the common case, not an edge case.

**B2. `admin-scim`** — populated. A sentence saying what these are for; a way to mint one, taking an optional note naming what it is for; a list with note, created, and last used; and revoke, with the confirm treatment invite revocation already uses.

The design problem to solve here: **`Last used` is empty on every token nobody has configured yet.** That is unremarkable an hour after minting and a real signal a month later, and the screen cannot tell those apart. Draw an answer.

Decide, and annotate: is a records table right at all when the most useful column is empty on every row? And does minting live in the header action slot the users and invites screens use — making it a dialog, like `Create invite link` — or stay an inline form on the page?

**B3. `admin-scim-token-created`** — the show-once panel. The minted value is displayed exactly once because the server keeps only a hash of it. The delivered create-user credentials panel is the same act and is the obvious source; show whether it is borrowed or restated.

### Group C — The org logo on the two screens that display it (3 artboards)

The delivered auth set draws the product name as live text with **no slot for a symbol**, even though the original brief asked for one. That request is being made again, with its constraint made explicit.

**The absent case is the common case.** Every install has no logo on day one and most will never set one. A layout that looks unfinished without a logo is the wrong layout.

**C1. `login-org-logo`** — the sign-in screen with an organization's logo present.
**C2. `login-org-logo-absent`** — the identical screen with none. These two must not look like different products.
**C3. `chat-sidebar-org-logo`** — the chat sidebar with a logo, in both present and absent states. Note that the org **name** is already the first thing in that sidebar, so a logo beside it is either identity or duplication. Pick one and say why.

Three product facts the design cannot change:

- The sign-in page is **unauthenticated**. The logo is a public asset fetched before any session exists, and anyone who can reach the sign-in page can fetch it. It cannot be treated as private content.
- Uploads happen on a different origin from the screens that display it, so the reference is absolute — this constrains nothing visually but rules out any design that assumes a same-page preview is the same asset.
- **One asset, unless you say otherwise.** A dark-ink logo on the dark canvas is invisible. Requiring a second file for dark theme is a legitimate answer, but it is a product decision and must be stated in the handoff, not assumed.

Also draw: chosen-but-not-yet-uploaded, uploading, and refused (wrong type, too large, wrong aspect ratio). A wide lockup and a square mark cannot both sit in one slot — the honest answers are a fixed aspect box, a cropper, or a refusal, and the artboard has to pick.

### Group D — The whole admin set at 375 (2 artboards)

The admin set is drawn only at 1440, with a 260px rail and a 40px gutter, which leaves roughly 75px of table on a phone. What ships today is a stopgap, not a design: below 899px the rail becomes a horizontal strip above the content, the gutter drops to 16px, and the document scrolls as one.

**D1. `admin-users-375`** — the records table at phone width.
**D2. `admin-org-settings-375`** — the settings screen at phone width, including the encryption block from A1a.

Three questions to settle explicitly:

- Is a records table a table at all on a phone, or a stack of rows?
- Where do the org identity, the `Back to chat` exit and the signed-in-as footer go, when three stacked blocks of chrome would otherwise sit above the first row?
- Does the row menu — 260px wide and anchored to the row's end edge — become a sheet?

### Group E — Foundation sheet (1 artboard)

**E1. `admin-addendum-foundations`** — every new component, state and token introduced above, labelled, with its light and dark variants: the encryption mode block, the disabled-but-visible radio, the four-paragraph dialog, the switch inside a fieldset, the three-line hint stack, the logo slot in all its states, the empty-column table cell, the new nav glyph, and the 375 rail strip.

## 5. Hard requirements for every artboard

1. **Bilingual and RTL.** Every screen ships in English (LTR) and Persian (RTL), and the Persian version is a true mirror — navigation, directional icons, alignment — not translated text in a Latin layout. Design in English, and deliver at least `admin-org-settings-v2` as a Persian/RTL artboard so mirroring intent is unambiguous.
2. **Numerals are ASCII `0-9` in both locales.** This is a locked project decision. The conversation counts, token dates and table figures all follow it.
3. **Light and dark theme.** Both. At minimum deliver `admin-org-settings-v2` and `admin-scim` in dark.
4. **Fonts must be bundleable** — this is a self-hosted product with no runtime CDN. The delivered pairing is Inter (Latin) + Vazirmatn (Persian); stay on it.
5. **Accessibility.** WCAG AA contrast; a visible focus state on every interactive element; never colour as the only signal; hit targets ≥40px. Two specifics that bite here: a disabled radio that still has to be readable, and a status figure that must not rely on a tint alone.
6. **States are part of the design.** Loading, empty with guidance rather than a blank void, and error. Destructive actions get a confirmation affordance.
7. **No text baked into images** — every string is translated.
8. **Primary artboard 1440×900**, plus the two 375 artboards in Group D.

## 6. Deliverable

A Claude Design canvas with the fifteen artboards above, clearly named, arranged in the group order given.

Plus a written handoff sheet covering: every new token with its reason; the answer to each "decide, and annotate" question in §4; which delivered components were reused unchanged, which were restated, and why; and anything you deliberately did not draw.

If the tool reaches a per-generation limit, continue in the same canvas rather than compressing, combining, or omitting required states.
