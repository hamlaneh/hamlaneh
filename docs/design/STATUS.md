# Design Status Registry

Source of truth for which screens have a finished mockup. Frontend agents check this table
before building any screen (see CLAUDE.md "UI pipeline"). The user updates rows when Claude
Design deliverables land. `PENDING` → build unstyled functional UI only.

Functional requirements per screen (for producing the designs): [BRIEFS.md](BRIEFS.md).

| Screen set | Brief | Status | Mockup |
|---|---|---|---|
| Login & password reset (7 artboards) | BRIEFS.md §1 | PENDING — **needed first** | — |
| Chat shell (sidebar, messages, composer) | BRIEFS.md §2 | PENDING | — |
| Admin dashboard (users, invites, org, audit) | BRIEFS.md §3 | PENDING | — |
| User settings (profile, security, sessions) | BRIEFS.md §4 | PENDING | — |
| Call / meeting view | BRIEFS.md §5 | PENDING — do not design yet (Phase 2) | — |
