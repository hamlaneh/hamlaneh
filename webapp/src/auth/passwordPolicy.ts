/**
 * Instance password policy.
 *
 * The minimum is instance policy, not a product constant: screens read it from
 * GET /api/v1/instance (`password_min_length`) via `useInstance()`. This
 * constant is only the fallback that applies until that document answers, or
 * if it never does; it matches the contract's own floor
 * (`ChangePasswordRequest.new_password`, minLength 12 in docs/api/openapi.yaml).
 */
export const PASSWORD_MIN_LENGTH = 12;

/**
 * How long an emailed reset link stays usable, in minutes.
 *
 * Mirrors the contract (`/auth/reset-request`: "Tokens are 256-bit,
 * single-use, thirty-minute"). The `reset-request-confirmation` artboard says
 * one hour; the shipped backend says thirty minutes, and printing the
 * artboard's number would tell the user something untrue — so the copy
 * interpolates this instead. Recorded for the designer in the slice report.
 */
export const RESET_TOKEN_TTL_MINUTES = 30;
