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
  conversation visible and in context. Below 1024 it becomes an overlay instead.
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
- Bidi: channel slugs, filenames, version tags and URLs are isolated LTR runs inside Persian.
  Message bodies are `dir="auto"`. Only send, reply and back mirror — attach, search,
  download and the hash do not.
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

Persian digit shaping remains unresolved (open question 3 below). The addendum contains no
human-facing numbers and must not be read as choosing Latin or Persian digits.

## Not in scope

Threads, reactions, pinned messages, voice/video calls, notification preferences,
channel-member management. None are in §2.

## STATUS.md row

```
| Chat shell (9 artboards) | BRIEFS.md §2 | DESIGNED | Hamlaneh Chat.dc.html |
```

## Open questions back to the designer (implementation deviated only where forced)

Recorded 2026-08-21 during implementation. Nothing below was invented to fill a gap.

1. **Mentions have no drawn treatment.** `@Ava` appears in message bodies but the artboards never
   specify how a mention should look. Rendered with the set's existing emphasis pair (brand tone,
   weight 500) as the least-invented option available.
2. **Code is set in IBM Plex Mono on the artboards**, which is not in the delivered token sheet and
   would be a new self-hosted font. The platform monospace stack is used instead. Either add the
   font (and a token) or bless the substitution.
3. **Persian digit shaping is inconsistent on the artboard**: times, member counts and badges use
   Latin digits, one file size uses Persian digits. Implemented exactly as drawn — times and counts
   pinned to Latin in `fa`, file sizes following the locale — which is almost certainly not intended
   as a rule. Needs one decision applied consistently.
4. **Undrawn but required by the flow**, built as unstyled plumbing (STATUS.md: `awaiting-design`):
   create-channel dialog, people picker (serves both *Invite people* and *New direct message*),
   mention picker, channel menu, account menu.
5. **Smaller silences**, each resolved by reusing something already drawn: initial history load
   reuses the scrollback skeleton; the delete confirmation is centred over the conversation (its
   placement is undrawn); day separators older than "Yesterday" use the locale long date; an
   unresolvable mention renders as a localized "@unknown"; a permalinked message scrolls into view
   without a highlight (none is drawn); "no conversations at all" gets one plain sentence plus the
   drawn create-channel control.
6. **Breakpoints**: the component sheet names 1280 / 1024 / <900 / 375 but only 1440 and 375 are
   drawn. Read as two tiers — search becomes an overlay at ≤1279, the full mobile set applies at
   ≤899. Confirm.

## Deliberate deviations from the drawn chrome, for honesty

- The **attach control** is drawn enabled, but uploads arrive in Phase 1.3. It renders disabled
  with its reason, following the component sheet's own rule that "the reason always travels with
  the disabled state".
- The **admin-dashboard shield** in the user footer is not rendered at all: the admin surface does
  not exist yet, and a control that goes nowhere is worse than an absent one. Restore it with the
  admin slice.
