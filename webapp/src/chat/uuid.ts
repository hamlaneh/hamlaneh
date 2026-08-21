const UUID_PATTERN =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * Whether a value is a uuid, as the contract types every id.
 *
 * Route parameters are attacker-controlled: a permalink id is interpolated
 * into a request path and into a DOM lookup, so it is checked at the boundary
 * rather than trusted because it came from the address bar.
 */
export function isUuid(value: string): boolean {
  return UUID_PATTERN.test(value);
}

/**
 * The contract types `client_msg_id` as a uuid, so the idempotency key has to
 * be one. `crypto.randomUUID` is present in every browser this app supports
 * and in the test environment; the getRandomValues path is the fallback for a
 * non-secure context, where randomUUID is not exposed but getRandomValues is.
 *
 * There is deliberately no Math.random fallback: a silent weak-randomness path
 * is worse than a thrown error.
 */
export function newUuid(): string {
  const cryptoApi = globalThis.crypto as Crypto | undefined;
  if (cryptoApi !== undefined && typeof cryptoApi.randomUUID === "function") {
    return cryptoApi.randomUUID();
  }
  if (cryptoApi === undefined || typeof cryptoApi.getRandomValues !== "function") {
    throw new Error("No cryptographic random source is available");
  }
  const bytes = new Uint8Array(16);
  cryptoApi.getRandomValues(bytes);
  // RFC 4122 version 4, variant 10xx.
  bytes[6] = ((bytes[6] ?? 0) & 0x0f) | 0x40;
  bytes[8] = ((bytes[8] ?? 0) & 0x3f) | 0x80;
  const hex = [...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join("");
  return [
    hex.slice(0, 8),
    hex.slice(8, 12),
    hex.slice(12, 16),
    hex.slice(16, 20),
    hex.slice(20),
  ].join("-");
}
