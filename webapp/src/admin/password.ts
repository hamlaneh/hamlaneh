/**
 * The temporary password the create-user modal generates.
 *
 * The contract puts the initial password in the *request* (openapi.yaml ->
 * AdminCreateUserRequest.password), so the client is what invents it. That
 * makes the generator a security surface, not a cosmetic one: it uses
 * `crypto.getRandomValues`, never `Math.random`.
 *
 * The alphabet is 32 characters — a power of two, so masking a random byte
 * with 31 is uniform and needs no rejection loop — and it omits the pairs a
 * person mistypes when reading a password off a screen: l/1/I and o/0/O.
 */

/** 32 unambiguous lowercase letters and digits. */
const ALPHABET = "abcdefghijkmnpqrstuvwxyz23456789";

/** Characters per dash-separated group, as the artboard's value is grouped. */
const GROUP = 4;

/**
 * A password of at least `minLength` characters, written as groups of four
 * joined by dashes ("tarn-vault-9042" on the artboard is the same shape).
 *
 * `minLength` is the instance's own policy (GET /api/v1/instance ->
 * password_min_length), so a stricter instance gets a longer password rather
 * than a rejected one.
 */
export function generateTemporaryPassword(minLength: number): string {
  // n groups spell n*GROUP characters plus n-1 dashes.
  const groups = Math.max(3, Math.ceil((minLength + 1) / (GROUP + 1)));
  const bytes = new Uint8Array(groups * GROUP);
  crypto.getRandomValues(bytes);

  const letters = [...bytes].map((byte) => ALPHABET[byte & 31] ?? "a");
  const chunks: string[] = [];
  for (let index = 0; index < groups; index += 1) {
    chunks.push(letters.slice(index * GROUP, (index + 1) * GROUP).join(""));
  }
  return chunks.join("-");
}
