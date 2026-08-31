#!/usr/bin/env bash
#
# Hamlaneh secure-defaults verification (Phase 0 — this script grows
# every phase, see docs/ROADMAP.md).
#
# Run against a booted stack (docker compose up -d from deploy/):
#   ./verify-defaults.sh
#
# Checks:
#   1. HTTPS endpoint serves the baseline security headers. HSTS comes from
#      Caddy; the content-security headers (CSP, nosniff, Referrer-Policy,
#      Permissions-Policy, Cross-Origin-*) come from the Go server and are
#      checked HERE because the point is that they survive the proxy.
#   2. The real web application is served — not a placeholder
#   3. /healthz returns 200
#   4. The files origin refuses to let an uploaded file behave like a
#      document: attachment + nosniff + a sandboxing CSP (ADR 003)
#   5. The db container publishes no host port at all — checked as a port
#      binding rather than probed, see check_db_not_exposed for why a probe
#      would be the weaker test here
#   6. caddy, server, livekit and db containers all run as a non-root UID
#   7. The media plane exposes its signal path and nothing else (ADR 005):
#      /rtc reaches LiveKit, its RoomService admin API and debug dumps do
#      not, its HTTP port is unpublished, it publishes exactly the three
#      ports the ADR names, and its API secret is in no public response
#
# Prints every check; exits non-zero listing every failed one.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${SCRIPT_DIR}/.env"
PROJECT_LABEL="com.docker.compose.project=hamlaneh"

FAILURES=()
PASS_COUNT=0

pass() {
  PASS_COUNT=$((PASS_COUNT + 1))
  printf 'PASS: %s\n' "$1"
}

failure() {
  FAILURES+=("$1")
  printf 'FAIL: %s\n' "$1" >&2
}

require_tools() {
  local cmd
  for cmd in curl docker timeout grep sed; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      printf 'ERROR: required tool not found: %s\n' "$cmd" >&2
      exit 2
    fi
  done
}

resolve_domain() {
  local d=""
  if [ -f "$ENV_FILE" ]; then
    d="$(sed -n 's/^HAMLANEH_DOMAIN=//p' "$ENV_FILE" | tail -n 1)"
  fi
  printf '%s' "${d:-localhost}"
}

# The files origin's hostname, from the variable the proxy is actually
# configured with. Never "files." glued onto HAMLANEH_DOMAIN: deploy/Caddyfile
# calls that variable load-bearing and explains at length why gluing produced
# a hostname nothing could certify. Gluing it here instead only moves the same
# mistake into the checker, where it probes a name Caddy was never given.
#
# The default matches the Caddyfile's own: {$HAMLANEH_FILES_DOMAIN:files.localhost}.
resolve_files_domain() {
  local d=""
  if [ -f "$ENV_FILE" ]; then
    d="$(sed -n 's/^HAMLANEH_FILES_DOMAIN=//p' "$ENV_FILE" | tail -n 1)"
  fi
  printf '%s' "${d:-files.localhost}"
}

# All HTTPS checks connect to 127.0.0.1:443 while sending the configured
# domain as SNI/Host, so they also work when HAMLANEH_DOMAIN is a real
# domain that does not resolve to this machine locally.
check_security_headers() {
  local headers
  if ! headers="$(curl -skI --max-time 10 "${CONNECT[@]}" "https://${DOMAIN}/")"; then
    failure "HTTPS endpoint https://${DOMAIN}/ unreachable via 127.0.0.1:443"
    return
  fi

  if grep -qi '^strict-transport-security:' <<<"$headers"; then
    pass "Strict-Transport-Security header present"
  else
    failure "Strict-Transport-Security header missing"
  fi

  if grep -qi '^x-content-type-options:[[:space:]]*nosniff' <<<"$headers"; then
    pass "X-Content-Type-Options: nosniff present"
  else
    failure "X-Content-Type-Options: nosniff missing"
  fi

  if grep -qi '^referrer-policy:' <<<"$headers"; then
    pass "Referrer-Policy header present"
  else
    failure "Referrer-Policy header missing"
  fi

  if grep -qi '^permissions-policy:' <<<"$headers"; then
    pass "Permissions-Policy header present"
  else
    failure "Permissions-Policy header missing"
  fi

  if grep -qi '^cross-origin-opener-policy:[[:space:]]*same-origin' <<<"$headers"; then
    pass "Cross-Origin-Opener-Policy: same-origin present"
  else
    failure "Cross-Origin-Opener-Policy: same-origin missing"
  fi

  if grep -qi '^cross-origin-resource-policy:[[:space:]]*same-origin' <<<"$headers"; then
    pass "Cross-Origin-Resource-Policy: same-origin present"
  else
    failure "Cross-Origin-Resource-Policy: same-origin missing"
  fi

  local csp
  csp="$(grep -i '^content-security-policy:' <<<"$headers" | head -n 1)"
  if [ -z "$csp" ]; then
    failure "Content-Security-Policy header missing"
    return
  fi
  pass "Content-Security-Policy header present"

  if grep -q "default-src 'self'" <<<"$csp"; then
    pass "CSP restricts default-src to 'self'"
  else
    failure "CSP does not restrict default-src to 'self'"
  fi

  if grep -q "frame-ancestors 'none'" <<<"$csp"; then
    pass "CSP denies framing (frame-ancestors 'none')"
  else
    failure "CSP frame-ancestors 'none' missing"
  fi

  # The quotes are part of the match, deliberately: CSP keywords are quoted
# tokens, and 'wasm-unsafe-eval' — which the app DOES carry so the MLS core
# can compile (ADR 006) — contains the bare substring "unsafe-eval" while
# being a strictly narrower allowance (wasm compilation only, no JS eval).
# An unquoted grep here would fail the build for the wrong directive.
if grep -q "'unsafe-inline'\|'unsafe-eval'" <<<"$csp"; then
    failure "CSP allows an unsafe source: ${csp}"
  else
    pass "CSP allows no 'unsafe-inline' or 'unsafe-eval'"
  fi
}

# The stack must serve the REAL web application, not a placeholder page.
# The assertion is deliberately something only a built bundle can satisfy:
# index.html has to reference a content-hashed module bundle under /assets/,
# and that bundle has to come back over HTTP. The cache split is checked at
# the same time — caching index.html the way the bundle is cached would pin
# every browser to the previous release and quietly break upgrades.
check_webapp_served() {
  local html asset headers
  if ! html="$(curl -sk --max-time 10 "${CONNECT[@]}" "https://${DOMAIN}/")"; then
    failure "web application document unreachable"
    return
  fi

  asset="$(grep -o '/assets/[A-Za-z0-9._-]*\.js' <<<"$html" | head -n 1)"
  if [ -z "$asset" ]; then
    failure "the served page references no /assets/*.js bundle — is this a placeholder?"
    return
  fi
  pass "the served page references its built bundle (${asset})"

  if curl -sk --max-time 10 -o /dev/null -f "${CONNECT[@]}" "https://${DOMAIN}${asset}"; then
    pass "the bundle itself is served"
  else
    failure "bundle ${asset} is referenced but not served"
    return
  fi

  headers="$(curl -skI --max-time 10 "${CONNECT[@]}" "https://${DOMAIN}${asset}")"
  if grep -qi '^cache-control:.*immutable' <<<"$headers"; then
    pass "the content-hashed bundle is cached immutably"
  else
    failure "the content-hashed bundle has no immutable Cache-Control"
  fi

  headers="$(curl -skI --max-time 10 "${CONNECT[@]}" "https://${DOMAIN}/")"
  if grep -qi '^cache-control:.*no-cache' <<<"$headers"; then
    pass "index.html is revalidated (no-cache), so upgrades take effect"
  else
    failure "index.html is missing Cache-Control: no-cache — upgrades would not reach browsers"
  fi
}

# The files origin (ADR 003), where uploaded bytes are served from.
#
# What is checkable at deploy time is the HEADERS, not a download: a fresh
# install has no uploads, and a signed URL for one could not be forged here
# anyway. That is the stronger assertion in any case. The serving code writes
# this posture BEFORE it looks anything up, so a 404 for an attachment that
# does not exist proves the same thing a real non-image download would — that
# an opaque blob is handed to the browser as a file to save, with sniffing
# off and scripting sandboxed, and can never behave like a document.
#
# The app origin is checked unconditionally, because bare-IP and home-mode
# installs have no separate hostname and serve /files/* from it with the
# identical headers. The separate origin is checked as well: it is defense in
# depth on top of these headers, never instead.
#
# Both are probed on every install, with no case analysis on the domain. The
# Caddyfile declares the files site block unconditionally — at files.<domain>
# where install.sh was given one, and at the files.localhost default where it
# was not — so the hostname always answers and always has to carry the same
# posture. Which of the two it is comes from HAMLANEH_FILES_DOMAIN, the
# variable Caddy is configured with, and never from "files." glued onto
# HAMLANEH_DOMAIN: gluing produced a name Caddy was never given, and doing it
# here only moves that mistake into the checker.
check_files_origin() {
  local probe="/files/00000000-0000-0000-0000-000000000000"

  assert_opaque_blob_headers "app origin" "https://${DOMAIN}${probe}" "${CONNECT[@]}"
  assert_opaque_blob_headers "files origin (${FILES_DOMAIN})" \
    "https://${FILES_DOMAIN}${probe}" --connect-to "${FILES_DOMAIN}:443:127.0.0.1:443"
}

# assert_opaque_blob_headers checks one file URL for the three headers every
# file response carries. Extra arguments are passed to curl (the --connect-to
# mapping differs per origin).
assert_opaque_blob_headers() {
  local label="$1" url="$2"
  shift 2

  local headers
  if ! headers="$(curl -skI --max-time 10 "$@" "$url")"; then
    failure "${label}: ${url} is unreachable"
    return
  fi

  if grep -qi '^x-content-type-options:[[:space:]]*nosniff' <<<"$headers"; then
    pass "${label}: file responses send X-Content-Type-Options: nosniff"
  else
    failure "${label}: file responses are missing X-Content-Type-Options: nosniff"
  fi

  if grep -qi '^content-disposition:[[:space:]]*attachment' <<<"$headers"; then
    pass "${label}: a non-image file is served as an attachment, never as a document"
  else
    failure "${label}: file responses do not force Content-Disposition: attachment"
  fi

  if grep -qi '^content-security-policy:.*sandbox' <<<"$headers"; then
    pass "${label}: file responses are sandboxed by CSP"
  else
    failure "${label}: file responses carry no sandboxing Content-Security-Policy"
  fi
}

check_healthz() {
  local code
  code="$(curl -sk --max-time 10 -o /dev/null -w '%{http_code}' "${CONNECT[@]}" "https://${DOMAIN}/healthz" || true)"
  if [ "$code" = "200" ]; then
    pass "/healthz returns 200"
  else
    failure "/healthz returned '${code:-000}' (expected 200)"
  fi
}

check_db_not_exposed() {
  # Authoritative check: the db container must publish zero host ports.
  # (A raw host-port probe would false-positive on machines that run
  # their own unrelated Postgres on 5432 — dev laptops routinely do.)
  local cid bindings
  cid="$(docker ps -q \
    --filter "label=${PROJECT_LABEL}" \
    --filter "label=com.docker.compose.service=db" | head -n 1)"
  if [ -z "$cid" ]; then
    failure "db container is not running"
    return
  fi
  bindings="$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$cid")"
  if [ "$bindings" = "null" ] || [ "$bindings" = "{}" ]; then
    pass "db container publishes no host ports"
  else
    failure "db container publishes host ports: ${bindings}"
  fi
}

# The media plane (ADR 005).
#
# LiveKit serves three very different things on ONE port, 7880: the signal
# endpoint (/rtc), the RoomService admin API (/twirp/*) and goroutine and
# room dumps (/debug/*). Anything that authenticates against LiveKit's own
# API secret is an admin of every room on the instance, so the split between
# "published" and "internal" is the entire security model here, and it is
# enforced in exactly two places — the Caddyfile matcher and the absence of a
# ports: entry for 7880. Both are checked.
check_livekit() {
  local cid bindings code body

  cid="$(docker ps -q \
    --filter "label=${PROJECT_LABEL}" \
    --filter "label=com.docker.compose.service=livekit" | head -n 1)"
  if [ -z "$cid" ]; then
    failure "livekit container is not running"
    return
  fi

  # Published ports: exactly the three ADR 005 names, and 7880 among them
  # would put the admin API on the internet. Compared as a sorted set so a
  # port silently ADDED here fails too, not only a missing one.
  bindings="$(docker inspect -f '{{range $p, $c := .HostConfig.PortBindings}}{{$p}} {{end}}' "$cid" |
    tr ' ' '\n' | grep -v '^$' | sort | tr '\n' ' ' | sed 's/ $//')"
  if grep -q '7880' <<<"$bindings"; then
    failure "livekit publishes its HTTP/admin port to the host: ${bindings}"
  else
    pass "livekit does not publish its HTTP port (7880) to the host"
  fi
  if [ "$bindings" = "3478/udp 7881/tcp 7882/udp" ]; then
    pass "livekit publishes exactly the three ports ADR 005 names (${bindings})"
  else
    failure "livekit publishes '${bindings}'; ADR 005 names exactly '3478/udp 7881/tcp 7882/udp'"
  fi

  # The signal path must reach LiveKit through Caddy. An anonymous /rtc
  # request answers 401 with LiveKit's own wording, which is the assertion:
  # a 404 would mean the Go server answered and the proxy is not wired, and
  # a 200 would mean LiveKit accepts unauthenticated signalling.
  body="$(curl -sk --max-time 10 "${CONNECT[@]}" "https://${DOMAIN}/rtc/validate" || true)"
  code="$(curl -sk --max-time 10 -o /dev/null -w '%{http_code}' "${CONNECT[@]}" "https://${DOMAIN}/rtc/validate" || true)"
  if [ "$code" = "401" ] && grep -qi 'permissions to access the room' <<<"$body"; then
    pass "/rtc reaches LiveKit through the proxy and refuses an unauthenticated join (401)"
  else
    failure "/rtc/validate returned '${code:-000}' body '${body:-<empty>}'; expected LiveKit's 401"
  fi

  # ...and nothing else of LiveKit may be. Each of these answers 401 when
  # asked of LiveKit directly, so 401 here is a FAILURE: it would mean the
  # admin API is on the public origin, one stolen API secret from total
  # control of every room.
  local path
  for path in \
    /twirp/livekit.RoomService/ListRooms \
    /twirp/livekit.RoomService/CreateRoom \
    /twirp/livekit.EgressService/ListEgress \
    /debug/rooms \
    /debug/goroutine; do
    body="$(curl -sk --max-time 10 "${CONNECT[@]}" -X POST \
      -H 'Content-Type: application/json' -d '{}' "https://${DOMAIN}${path}" || true)"
    if grep -q '"code":"unauthenticated"\|"msg":"permissions denied"' <<<"$body"; then
      failure "LiveKit answered on the public origin at ${path} — its admin API is exposed"
    else
      pass "LiveKit's ${path} is not reachable through the proxy"
    fi
  done

  # The API secret is what mints join tokens for every room on the instance.
  # Nothing anonymous may echo it — least of all the two documents every
  # visitor fetches before signing in.
  local secret
  secret="$(sed -n 's/^HAMLANEH_LIVEKIT_API_SECRET=//p' "$ENV_FILE" 2>/dev/null | tail -n 1)"
  if [ -z "$secret" ]; then
    failure "HAMLANEH_LIVEKIT_API_SECRET is not set in deploy/.env — cannot check for leaks"
    return
  fi
  local url leaked=0
  for url in "https://${DOMAIN}/api/v1/instance" "https://${DOMAIN}/"; do
    body="$(curl -sk --max-time 10 "${CONNECT[@]}" "$url" || true)"
    if grep -qF -- "$secret" <<<"$body"; then
      failure "the LiveKit API secret appears in the response body of ${url}"
      leaked=1
    fi
  done
  if [ "$leaked" -eq 0 ]; then
    pass "the LiveKit API secret appears in neither the instance document nor the login page"
  fi
}

check_nonroot_containers() {
  local svc cid user uid
  for svc in caddy server livekit db; do
    cid="$(docker ps -q \
      --filter "label=${PROJECT_LABEL}" \
      --filter "label=com.docker.compose.service=${svc}" | head -n 1)"
    if [ -z "$cid" ]; then
      failure "container for service '${svc}' is not running"
      continue
    fi
    user="$(docker inspect -f '{{.Config.User}}' "$cid")"
    uid="${user%%:*}"
    case "$uid" in
      ""|root|0)
        failure "service '${svc}' runs as root (Config.User='${user}')"
        ;;
      *)
        pass "service '${svc}' runs as non-root user '${user}'"
        ;;
    esac
  done
}

# Auth surface must fail closed for anonymous callers, with the contract's
# JSON error shape and no account enumeration.
check_auth_defaults() {
  local code body
  code="$(curl -sk --max-time 10 -o /dev/null -w '%{http_code}' "${CONNECT[@]}" "https://${DOMAIN}/readyz" || true)"
  if [ "$code" = "200" ]; then
    pass "/readyz returns 200"
  else
    failure "/readyz returned '${code:-000}' (expected 200)"
  fi

  code="$(curl -sk --max-time 10 -o /dev/null -w '%{http_code}' "${CONNECT[@]}" "https://${DOMAIN}/api/v1/admin/users" || true)"
  if [ "$code" = "401" ]; then
    pass "anonymous request to admin API is rejected (401)"
  else
    failure "anonymous GET /api/v1/admin/users returned '${code:-000}' (expected 401)"
  fi

  # The roadmap's gate asks for "signup 403 by default". There is no
  # self-serve signup route in the contract at all -- registration_mode has
  # never had a door behind it -- so the true assertion is stronger than the
  # one asked for: the endpoint does not exist. If somebody later builds one,
  # this check fails and they have to come here and decide deliberately what
  # a closed instance answers, which is the point.
  local path
  for path in /api/v1/auth/register /api/v1/auth/signup /api/v1/users; do
    code="$(curl -sk --max-time 10 -o /dev/null -w '%{http_code}' "${CONNECT[@]}"       -X POST "https://${DOMAIN}${path}" -H 'Content-Type: application/json' -d '{}' || true)"
    case "$code" in
      404) pass "no self-serve signup at ${path}" ;;
      401|403) pass "self-serve signup at ${path} refuses anonymous callers (${code})" ;;
      *) failure "POST ${path} returned '${code:-000}'; a closed instance must not accept self-serve signup" ;;
    esac
  done

  body="$(curl -sk --max-time 10 "${CONNECT[@]}" -X POST "https://${DOMAIN}/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d '{"identifier":"no-such-user-verify-defaults","password":"definitely-wrong-password"}' || true)"
  if printf '%s' "$body" | grep -q '"invalid_credentials"'; then
    pass "login with bad credentials fails with the generic invalid_credentials error"
  else
    failure "login with bad credentials did not return the invalid_credentials contract error (got: ${body:-<empty>})"
  fi
}

main() {
  require_tools

  DOMAIN="$(resolve_domain)"
  FILES_DOMAIN="$(resolve_files_domain)"
  CONNECT=(--connect-to "${DOMAIN}:443:127.0.0.1:443")
  printf 'Verifying secure defaults for https://%s/ (via 127.0.0.1)\n\n' "$DOMAIN"

  check_security_headers
  check_webapp_served
  check_healthz
  check_files_origin
  check_auth_defaults
  check_livekit
  check_db_not_exposed
  check_nonroot_containers

  printf '\n%d passed, %d failed\n' "$PASS_COUNT" "${#FAILURES[@]}"
  if [ "${#FAILURES[@]}" -gt 0 ]; then
    printf 'Failed checks:\n' >&2
    local f
    for f in "${FAILURES[@]}"; do
      printf '  - %s\n' "$f" >&2
    done
    exit 1
  fi
  printf 'All secure-default checks passed.\n'
}

main "$@"
