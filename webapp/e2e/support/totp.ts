/**
 * One-time codes for the two-step verification flows.
 *
 * The arithmetic comes from `otpauth` (MIT, over @noble/hashes) rather than
 * from a hand-written HMAC here: CLAUDE.md principle 3 — assemble, don't
 * reinvent — and a subtly wrong test-side generator would show up as a
 * flaky suite, which is the worst possible way to learn about it.
 *
 * Every parameter is taken from the server's own otpauth URI, so the suite
 * cannot drift from the server's choice of algorithm, digits or period.
 */
import { TOTP, URI } from "otpauth";

function parse(otpauthUri: string): TOTP {
  const parsed = URI.parse(otpauthUri);
  if (!(parsed instanceof TOTP)) {
    throw new Error(`expected a totp otpauth URI, got ${otpauthUri.split("?")[0] ?? otpauthUri}`);
  }
  return parsed;
}

/** The code valid right now for the account the URI describes. */
export function totpCode(otpauthUri: string, at: number = Date.now()): string {
  return parse(otpauthUri).generate({ timestamp: at });
}

/**
 * The code to sign in with, for an account this run has just enrolled.
 *
 * The server never accepts a time step twice (`totp.Verify` skips every
 * candidate step at or below the last one used), and enrolment burns the
 * step its verification code came from. A sign-in a second later is usually
 * still inside that same thirty-second step, so the "current" code would be
 * refused as a replay — a flaky failure that looks like a broken login.
 *
 * Reaching one step forward avoids it without waiting: the server's ±1 skew
 * accepts the next step's code, and that step is by definition later than
 * the one enrolment consumed.
 */
export function loginTotpCode(otpauthUri: string): string {
  return totpCode(otpauthUri, Date.now() + 30_000);
}

/**
 * A syntactically valid code that is not the current one.
 *
 * It is generated a long way outside the server's ±1-step skew window, so it
 * is refused for being wrong rather than for being malformed — which is what
 * the "a wrong code does not sign you in" assertion needs to mean.
 */
export function wrongTotpCode(otpauthUri: string): string {
  const totp = parse(otpauthUri);
  const now = Date.now();
  const far = totp.generate({ timestamp: now - 3_600_000 });
  // Astronomically unlikely, but a collision would make the test assert the
  // opposite of what it says; nudge to a different step instead of hoping.
  return far === totp.generate({ timestamp: now }) ? totp.generate({ timestamp: now - 7_200_000 }) : far;
}
