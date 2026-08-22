# Design Status Registry

Source of truth for which screens have a finished mockup. Frontend agents check this table
before building any screen (see CLAUDE.md "UI pipeline"). The user updates rows when Claude
Design deliverables land. `PENDING` → build unstyled functional UI only.

Functional requirements per screen (for producing the designs): [BRIEFS.md](BRIEFS.md).

| Screen set | Brief | Status | Mockup |
|---|---|---|---|
| Login & password reset (10 artboards) | BRIEFS.md §1 | **DESIGNED** — delivered 2026-08-21 | `Hamlaneh Auth.dc.html` ([canvas](https://claude.ai/design/p/185f4552-36fc-489e-a9b1-c012ab74f6d6), handoff copied to [LOGIN_HANDOFF.md](LOGIN_HANDOFF.md)) |
| Chat shell (9 artboards + components) | BRIEFS.md §2 | **DESIGNED** — delivered 2026-08-21 | `Hamlaneh Chat.dc.html` (handoff: [CHAT_HANDOFF.md](CHAT_HANDOFF.md)) |
| Admin dashboard (8 artboards + components) | BRIEFS.md §3 | **DESIGNED** — delivered 2026-08-21 | `Hamlaneh Admin.dc.html` (handoff: [ADMIN_HANDOFF.md](ADMIN_HANDOFF.md)) |
| User settings (6 artboards + components) | BRIEFS.md §4 | **DESIGNED** — delivered 2026-08-21 | `Hamlaneh Settings.dc.html` (handoff: [SETTINGS_HANDOFF.md](SETTINGS_HANDOFF.md)) |
| Call / meeting view | BRIEFS.md §5 | PENDING — do not design yet (Phase 2) | — |
| Chat overlays — create channel, people picker (invite + new DM), mention picker | BRIEFS.md §2 addendum | **DESIGNED** — delivered 2026-08-21 as 13 `chat-addendum-*` artboards; plumbing exists and awaits the reskin | `Hamlaneh Chat.dc.html` (handoff: [CHAT_HANDOFF.md](CHAT_HANDOFF.md)) |
| Channel menu, account menu | BRIEFS.md §2 addendum 2 | **DESIGNED** — delivered 2026-08-21 as 11 further `chat-addendum-*` artboards; plumbing exists and awaits the reskin | `Hamlaneh Chat.dc.html` (handoff: [CHAT_HANDOFF.md](CHAT_HANDOFF.md)) |
| Recovery-code entry on `login-totp` | BRIEFS.md §1 | `awaiting-design` — the `login-totp` artboard draws only the six authenticator cells, so a user who lost their authenticator had nowhere to type the recovery codes enrolment gave them; the screen now switches between the six cells and one plain recovery-code field, built entirely from delivered parts (`TextField`, `hm-text-button`) with no new visual treatment | — (needs a `login-totp-recovery` variant of `Hamlaneh Auth.dc.html`) |

## Brand assets

The symbol SVGs live in `webapp/public/brand/` (the 4096px PNG renders and the kit zip are
deliberately not committed — derived artefacts, and git history is not rewritable).

**RESOLVED 2026-08-21.** The symbol was recoloured to the product palette and the delivered
UI design is unchanged — the two decisions are separate and both hold:

- The mark now uses the exact Quiet Nest tokens: light gradient
  `#123F39 → #194941 → #235C55`, dark gradient `#6FB5AA → #81C9BD → #9ADACF`, tiles on
  `#F7F6F2` and `#111615 → #18201E` (the dark tile is the app's own canvas, no longer navy).
  Flat single-colour variants ship in `flat/` for 16–32px and favicon use, where a three-stop
  gradient collapses into a smudge.
- **No screen changes.** The delivered artboards draw the product name as text and have no slot
  for a symbol, so the mark is not inserted into them. It is a project-level asset: repository
  and product identity, and the fallback wherever a logo is needed but none exists.

All eight SVGs verified self-contained before shipping — no raster, no external references, no
scripts or filters, `role="img"` and `aria-labelledby` intact — which matters because they are
served by an app under a strict CSP.

Still missing: the full lockup (symbol + wordmark) exists only as PNG. An SVG would be needed for
the README and any future site.
