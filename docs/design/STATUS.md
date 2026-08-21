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

## Brand assets

The symbol SVGs live in `webapp/public/brand/` (the 4096px PNG renders and the kit zip are
deliberately not committed — derived artefacts, and git history is not rewritable).

**RESOLVED 2026-08-21 — the design does not change and the logo is not recoloured.** The symbol
is a project-level brand asset: it identifies the repository and the product (README, favicon,
anywhere outside the delivered screens), and serves as the fallback wherever a logo is needed but
none exists. It is deliberately **not** inserted into the delivered UI, which draws the product
name as text and has no slot for a symbol.

Recorded for the future, since the two palettes do differ: The kit declares its brand
colours as Indigo `#4F46E5`, Blue `#3B82F6`, Teal `#14B8A6` on a Slate `#0F172A` dark backdrop.
The delivered design system uses none of them: brand `#235C55`/`#81C9BD` on a warm `#F7F6F2`
light ground and a near-black-green `#111615` dark ground. Placed together they read as two
products, and the dark tile clashes hardest (navy vs green-black).

the kit is indigo/blue/teal on navy, the interface is green-teal on a warm ground. They are not
placed next to each other today, so nothing is broken; if a future screen ever puts the symbol
beside the interface chrome, that is the moment to revisit it.

Still missing: the full lockup (symbol + wordmark) exists only as PNG. An SVG would be needed for
the README and any future site.
