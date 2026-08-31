# Hamlaneh desktop (Tauri v2)

A native window that points at *your* instance. It is deliberately thin: one local page
(`ui/`) takes an address, and everything after that is the instance's own web application,
served from its own origin over the network — the identical bundle a browser loads.

## Why it does not bundle the web application

`webapp/src/api/client.ts` sets its base URL to `window.location.origin`, the session lives in
an HttpOnly cookie and CSRF is a double-submit cookie beside it. A copy of the SPA embedded in
the app would therefore run on `tauri://localhost`: an origin with no server behind it, which
cannot hold that instance's cookies and could not reach its API without CORS and a
cross-origin session the server does not offer. Loading the real origin keeps every mechanism
exactly as built and tested. It is a constraint, not a preference.

The consequence worth knowing: an instance using Caddy's **internal** CA (the default when
`HAMLANEH_DOMAIN=localhost`) presents a certificate the system webview will not trust, and
there is no override here — trusting it is the operating system's decision, made once, by the
person who owns the machine. A public certificate or home mode over `http://localhost:8080`
both work as-is.

## Building

```
npm ci
npm test          # node --test — the address guard and en/fa parity
npm run build     # npx tauri build
```

Unlike the rest of the repository, this **does** need a Rust toolchain on the host, and a
per-OS one: a Windows installer can only be produced on Windows. That is inherent to Tauri
(CLAUDE.md tech stack) and is why `webapp/src-mls`'s "Rust is not a development-host
requirement" note does not extend here. CI builds all three (`.github/workflows/ci.yml`, job
`desktop`); most contributors never need to build this locally.

Linux additionally needs the WebKitGTK development packages — the CI job's
"Install the WebKitGTK toolchain" step is the current list.

## Icons

`src-tauri/icons/` is generated, not drawn:

```
npx tauri icon ../webapp/public/brand/icons/icon-512.png -o src-tauri/icons
```

That source is the committed brand mark (`webapp/public/brand/symbol-light-bg.svg`, rendered by
`webapp/scripts/generate-icons.mjs`). Nothing here is a new visual decision. The generated iOS,
Android and Microsoft Store variants are deleted after each run: this app targets desktop only.
