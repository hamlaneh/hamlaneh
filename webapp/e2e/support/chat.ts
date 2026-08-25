/**
 * Conversation setup, through the application's own API.
 *
 * The same rule accounts.ts follows applies here: there is no test-only
 * seeding path. A channel a spec needs is created by the endpoint the "+"
 * calls, a member is added by the endpoint "Invite people" calls, and a
 * message is sent by the endpoint the composer calls — so a fixture can never
 * put the database into a state the product cannot reach.
 *
 * What belongs here is *arrangement*. The act a spec is about is always driven
 * through the browser: the headline test sends its message from the composer,
 * the channel test clicks "+", the DM test uses the picker. Setting up the
 * other side of a two-person conversation over the API is what keeps each
 * spec's failure pointing at the thing it names.
 */
import { randomBytes, randomUUID } from "node:crypto";

import { expectOk, post, type ApiSession } from "./accounts";

/** Matches the contract's channel-slug shape, and carries no digits. */
export function uniqueSlug(prefix = "chan"): string {
  // Letters only, so a spec can assert on a sidebar badge's numeral without
  // the channel's own name being able to contain it.
  const alphabet = "abcdefghijklmnopqrstuvwxyz";
  const suffix = [...randomBytes(8)].map((byte) => alphabet[byte % alphabet.length]).join("");
  return `${prefix}-${suffix}`.slice(0, 64);
}

/** The wire form of a mention (openapi.yaml -> SendMessageRequest.content). */
export function mentionToken(userId: string): string {
  return `<@${userId}>`;
}

/** Creates a channel as this session's user, who becomes its sole member. */
export async function createChannelApi(
  session: ApiSession,
  slug: string,
  kind: "public" | "private" = "public",
): Promise<string> {
  const response = await expectOk(
    await post(session, "/api/v1/channels", { slug, kind }),
    `channel creation (${slug})`,
  );
  const { id } = (await response.json()) as { id: string };
  return id;
}

/** Invites a user into a channel this session's user is already in. */
export async function inviteApi(
  session: ApiSession,
  channelId: string,
  userId: string,
): Promise<void> {
  await expectOk(
    await post(session, `/api/v1/channels/${channelId}/members`, { user_id: userId }),
    "channel invitation",
  );
}

/**
 * Sends a message and returns its id.
 *
 * The idempotency key is generated per call, exactly as the composer does:
 * reusing one across sends would make the second send a lookup of the first
 * (ws-protocol.md §5) and the test would silently assert on one message.
 */
export async function sendMessageApi(
  session: ApiSession,
  channelId: string,
  content: string,
): Promise<string> {
  const response = await expectOk(
    await post(session, `/api/v1/channels/${channelId}/messages`, {
      client_msg_id: randomUUID(),
      content,
    }),
    "message send",
  );
  const { id } = (await response.json()) as { id: string };
  return id;
}

/** Opens (or reuses) the direct message between this session's user and one other. */
export async function openDirectMessageApi(
  session: ApiSession,
  userId: string,
): Promise<string> {
  const response = await expectOk(
    await post(session, "/api/v1/dms", { user_id: userId }),
    "direct message",
  );
  const { id } = (await response.json()) as { id: string };
  return id;
}
