/**
 * Instance password policy.
 *
 * The minimum is instance policy, not a product constant: the delivered
 * design renders it from the value served with the form. No endpoint exposes
 * it yet, so it lives here — in exactly one place — and matches the contract
 * (`ChangePasswordRequest.new_password`, minLength 12 in docs/api/openapi.yaml).
 * When the policy endpoint lands, this constant becomes its default.
 */
export const PASSWORD_MIN_LENGTH = 12;
