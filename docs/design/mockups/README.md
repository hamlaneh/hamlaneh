# Delivered mockups

Design source of truth, mirrored from the Claude Design canvas so implementation agents (and
future contributors) do not depend on external access.

| File | What it is |
|---|---|
| `Hamlaneh Auth.dc.html` | Login & password reset, delivered 2026-08-21. Ten screen artboards at 1440×900, a live shell, and the `auth-foundations-and-components` sheet (tokens, type, every component state, behaviour, Persian reference, font/icon plan). Written contract: [../LOGIN_HANDOFF.md](../LOGIN_HANDOFF.md). |

Read these as plain HTML — every artboard is inline markup and inline styles, so the design is
fully readable from the file itself.

To view one rendered, open the canvas:
<https://claude.ai/design/p/185f4552-36fc-489e-a9b1-c012ab74f6d6>. The `support.js` the file
references is the canvas's own generated React runtime (`dc-runtime`); it carries no design
information and is deliberately not vendored here.

Extracted design tokens live in `webapp/src/tokens.css` — that file is the delivered token
sheet verbatim and must not be edited by hand; changes come from the design pipeline.
