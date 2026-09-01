/**
 * Conversation setup, through the application's own surfaces.
 *
 * The same rule accounts.ts follows applies here: there is no test-only
 * seeding path. A channel a spec needs is created by the endpoint the "+"
 * calls, a member is added by the endpoint "Invite people" calls, and a
 * message is sent the only way the product will take one — so a fixture can
 * never put the database into a state the product cannot reach.
 *
 * What belongs here is *arrangement*. The act a spec is about is always driven
 * through the browser: the headline test sends its message from the composer,
 * the channel test clicks "+", the DM test uses the picker. Setting up the
 * other side of a two-person conversation here is what keeps each spec's
 * failure pointing at the thing it names.
 *
 * Since ADR 011 the arrangement split runs along a different seam than it used
 * to. Creating a channel, inviting somebody and uploading a file are still one
 * request each. A MESSAGE is not: every conversation an instance creates is
 * end-to-end encrypted, the send path refuses a plaintext body outright, and
 * the only thing that can produce a legal one is an MLS client — which exists
 * only in the browser. `seedMessages` is therefore a page, not a `fetch`, and
 * that is a property of the product rather than a preference of the suite.
 */
import { randomBytes } from "node:crypto";

import { del, expectOk, post, type ApiSession, type TestAccount } from "./accounts";
import type { App } from "./app";
import type { OpenApp } from "./fixtures";

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
  options: { e2ee?: boolean; topic?: string } = {},
): Promise<string> {
  const response = await expectOk(
    await post(session, "/api/v1/channels", { slug, kind, ...options }),
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
 * Removes somebody from a channel — the caller must be its creator, or an
 * admin who is a member (openapi.yaml -> removeChannelMember).
 */
export async function removeMemberApi(
  session: ApiSession,
  channelId: string,
  userId: string,
): Promise<void> {
  await expectOk(
    await del(session, `/api/v1/channels/${channelId}/members/${userId}`),
    "channel member removal",
  );
}

/**
 * Puts messages in a conversation, as `account`, from a real browser.
 *
 * There is no API form of this and there cannot be one. Since ADR 011 every
 * conversation is born end-to-end encrypted, and the send path refuses a body
 * the server could read — `400 e2ee_required` — so a message exists only if an
 * MLS client encrypted it, and the only MLS client is the one compiled into
 * the web app. Arrangement that used to be one `fetch` is a signed-in page
 * driving the composer, which is what e2ee-messaging.e2e.ts already does.
 *
 * TWO CONSEQUENCES, both the protocol's rather than this helper's, and both
 * things a caller has to design around instead of wait out:
 *
 *   - **Readers first.** A device that has never opened the app has published
 *     no key packages, and a client bootstrapping a group can only add devices
 *     whose packages exist. Everybody who must READ these messages has to have
 *     opened the app BEFORE this call. A member added at a later epoch cannot
 *     read an earlier message, ever.
 *   - **No seeding "history".** For the same reason there is no way to arrange
 *     a conversation that already had messages in it before a reader arrived.
 *     A spec that wants prior history has to let its reader watch it happen.
 *
 * The page is returned rather than closed: a spec that seeds usually needs the
 * same person again, and the fixture closes every context it opened anyway.
 */
export async function seedMessages(
  openApp: OpenApp,
  account: TestAccount,
  channelId: string,
  contents: readonly string[],
): Promise<App> {
  const app = await openApp(account, `/c/${channelId}`);
  for (const content of contents) {
    await app.sendMessage(content);
  }
  return app;
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
