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
| Channel menu, account menu | BRIEFS.md §2 (implied, not drawn) | **awaiting-design** — unstyled plumbing; not covered by the addendum | — |

## Brand assets

The symbol SVGs live in `webapp/public/brand/` (the 4096px PNG renders and the kit zip are
deliberately not committed — derived artefacts, and git history is not rewritable).

**Open — the logo and the UI are currently two different palettes.** The kit declares its brand
colours as Indigo `#4F46E5`, Blue `#3B82F6`, Teal `#14B8A6` on a Slate `#0F172A` dark backdrop.
The delivered design system uses none of them: brand `#235C55`/`#81C9BD` on a warm `#F7F6F2`
light ground and a near-black-green `#111615` dark ground. Placed together they read as two
products, and the dark tile clashes hardest (navy vs green-black).

Not resolved in implementation, because recolouring a logo is design authorship. Three things
need a decision:

1. Which is the brand — the kit or Quiet Nest? (Implementation has assumed Quiet Nest, since it
   is implemented across four delivered screen sets, but that is an assumption, not a decision.)
2. Where does the symbol appear in the product? The login artboard draws the product name as
   **text**, with no symbol anywhere — so the mark has no designed home yet.
3. The full lockup exists only as PNG; an SVG is needed for the README and any future site.

A recolour brief with the exact token values is ready to hand to the design pipeline.
