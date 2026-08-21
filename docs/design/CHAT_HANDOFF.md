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

## Not in scope

Threads, reactions, pinned messages, voice/video calls, notification preferences,
channel-member management. None are in §2.

## STATUS.md row

```
| Chat shell (9 artboards) | BRIEFS.md §2 | DESIGNED | Hamlaneh Chat.dc.html |
```
