#!/usr/bin/env bash
#
# Hamlaneh secure-defaults verification (Phase 0 — this script grows
# every phase, see docs/ROADMAP.md).
#
# Run against a booted stack (docker compose up -d from deploy/):
#   ./verify-defaults.sh
#
# Checks:
#   1. HTTPS endpoint serves the baseline security headers
#      (HSTS, nosniff, Referrer-Policy, CSP incl. frame-ancestors 'none')
#   2. /healthz returns 200
#   3. Postgres is NOT reachable from the host (localhost:5432 closed)
#   4. caddy, server and db containers all run as a non-root UID
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

check_nonroot_containers() {
  local svc cid user uid
  for svc in caddy server db; do
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

main() {
  require_tools

  DOMAIN="$(resolve_domain)"
  CONNECT=(--connect-to "${DOMAIN}:443:127.0.0.1:443")
  printf 'Verifying secure defaults for https://%s/ (via 127.0.0.1)\n\n' "$DOMAIN"

  check_security_headers
  check_healthz
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
