<!-- Security fixes for released versions follow coordinated disclosure (SECURITY.md)
     — do not open a public PR for an undisclosed vulnerability. -->

## What & why

<!-- What does this PR change, and why? Link the issue if one exists. -->

## Checklist

- [ ] Tests included in the same commits as the code (regression test for bug fixes)
- [ ] `go test -race` / lint green (server) and/or `npm test` / `lint` / `typecheck` green (webapp)
- [ ] Conventional Commit messages, English, imperative mood
- [ ] No secrets, credentials, or `.env` files committed
- [ ] UI strings go through i18n keys; `fa` keys added; layout works in RTL (if UI-facing)
- [ ] Docs updated where this change makes them stale (README, ROADMAP, API spec)
- [ ] New dependencies justified in the PR description (need, maintenance status, pinned version)
- [ ] Commits signed off (`git commit -s`) — see CONTRIBUTING.md (DCO)
