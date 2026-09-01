# Chat shell — design handoff

Mockup: `Hamlaneh Chat.dc.html` (Claude Design canvas).
Tokens: reuses `webapp/src/tokens.css` unchanged — this set adds **no** new colour, type,
spacing, radius or motion tokens.
Brief: `docs/design/BRIEFS.md` §2.

## What is on the canvas

A working `live-shell` (switch channels, send a message, open search, toggle theme and
language), then nine artboards, then `chat-components`.

| Artboard | Notes |
|---|---|
| `chat-default` | 1440×900, LTR, light. Busy `#deploys`: grouped runs, unread divider, file card, link preview, edited and removed markers. |
| `chat-default-dark` | Token-mapped counterpart, identical geometry. |
| `chat-empty-channel` | Freshly created channel. Guidance copy, `Invite people` as the primary action. |
| `chat-loading-history` | Scrollback fetch in flight; skeleton bubbles above, today already readable. |
| `chat-reconnecting` | Overlay banner, dimmed history, queued own message, composer disabled with its reason. |
| `chat-dm` | Direct message with an image attachment card; presence in the header. |
| `chat-search-results` | Search as a third 340px column — the conversation stays in place. |
| `chat-rtl-fa` | Full Persian mirror. Bubbles, sidebar and cards all swap sides. |
| `chat-mobile` | 375×812, drawer closed and open, side by side. |
| `chat-components` | Geometry, bubble anatomy, row/presence/badge states, cards, composer states, connection banners, behaviour, bidi, responsive, component tree. |

## Geometry

Sidebar 280 · search panel 340 (conditional) · header 64 · user footer 60 ·
bubble max-width 520 · message list padding 18/28 · desktop row 36 · mobile row and
drawer row 44 · mobile drawer 300 · mobile bubble max-width 264.

## The message treatment

Bubbles, not rows. Others' messages sit on the logical start side in `surface` with a
`border.subtle` outline; your own sit on the logical end side in `brand.soft` with no
outline. The first bubble of a run notches its leading top corner to 4px
(`border-start-start-radius` / `border-start-end-radius`); the rest stay at 12px.
Consecutive messages from one author within five minutes group under one avatar, name and
timestamp.

Side comes from `align-items` on a column flex, so the entire conversation mirrors from the
`dir` attribute alone — no RTL-specific rules. That is the main reason bubbles were chosen:
the RTL-critical screen of the product mirrors structurally rather than by re-aligning text.

## Decisions made where the brief left them open

- **Search** is a third column beside the channel (brief said designer's call). Keeps the
  conversation visible and in context. At `900–1279` it becomes an overlay instead.
- **Markdown hint row** is permanently visible under the composer rather than
  focus-revealed — it states Enter/Shift+Enter, which is worth saying always, not once.
- **Unread vs mention**: unread is bold text plus an outlined count; a mention is a filled
  brand badge carrying `@`. Fill, glyph and weight all differ — never colour alone.
- **Presence** is shape-differentiated: online solid, away barred, offline hollow ring. Every
  dot is paired with a text label somewhere in view.
- **Avatars** use four token fills (`brand.primary`, `accent.warm`, `info`, `success`)
  assigned by a stable hash of the user id, with `on-brand` initials as the no-photo fallback.
- **Deleted messages** leave a dashed placeholder so the conversation does not silently
  reshape. Delete is the only accent-red control in the set and always confirms.
- **Queued messages** (composer disconnected) get the dashed treatment plus a
  "Waiting to send" marker rather than being dropped or silently retried.

## Implementation notes

- Message list is a labelled `log` region; arrivals announce politely. Connection changes use
  `role="status"` and never steal focus from the composer.
- Hover actions (edit, copy link, delete) are keyboard reachable — focus inside a message
  reveals the same toolbar. Long-press on touch.
- The unread divider is placed on entry and holds position until you leave the channel.
- Editing replaces the bubble with an inline composer; the message keeps its place.
- Composer grows to 120px then scrolls. Enter sends, Shift+Enter newlines.
- Bidi: filenames, version tags and URLs are isolated LTR runs inside Persian, and the
  complete channel slug renders as one isolated unit — `<bdi dir="ltr">#deploys</bdi>` — so it
  never reads as `deploys#`. Message bodies, topics and display names are `dir="auto"`.
  Centred dialogs use `inset-inline: 0; margin-inline: auto` with a vertical-only transform,
  never `inset-inline-start: 50%` plus a physical X translation. Only `arrow-left` and
  `log-out` mirror — attach, search, download, hash, lock, close, settings and the chevrons
  do not.
- **Numerals are settled, not an open question**: Persian UI uses ASCII `0–9` for every
  app-generated number (times, dates, unread and member counts, badges, file sizes, counters,
  limits). User-authored messages, topics, filenames, usernames and technical strings stay
  exactly as authored.
- **Technical text uses the platform `ui-monospace` stack** (`ui-monospace, SFMono-Regular,
  Menlo, monospace`). No web-font asset and no new typography token is introduced for it.
- **Breakpoints are exactly** `≥1280`, `900–1279`, `≤899`. There is no 1024 rule.
- Persian sets at 1.7–1.75 line-height, no negative tracking.
- Desktop rows are 36px (pointer-only); every touch target is 44px.

## New icons (all Lucide, 24×24, 1.75 stroke — same family as the auth set)

hash · lock · users · search · paperclip · send · pencil · trash-2 · link · file-text ·
image · download · menu · shield · settings · ellipsis-vertical · wifi-off

## Tweaks on the Design Component

`density` (comfortable / compact), `showMarkdownHint`, `bubbleTails` — all affect the live
shell only, for trying the alternatives without re-cutting artboards.

## Addendum — create channel, people picker, mention picker

Appended to the same canvas as thirteen `chat-addendum-*` artboards. No new tokens, fonts,
icons or scrim; the two dialogs reuse the mobile drawer's overlay layering and the picker
reuses the sidebar's presence system unchanged.

| Artboard | Notes |
|---|---|
| `chat-addendum-create-channel-light` / `-dark` | Modal over `chat-default`. Public preselected. |
| `chat-addendum-people-invite-light` / `-dark` | Invite mode, 520 × 616. |
| `chat-addendum-people-new-dm-light` / `-dark` | Same component, `newDm` mode. |
| `chat-addendum-people-invite-rtl-fa` | RTL-critical. Mixed Persian and Latin display names. |
| `chat-addendum-mention-picker-light` / `-dark` | Non-modal popover, composer holds `@nas`. |
| `chat-addendum-create-channel-states` | 6 named states. |
| `chat-addendum-people-picker-states` | 9 named states. |
| `chat-addendum-mention-picker-states` | 9 named states. |
| `chat-addendum-overlay-components` | Geometry, variant matrix in both themes, layering, locale keys, RTL and 375 behaviour. |

**The `@`-query mention behaviour is a product requirement of this addendum, not a reskin.**
The picker opens both from an `@` typed at the current composer token and from the existing
`Mention someone` control — same component, same listbox, `aria-activedescendant`, focus never
leaving the composer. Enter inserts, Escape closes, Left/Right keep moving the caret, and Tab
closes without inserting. Filtering is client-side over the already-loaded member list; no new
server search endpoint is implied. The stored reference is `<@{user_id}>` and the UI never
renders the UUID or the raw token.

Other decisions worth knowing:

- **Create channel submits exactly a slug and a kind.** No topic, description, members or
  discoverability — absent, not hidden. Neither kind is browsable in this release, and no copy
  implies find, browse, join or read access.
- **The `#` prefix is decoration** and is not part of the submitted value. Slugs are isolated
  LTR runs even inside Persian UI.
- **The people picker is single-pick and immediate** in both modes. The whole row is one
  semantic button with the verb inside it — no nested control, no checkboxes, no chips, no
  footer confirm. Rows carry only initials avatar, display name, complete `@username` and
  optional presence.
- **No already-a-member state** is drawn; the current data contract cannot support one.
- **The mention list omits presence entirely** — a mention does not depend on it.
- Fourteen new strings need matching `en` and `fa` keys; they are listed on
  `chat-addendum-overlay-components` §04.

All app-generated numbers use ASCII `0–9`, in both languages.

## Addendum 2 — Channel actions and Account menu

Eleven further `chat-addendum-*` artboards on the same canvas. Both surfaces are **anchored
non-modal dialogs, not ARIA menus** — Channel actions holds a form and Account holds a radio
group, so `role="dialog"` + `aria-modal="false"`, triggers carrying `aria-haspopup="dialog"`,
and no scrim or desktop focus trap.

| Artboard | Notes |
|---|---|
| `chat-addendum-channel-menu-light` / `-dark` / `-rtl-fa` | Anchored under the header ellipsis. |
| `chat-addendum-account-menu-light` / `-dark` / `-rtl-fa` | Anchored above the identity trigger. |
| `chat-addendum-channel-menu-mobile` | 375, trigger restored beside search, Topic above the keyboard. |
| `chat-addendum-account-menu-mobile` | Drawer open, popover inside it, drawer's own scrim reused. |
| `chat-addendum-channel-menu-states` | 11 named states + dark set. |
| `chat-addendum-account-menu-states` | 11 named states + dark set. |
| `chat-addendum-menu-components` | Anatomy, anchors, collision, focus, keyboard, locked corrections, locale keys. |

### Locked corrections applied to the earlier canvas

- **Responsive tiers** are now `≥1280` / `900–1279` / `≤899`. The 1024 row on `chat-components`
  was corrected; there is no separate 1024 behaviour.
- **Numerals**: Persian UI uses ASCII `0–9` for every app-generated number. The previous
  "digit policy unresolved" annotation is removed from the canvas and the Persian invite
  backdrop now shows ASCII timestamps and file size.
- **Rendered mentions** are inline text in the deep brand step at weight 500 — no pill, chip,
  background or border. Documented on `chat-addendum-menu-components` §04.
- **The Channel actions trigger is 44×44 everywhere.** Propagated through the live shell and
  every base artboard, not only the addendum boards.
- **Channel actions renders only for an active public or private channel.** It is absent from
  the DOM in a DM and in the no-channel state — the `chat-dm` artboard no longer draws it, and
  no explanatory dead-end popover exists.

### Structural changes to the shell

- The sidebar **identity block becomes a 44px disclosure button** named `Open account menu`.
  The gear beside it stays a separate `Settings` button opening the delivered Settings panel
  directly, and Admin stays separate. Settings and Admin are never nested inside the identity
  button, and there is no redundant Settings row inside Account.
- **Channel actions renders only for an active channel.** In a DM and the no-channel state it is
  absent from the DOM — not disabled, and never a popover that exists to say nothing is here.

### Contracts worth not guessing

- **Topic**: max 250, empty deliberately clears, authored value never trimmed or normalised,
  Save disabled while the draft equals the current topic, returning to the original restores
  pristine. Counter is ASCII and `aria-live="off"`. Success announces `Topic saved.` politely;
  failure preserves the exact draft and allows retry. Closing mid-request does not cancel it.
- **Dirty drafts never vanish**: any dismissal path turns the same anchored surface into the
  discard confirmation. `Keep editing` returns to the editor; `Discard changes` completes the
  original destination. Hierarchy and copy, never accent red — red stays reserved for message
  deletion.
- **Log out** is neutral, unconfirmed and duplicate-safe, with no error state: the local session
  clears even if the server request fails.
- **Language** applies immediately, keeps the popover open and anchored, and leaves focus on the
  selected radio. Direction changes atomically.

Eleven new bilingual strings are listed on `chat-addendum-menu-components` §05.

## Not in scope

Threads, reactions, pinned messages, voice/video calls, notification preferences,
channel-member management. None are in §2.

## STATUS.md row

```
| Chat shell (9 artboards) | BRIEFS.md §2 | DESIGNED | Hamlaneh Chat.dc.html |
```

## Correction pass

A narrow correction pass was applied across the whole canvas. No tokens, artboard names,
colours, component scope or light/dark geometry changed.

- **RTL dialog centring.** Every centred dialog now uses `inset-inline: 0; margin-inline: auto`
  with a vertical-only transform. `inset-inline-start: 50%` combined with a physical X
  translation mis-shifts under `dir="rtl"` — the Persian people picker was cropped off the left
  edge and now sits centred at 460px on both sides.
- **Mention popovers anchor inside the chat pane** at `inset-inline-start: 308px` with
  `max-inline-size: calc(100% - 336px)`, clear of the 280px sidebar.
- **ASCII numerals everywhere.** Twelve Persian digits were converted; nothing app-generated
  renders in Eastern Arabic-Indic form.
- **Platform monospace.** All 490 `IBM Plex Mono` references became
  `ui-monospace, SFMono-Regular, Menlo, monospace`, and the Google Fonts entry was removed.
  No new font asset ships.
- **Shell contract propagated** to the live shell and all 19 base artboard footers: the avatar,
  display name, presence and chevron are one 44px `Open account menu` disclosure button with
  the avatar *inside* the target; the gear is a separate sibling opening Settings directly;
  Admin stays separate.
- **Slugs as single LTR units** — `<bdi dir="ltr">#deploys</bdi>`, fifteen occurrences.
- **Topic saving disables Invite people** and every action that could open a competing overlay.
- **Mobile Topic field is 16px computed** so iOS Safari does not auto-zoom with the keyboard up.
- **Accessibility rendered, not just described**: `aria-haspopup`, `aria-expanded` and
  `aria-controls` on both triggers; `aria-checked` on all 19 language radios; Persian `بستن`
  on RTL Close controls.
- **Mentions map to `brand.hover`** — `#194941` light, `#9ADACF` dark, shown as two specimens.
  The light value is never hardcoded into dark mode.
- **State-sheet specimens are labelled non-production illustrations** at both sheet heads and
  in `chat-addendum-menu-components` §06. Production geometry and the 44×44 floor live on the
  full frames.
- **Localization is complete**: 59 keys in the repository's existing `chat.*` namespace, both
  languages, including the previously omitted helper copy ("Lowercase letters, numbers,
  hyphens and underscores…", "Anyone invited can take part.", "Invitation only, same as
  public.", "Name or username").
- **Icon inventory** now names chevron-up, chevron-down, key-round and log-out with size,
  stroke and mirroring — only `log-out` mirrors in RTL.
- **Mobile dismissal recorded**: Account Close closes only the popover and leaves the drawer
  open; tapping the drawer scrim closes both.
