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
