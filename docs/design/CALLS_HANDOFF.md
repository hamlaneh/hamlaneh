# Calls and meetings — handoff

Nine artboards on `Hamlaneh Chat Calls.dc.html`, plus `settings-meetings` appended to
`Hamlaneh Settings.dc.html`. A design pass over working Phase 2 plumbing —
`components/calls/`, `screens/MeetGuestScreen.tsx`, `components/settings/MeetingsSection.tsx` —
none of which had a styled treatment.

Nothing new was introduced: no design token, no font asset, no typography step. Every value is
already in `webapp/src/tokens.css` or in the delivered chat, auth and settings sets. Preserved
as given: the ≥1280 / 900–1279 / ≤899 breakpoints, ASCII numerals, the platform
`ui-monospace` stack for technical text, and the Lucide inventory.

| Artboard | What it settles |
|---|---|
| `call-prejoin` | No camera present — a notice in info tone, never an error. Joins audio-only. |
| `call-grid` | Five tiles, four camera-off. Call is a sibling of the message column. |
| `call-screenshare` | The sharer's own view. Faces in a 212 rail at logical inline-end. |
| `call-banner` | One strip, two states, permanent 60px slot. |
| `call-ring` | Caller, Answer, Dismiss. Nothing more, and it says so. |
| `meet-guest` | A room card, not the auth treatment. |
| `meet-guest-dead` | One sentence for unknown, expired and revoked. |
| `call-rtl-fa` | Control-bar order, share-rail side, speaker emphasis. |
| `call-states` | Tile, controls, toggles, call and room — both themes. |
| `settings-meetings` | Fourth nav row. The link can never be a column. |

## The tile is the whole design

**It borrows the sidebar's avatar treatment and restates the frame.** The initials circle in
the presence tint is identity, and identity should not change shape between the sidebar and a
call. The frame around it is new, because a tile is a video surface with a persistent name
plate and the sidebar has no equivalent.

The camera-off tile is the designed case — avatar at 54px, the words "Camera off" beneath it,
name plate at the bottom. The camera-on tile is the exception. Per-tile state is a shape or a
word as well as a colour, so it survives the smallest cell in a twelve-person grid:

- **speaking** — 2px ring on all four sides, name weight 600, animated level meter
- **muted** — chip with the slashed glyph in the danger tone
- **camera off** — avatar plus words
- **sharing** — the share replaces the camera in the same tile; chip says which
- **connection poor** — one state, not a meter: amber inset hairline plus chip. No bars, no percentages
- **reconnecting** — the tile keeps its cell so the grid never reflows

## Grid

2 → two cells, one row. 3 → two columns, third at single-column width flush to logical
inline-start. 5 → three columns, two rows, last cell empty. 12 → four columns, three rows.
**Beyond 12 the grid stops growing**: eleven cells carry the most recent speakers, the twelfth
becomes a count cell (`+8 more`) with names beneath — a count, not a participant tile, and not
focusable as a person.

**Phone width is a separate layout.** At ≤899 the stage is a single scrolling column of 16:9
tiles with the active speaker pinned to the top, and the control bar is a fixed 56px bottom row
carrying the same five controls at full size.

## Leave, and where the call lives

Leave ends the call for the person clicking and for nobody else. There is no end-for-everyone
anywhere in this set. It is a solid danger fill with the phone-off glyph and the full words, at
the logical inline-end behind a 22px gap and its own vertical rule, with the three toggles
clustered at the opposite end — so a hand reaching for mute cannot arrive at it, and the
protecting distance is logical and survives the mirror.

**The call is a sibling of the message column, never an overlay.** It takes the upper region of
the channel pane and the conversation continues beneath it. Navigating to another channel does
not end the session: the call collapses to the banner strip carrying **Return to call**. A call
somebody navigated away from must still be leavable — that is the whole reason it is not an
overlay.

## Banner

One strip with two states, not two things. A room is created by whoever joins first, so
starting and joining are the same act with two labels. Because the idle state is always
present, going live changes the strip's content and never the layout — nothing shoves the
message list. Entry is 180ms opacity and a 2px rise on the contents only; it never steals
focus, never animates position, carries no sound, and never becomes a dialog. This is the only
call surface a person meets without having chosen a call.

## meet-guest — the four answers

- **Instance identity**: yes, as one line of text — "Sanjab Coop is hosting this meeting" — and
  no logo lockup. A wordmark over a centred card *is* the front-door treatment.
- **Language switcher**: in the page's own thin top rule at logical inline-end. Identity at one
  end, language at the other. English is the landing language; the switcher persists the choice.
- **375**: stacked. A 64-character name and a 44px target cannot share a 375 row. One row at
  ≥1280.
- **In-call surface**: the entire page. Same tiles, same control bar, sidebar and message column
  simply absent. The top rule persists carrying the meeting title.

**What it refuses**: `AuthShell`, `AuthForm`, `PrimaryButton`, the centred card on a tinted
ground, the product wordmark lockup, any link to sign-in or registration. The field is
`autocomplete="name"` and never `username` or `email` — a password manager offering to save
credentials would say the opposite of the copy.

## settings-meetings — the four answers

- **A records table is right**, once the absence is stated rather than left as a gap. No blank
  link cell, no disabled Copy, no tooltip. The note sits above the table in the reader's path.
  What people do here is audit and revoke, which is row work.
- **"Someone is in it"** is a filled dot with a ring plus the words; hollow dot plus "Nobody in
  it". Nothing feeds this screen live, so the header says "Read when this panel opened" and
  offers Refresh. It must not pulse or animate — that would promise liveness it does not have.
- **Header action opening a dialog**, as `Create invite link` does. Creation ends in the
  show-once credentials panel, so the act is already modal.
- **It belongs in Settings**, and the nav row existing is the answer. Any member may create a
  conference, so the admin dashboard is the one place it must not be.

## Persian mirror

- **Control-bar order** mirrors whole: toggles at logical inline-start (the right in Persian),
  then rule, then count; Leave at logical inline-end (the left in Persian).
- **Share rail** sits at logical inline-end — the left in Persian. The share keeps logical
  inline-start, because the subject leads.
- **Active-speaker emphasis** is direction-neutral: ring on all four sides plus name weight.
  The level meter fills from logical inline-start.
- **No glyph in this set mirrors.** Microphone, microphone-off, video, video-off, monitor-up,
  users and phone-off are objects or states, not directions, and the slash on the off-glyphs is
  a fixed diagonal that reads as a different mark reversed. Only `arrow-left`, `arrow-right` and
  `log-out` mirror in the package; none appears on a call surface.
- Slugs are `<bdi dir="ltr">#deploys</bdi>`; display names are `dir="auto"`, so
  `Omid Rezaei` stays Latin inside a Persian call.

## Not drawn, deliberately

Raise-hand, reactions, emoji, moderator or host controls, mute-someone-else,
remove-from-call, waiting rooms, breakout rooms, recording or any recording indicator, in-call
chat panel, call duration or elapsed timer, network-quality meters beyond the single
poor-connection state, virtual backgrounds, layout pickers. None is built, and drawing any of
them would describe a product that does not exist.

## Open

- **Persian copy on this canvas is mine, not authoritative.** The English strings are lifted
  verbatim from `locales/en/common.json` (`calls.*`, `meet.*`, `settings.meetings.*`); the
  Persian on `call-rtl-fa` needs a native reviewer.
- **`settings-meetings` needs three new keys** the current locale file lacks:
  the "no link column" note as displayed, `Read when this panel opened`, and `Refresh`
  (an existing `admin.error.retry` may serve the last).
- The `Hamlaneh Settings.dc.html` boards drawn before the mono decision still carry
  `'IBM Plex Mono'` refs. The new artboard uses the platform stack. Correcting the older boards
  is a separate pass on that file.
