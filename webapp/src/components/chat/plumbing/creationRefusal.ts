/**
 * The refusals a creation surface has to say out loud, by the contract's own
 * error codes (openapi.yaml -> `CreateChannelRequest.e2ee`,
 * `OpenDirectMessageRequest.e2ee`).
 *
 * Both mean the same thing: this client asked for a conversation the
 * organisation's encryption mode does not allow, which happens when its view
 * of that mode is stale. The server refuses rather than quietly creating the
 * opposite (ADR 011 decision 1), because the property is fixed forever at
 * creation and a silent substitution is how an immutable surprise is made. So
 * the surface must not swallow it into "that did not work".
 *
 * Everything else keeps the surface's own generic message: a slug already
 * taken or a server that did not answer is not a thing this sentence explains.
 */
export type CreationRefusalKey =
  | "chat.e2eeByOrg.requiredError"
  | "chat.e2eeByOrg.forbiddenError";

export function creationRefusalKey<Fallback extends string>(
  code: string,
  fallbackKey: Fallback,
): CreationRefusalKey | Fallback {
  if (code === "e2ee_required_by_org") {
    return "chat.e2eeByOrg.requiredError";
  }
  if (code === "e2ee_forbidden_by_org") {
    return "chat.e2eeByOrg.forbiddenError";
  }
  return fallbackKey;
}
