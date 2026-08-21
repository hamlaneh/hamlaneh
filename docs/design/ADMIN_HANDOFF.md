# Admin dashboard — design handoff

Mockup: `Hamlaneh Admin.dc.html` (Claude Design canvas).
Tokens: reuses `webapp/src/tokens.css` unchanged — no new colour, type or spacing tokens.
Brief: `docs/design/BRIEFS.md` §3.

## What is on the canvas

Eight artboards plus `admin-components`. The brief asked for seven; the eighth
(`admin-create-user-credentials`) is the credentials-shown-once state the brief requires in
prose but did not list as its own artboard.

| Artboard | Notes |
|---|---|
| `admin-users` | Populated, 44px rows, one deactivated user, one row hover. |
| `admin-users-empty` | Fresh install — the admin is the only account; both routes to a first user. |
| `admin-create-user` | Modal over the table, generated temporary password. |
| `admin-create-user-credentials` | The one-time step: warning band, copy and download before the only close. |
| `admin-invites` | Open links, nearest expiry flagged, revoke as outlined danger. |
| `admin-org-settings` | Two columns; registration mode as a radio group with the Open warning. |
| `admin-audit-log` | Filter bar, export, older/newer paging, destructive actions tinted. |
| `admin-users-dark` | Token-mapped counterpart. |
| `admin-components` | Mode signals, table states, row-action menu, confirms, new controls, empty/error states, behaviour. |

## The mode signal

Three things separate admin from chat, and nothing else does:

1. **The sidebar is white** (`surface`) where chat's is tinted (`surface.subtle`). Tinted means
   conversation, white means records.
2. **`Back to chat` sits above the org identity** and is first in the tab order. Admin is
   somewhere you visit, not somewhere you live.
3. **One `accent.warm` kicker** reading `ADMINISTRATION`, exactly once per screen. It never
   becomes a button, a link or a status.

## Geometry

Sidebar 260 · content padding-inline 40 · nav row 44 · table row 44 · table header 42 ·
modal 560 · settings card column 540.

## Decisions made where the brief left them open

- **Nav** replaces chat's sidebar rather than sitting beside a collapsed one (your pick).
- **Create user** is a centred modal over the table.
- **Credentials** get a distinct step with a warning band above everything, copy and download
  offered before the only close button, and Escape deliberately disabled on that one dialog.
- **Tables** are compact 44px rows — still clear of the 40px floor, and more visible at once.
- **Audit log** implies mid scale (1,284 entries): a real filter bar, CSV export, older/newer
  paging rather than numbered pages.
- **Row actions** live in one ellipsis menu, not a row of icons, so the table reads the same at
  12 users and 400. Destructive items are last, separated by a rule, `danger`-coloured in the
  menu — never a filled red button in a row.
- **Org settings save immediately** and the page subtitle says so. No Save button to forget.

## Implementation notes

- Deactivate confirms and states that all sessions end. Force-reset confirms and states the
  session survives. That difference is the point of the sentence.
- Reactivate is the inverse and is **constructive**: `brand.primary` in the menu, not `danger`.
  It restores the ability to sign in; it does not restore the sessions that were killed. A
  deactivated row swaps only the last menu item, so the menu never reorders under the pointer.
  Both menu variants are on `admin-components` §02.
- Removing the last admin is **blocked and explained**, not warned about afterwards.
- Generated values (temporary passwords, invite links) are set in mono with `dir="ltr"` so they
  read correctly in a Persian interface.
- Tables ship as real `<table>` markup with `<th scope="col">`; the flex layout in the mockup is
  a drawing convenience.
- Row focus is an inset 3px ring so the card's `overflow:hidden` cannot clip it.
- Modals trap focus, close on Escape and restore focus to their opener — except the credentials
  panel, which needs a deliberate acknowledgement.
- The password minimum set here feeds every password screen: login's forced change, reset, and
  self-service change all read it from this setting.
- Empty-because-nothing-exists and empty-because-filtered are different states with different
  copy and different actions.

## New icons (Lucide, 24×24, 1.75 stroke)

scroll-text · copy · refresh-cw · chevron-down · chevron-left · chevron-right · user-plus ·
user-minus

## Not in scope

Channel administration, retention settings, backups, SMTP configuration, licence or billing.
None are in §3.

## STATUS.md row

```
| Admin dashboard (8 artboards) | BRIEFS.md §3 | DESIGNED | Hamlaneh Admin.dc.html |
```
