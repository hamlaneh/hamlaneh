import { http, HttpResponse } from "msw";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { api } from "../api/client";
import { CHAT_USERS, setMockEncryptionMode } from "../mocks/chat";
import { resetMockAuth } from "../mocks/handlers";
import { mockMlsDevices, mockMlsGroup, mockMlsWelcomes, seedMockMlsDevice } from "../mocks/mls";
import { server } from "../mocks/node";
import { fromBase64, toBase64, unpackStrings } from "./bytes";
import { fakeKeyPackage, fakeMlsModule, fakeSignatureKey, fakeWorld } from "./fakeModule";
import { memoryStore, Keystore } from "./keystore";
import { safetyNumber } from "./safetyNumber";
import { MlsService } from "./service";
import type { MlsState } from "./types";
import { needsAttention } from "./types";
import { setMlsModule } from "./wasm";

/**
 * The service against the contract mocks, with the wasm wrapper replaced by a
 * double (see `fakeModule.ts`). These are tests about orchestration: who is
 * claimed, what is committed, what happens when the epoch moves, and what the
 * screen is told. Real MLS is proved in `wasm.roundtrip.test.ts`.
 */

const ME = CHAT_USERS.me.id;
const OTHERS = [CHAT_USERS.nasrin.id, CHAT_USERS.omid.id, CHAT_USERS.parisa.id];

const encoder = new TextEncoder();

/** The devices of one test, all in one world so their leaves can meet. */
let world = fakeWorld();

/** A signature key as the member-device directory carries it: base64. */
function directoryKey(identity: string): string {
  return toBase64(encoder.encode(fakeSignatureKey(identity)));
}

/**
 * A peer with `packages` key packages published, registered under the very
 * key the leaf they join with will carry — the honest case, and the one every
 * test that is not about a planted leaf wants. A fixture whose directory
 * entry disagreed with its leaf would be swept out on the next reconcile.
 */
function seedDevice(userId: string, packages = 1) {
  return seedMockMlsDevice(
    userId,
    Array.from({ length: packages }, () => toBase64(fakeKeyPackage(userId))),
    directoryKey(userId),
  );
}

/** One page of the directory, as the roster this test wants it read. */
function directoryPage(userIds: readonly string[]) {
  return HttpResponse.json({
    members: userIds.map((userId) => ({
      user_id: userId,
      signature_public_keys: mockMlsDevices()
        .filter((device) => device.userId === userId)
        .map((device) => device.signaturePublicKey),
    })),
  });
}

/** Every leaf this device holds for the channel, as `identity|key`. */
function leavesOf(channelId: string): string[] {
  const device = world.devices.get(ME);
  if (device === undefined) {
    throw new Error("this world holds no device for the signed-in user");
  }
  const groupId = fromBase64(mockMlsGroup(channelId)?.groupId ?? "");
  const identities = unpackStrings(device.member_identities(groupId));
  const keys = unpackStrings(device.member_signature_keys(groupId));
  return identities.map((identity, index) => `${identity}|${keys[index] ?? ""}`);
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

afterEach(() => {
  server.resetHandlers();
  resetMockAuth();
  setMlsModule(null);
  // One case puts the mock instance in Compliance; every other case in this
  // file assumes the mode every install actually has.
  setMockEncryptionMode("strict");
});

afterAll(() => {
  server.close();
});

let states: MlsState[] = [];

function newService(keystore: Keystore | null = null) {
  states = [];
  return new MlsService({
    currentUserId: ME,
    onChange: (state) => {
      states.push(state);
    },
    keystore,
  });
}

function latest(): MlsState {
  const state = states.at(-1);
  if (state === undefined) {
    throw new Error("the service published no state");
  }
  return state;
}

/** A fresh encrypted channel, created through the contract like a client. */
async function createE2eeChannel(slug: string): Promise<string> {
  const { data } = await api.POST("/api/v1/channels", {
    body: { slug, kind: "private", e2ee: true },
  });
  if (data === undefined) {
    throw new Error("the fixture channel could not be created");
  }
  return data.id;
}

beforeEach(() => {
  world = fakeWorld();
  setMlsModule(fakeMlsModule(world));
});

/** A fresh encrypted direct message, opened through the contract. */
async function openE2eeDm(userId: string): Promise<string> {
  const { data } = await api.POST("/api/v1/dms", { body: { user_id: userId, e2ee: true } });
  if (data === undefined) {
    throw new Error("the fixture direct message could not be opened");
  }
  return data.id;
}

describe("start", () => {
  it("registers the device and publishes a key-package pool", async () => {
    const service = newService();
    expect(await service.start()).toBe(true);
    expect(latest().device).toEqual({ status: "ready" });
  });

  it("is single-flighted, so two callers share one registration", async () => {
    let registrations = 0;
    server.use(
      http.post("/api/v1/users/me/mls/device", async ({ request }) => {
        registrations += 1;
        return passThroughDeviceRegistration(request);
      }),
    );
    const service = newService();
    const [first, second] = await Promise.all([service.start(), service.start()]);
    expect(first && second).toBe(true);
    expect(registrations).toBe(1);
  });

  it("restores the stored device rather than minting a second one", async () => {
    const keystore = await Keystore.open(memoryStore());
    const first = newService(keystore);
    await first.start();

    const second = newService(keystore);
    expect(await second.start()).toBe(true);
    // The identity came back from the keystore: a fresh device would have
    // registered a second signature key with the directory.
    expect(latest().device).toEqual({ status: "ready" });
  });

  it("is unavailable, not broken, when the directory refuses the device", async () => {
    server.use(
      http.post("/api/v1/users/me/mls/device", () =>
        HttpResponse.json(
          { error: { code: "internal_error", message: "no." } },
          { status: 500 },
        ),
      ),
    );
    const service = newService();
    expect(await service.start()).toBe(false);
    expect(latest().device).toEqual({ status: "unavailable", reason: "server" });
  });

  it("is unavailable when the wrapper cannot be loaded", async () => {
    setMlsModule(null); // no override and no built artifact: the import fails
    const service = newService();
    expect(await service.start()).toBe(false);
    expect(latest().device).toEqual({ status: "unavailable", reason: "wasm" });
  });
});

describe("opening a channel", () => {
  it("creates the group and adds every member who has a device", async () => {
    for (const userId of OTHERS) {
      seedDevice(userId);
    }
    const channelId = await createE2eeChannel("secrets");

    const service = newService();
    await service.openChannel(channelId);

    expect(latest().channels[channelId]).toEqual({ status: "ready" });
    const group = mockMlsGroup(channelId);
    expect(group?.epoch).toBe(1);
    // One commit for all three, with a Welcome stored for each device.
    expect(group?.commits).toHaveLength(1);
    expect(mockMlsWelcomes()).toHaveLength(OTHERS.length);
  });

  it("says who cannot be added yet rather than pretending", async () => {
    // A device with an empty pool, and two people with no device at all.
    seedDevice(CHAT_USERS.nasrin.id, 0);
    const channelId = await createE2eeChannel("half-reachable");

    const service = newService();
    await service.openChannel(channelId);

    const channel = latest().channels[channelId];
    expect(channel?.status).toBe("incomplete");
    expect(channel?.status === "incomplete" ? channel.unreachableUserIds.sort() : []).toEqual(
      [...OTHERS].sort(),
    );
  });

  it("waits to be added when another member won the create race", async () => {
    const channelId = await createE2eeChannel("contested");
    server.use(
      http.post("/api/v1/channels/:channelId/mls/group", () =>
        HttpResponse.json(
          { error: { code: "mls_group_exists", message: "already." } },
          { status: 409 },
        ),
      ),
    );

    const service = newService();
    await service.openChannel(channelId);
    expect(latest().channels[channelId]).toEqual({ status: "waiting" });
  });

  it("waits when the group exists and this device is not in it", async () => {
    const channelId = await createE2eeChannel("existing");
    // Somebody else's group, which this device holds no state for.
    const other = newService();
    await other.start();
    await api.POST("/api/v1/channels/{channelId}/mls/group", {
      params: { path: { channelId } },
      body: { group_id: toBase64(new Uint8Array([1, 2, 3])) },
    });

    const service = newService();
    await service.openChannel(channelId);
    expect(latest().channels[channelId]).toEqual({ status: "waiting" });
  });

  it("retries once the epoch moves under it", async () => {
    seedDevice(CHAT_USERS.nasrin.id, 2);
    const channelId = await createE2eeChannel("racy");

    let refusals = 0;
    server.use(
      http.post("/api/v1/channels/:channelId/mls/commits", async ({ request, params }) => {
        if (refusals === 0) {
          refusals += 1;
          return HttpResponse.json(
            { error: { code: "mls_epoch_conflict", message: "too late." } },
            { status: 409 },
          );
        }
        return passThroughCommit(request, params.channelId as string);
      }),
    );

    const service = newService();
    await service.openChannel(channelId);

    expect(refusals).toBe(1);
    expect(latest().channels[channelId]?.status).toBe("incomplete");
    expect(mockMlsGroup(channelId)?.epoch).toBe(1);
  });

  it("fails honestly when the group cannot be read", async () => {
    const channelId = await createE2eeChannel("unreadable");
    server.use(
      http.get("/api/v1/channels/:channelId/mls/group", () =>
        HttpResponse.json(
          { error: { code: "internal_error", message: "no." } },
          { status: 500 },
        ),
      ),
    );

    const service = newService();
    await service.openChannel(channelId);
    expect(latest().channels[channelId]).toEqual({ status: "failed" });
  });
});

describe("welcomes", () => {
  it("joins a welcome addressed to this device and acknowledges it", async () => {
    const channelId = await createE2eeChannel("welcomed");
    const service = newService();
    await service.start();
    const ourDevice = mockMlsDevices().find((device) => device.userId === ME);
    expect(ourDevice).toBeDefined();

    // Somebody else's group, and a commit that adds us — the shape the
    // contract stores when another member's client wins the create race.
    await api.POST("/api/v1/channels/{channelId}/mls/group", {
      params: { path: { channelId } },
      body: { group_id: toBase64(new Uint8Array([7])) },
    });
    await api.POST("/api/v1/channels/{channelId}/mls/commits", {
      params: { path: { channelId } },
      body: {
        epoch: 0,
        message: toBase64(encoder.encode(`commit:7:${ME}|${fakeSignatureKey(ME)}`)),
        welcomes: [
          {
            device_ids: [ourDevice?.id ?? ""],
            welcome: toBase64(encoder.encode(`welcome:7:${ME}|${fakeSignatureKey(ME)}`)),
          },
        ],
      },
    });

    // Seeded because a joined device now reconciles before it reports ready:
    // the Welcome puts it into a tree somebody else assembled, and `ready`
    // enables the composer, so the sweep that ADR 007's guarantee is stated
    // in terms of has to run before the first message rather than after it.
    // Without devices to add, this channel would honestly report `incomplete`.
    for (const userId of OTHERS) {
      seedDevice(userId);
    }

    // Captured before the join, because the reconcile that now follows it
    // adds the other members and mints welcomes for them: asserting an empty
    // table afterwards would be asserting that reconcile did nothing.
    const ours = mockMlsWelcomes().map((welcome) => welcome.id);
    expect(ours).toHaveLength(1);

    await service.syncWelcomes();

    expect(latest().channels[channelId]).toEqual({ status: "ready" });
    // Acknowledged only after the join succeeded.
    const remaining = new Set(mockMlsWelcomes().map((welcome) => welcome.id));
    expect(ours.filter((id) => remaining.has(id))).toHaveLength(0);
  });

  it("leaves a welcome for a sibling device alone", async () => {
    const channelId = await createE2eeChannel("sibling");
    // Left on the default directory key on purpose: a sibling is a *second*
    // device, so it must not collide with the key this session registers —
    // the directory is idempotent on it, and a collision would hand us the
    // sibling's id as our own.
    const sibling = seedMockMlsDevice(ME, [toBase64(fakeKeyPackage(ME))]);
    const service = newService();
    await service.start();

    // A Welcome addressed to the sibling, not to us.
    await api.POST("/api/v1/channels/{channelId}/mls/group", {
      params: { path: { channelId } },
      body: { group_id: toBase64(new Uint8Array([9])) },
    });
    await api.POST("/api/v1/channels/{channelId}/mls/commits", {
      params: { path: { channelId } },
      body: {
        epoch: 0,
        message: toBase64(new TextEncoder().encode("commit:9:x")),
        welcomes: [
          {
            device_ids: [sibling.id],
            welcome: toBase64(new TextEncoder().encode("welcome:9:x")),
          },
        ],
      },
    });

    await service.syncWelcomes();
    // Still there: acknowledging it would delete it out from under the
    // device that can actually open it.
    expect(mockMlsWelcomes()).toHaveLength(1);
  });
});

describe("messages", () => {
  it("encrypts and decrypts, and reports the epoch it sealed at", async () => {
    seedDevice(CHAT_USERS.nasrin.id);
    const channelId = await createE2eeChannel("chatty");
    const service = newService();
    await service.openChannel(channelId);

    const sealed = await service.encrypt(channelId, "سلام");
    expect(sealed).not.toBeNull();
    expect(sealed?.epoch).toBe(1);

    await service.decrypt(channelId, "m-1", sealed?.ciphertext ?? "");
    expect(latest().decrypted["m-1"]).toBe("سلام");
  });

  it("refuses to encrypt for a channel it has no group for", async () => {
    const service = newService();
    await service.start();
    expect(await service.encrypt("00000000-0000-4000-8000-00000000ffff", "hi")).toBeNull();
  });

  it("records null for a message it cannot open, and never throws", async () => {
    seedDevice(CHAT_USERS.nasrin.id);
    const channelId = await createE2eeChannel("older");
    const service = newService();
    await service.openChannel(channelId);

    await service.decrypt(channelId, "m-old", toBase64(new TextEncoder().encode("garbage")));
    expect(latest().decrypted["m-old"]).toBeNull();
  });

  it("keeps the plaintext of what this device sent", async () => {
    seedDevice(CHAT_USERS.nasrin.id);
    const channelId = await createE2eeChannel("mine");
    const service = newService();
    await service.openChannel(channelId);

    await service.rememberSent("m-2", "what I said");
    expect(latest().decrypted["m-2"]).toBe("what I said");
  });
});

describe("a direct message", () => {
  /**
   * A DM is a channel of two, and the service treats it as one — there is no
   * DM-shaped branch anywhere in it. This proves that in the only way that
   * counts: the group is created, the peer is added, and a message written on
   * one side opens on the other.
   */
  it("bootstraps its group and carries a message to the other side", async () => {
    // Not Parisa: the fixture already holds a plaintext DM with her, and
    // get-or-create would hand that one back — which is the rule the last
    // case in this group pins down.
    const peerId = CHAT_USERS.nasrin.id;
    // The peer's own device, in the same world, with a key package in the
    // directory for us to claim.
    const peer = fakeMlsModule(world).create(peerId);
    seedDevice(peerId);

    const channelId = await openE2eeDm(peerId);
    const service = newService();
    await service.openChannel(channelId);

    const group = mockMlsGroup(channelId);
    expect(group?.epoch).toBe(1);

    // The Welcome the commit carried, joined by the peer exactly as its own
    // client would after the mls_welcome nudge.
    const welcome = mockMlsWelcomes().find((entry) => entry.channel_id === channelId);
    expect(welcome).toBeDefined();
    peer.join_from_welcome(fromBase64(welcome?.welcome ?? ""));

    const sealed = await service.encrypt(channelId, "just between us");
    expect(sealed).not.toBeNull();
    const groupId = fromBase64(group?.groupId ?? "");
    expect(peer.decrypt(groupId, fromBase64(sealed?.ciphertext ?? ""))).toBe("just between us");
  });

  it("is refused, not quietly encrypted, when the flag fights a strict org", async () => {
    // ADR 011 decision 1: the mode decides what a conversation is born as, and
    // a request that disagrees is refused rather than coerced — silently
    // handing back the opposite of what was asked for is how an immutable
    // surprise is manufactured.
    const { data, error } = await api.POST("/api/v1/dms", {
      body: { user_id: CHAT_USERS.omid.id, e2ee: false },
    });
    expect(data).toBeUndefined();
    expect(error?.error.code).toBe("e2ee_required_by_org");
  });

  it("opens as plaintext under a compliance org", async () => {
    // The only way a plaintext conversation is born now. Not reachable through
    // the API until Compliance unlocks, so it is set directly — the rule has
    // to be right the day it can be.
    setMockEncryptionMode("compliance");
    const { data } = await api.POST("/api/v1/dms", {
      body: { user_id: CHAT_USERS.omid.id, e2ee: false },
    });
    expect(data?.e2ee).toBe(false);
  });

  it("keeps an existing conversation as it was opened", async () => {
    const peerId = CHAT_USERS.omid.id;
    const first = await openE2eeDm(peerId);
    // Get-or-create is idempotent, and a flag cannot re-decide a conversation
    // that already exists (openapi.yaml -> OpenDirectMessageRequest.e2ee).
    const { data } = await api.POST("/api/v1/dms", {
      body: { user_id: peerId, e2ee: false },
    });
    expect(data?.id).toBe(first);
    expect(data?.e2ee).toBe(true);
  });
});

describe("membership events", () => {
  const STAYS = [ME, CHAT_USERS.omid.id, CHAT_USERS.parisa.id];

  it("evicts the leaves of a member the directory no longer lists", async () => {
    seedDevice(CHAT_USERS.nasrin.id);
    const channelId = await createE2eeChannel("leavers");
    const service = newService();
    await service.openChannel(channelId);
    expect(mockMlsGroup(channelId)?.epoch).toBe(1);
    expect(leavesOf(channelId)).toHaveLength(2);

    // What a removal looks like from here: the roster answer stops carrying
    // her, and her key stops being allowed.
    server.use(
      http.get("/api/v1/channels/:channelId/mls/member-devices", () => directoryPage(STAYS)),
    );
    await service.memberRemoved(channelId);

    expect(mockMlsGroup(channelId)?.epoch).toBe(2);
    expect(leavesOf(channelId)).toEqual([`${ME}|${fakeSignatureKey(ME)}`]);
  });

  it("evicts a leaf credentialed under a member who is staying", async () => {
    // The regression this slice exists for (ADR 007). Omid publishes a key
    // package whose credential says "Nasrin" — a member who is staying — so
    // no removal that selects on the credential can ever name it: it is not
    // the departing user's, and it is never stale. Its key is Omid's, and
    // once he is off the roster the directory lists it for nobody.
    seedDevice(CHAT_USERS.nasrin.id);
    seedMockMlsDevice(
      CHAT_USERS.omid.id,
      [toBase64(fakeKeyPackage(CHAT_USERS.nasrin.id, fakeSignatureKey(CHAT_USERS.omid.id)))],
      directoryKey(CHAT_USERS.omid.id),
    );
    const channelId = await createE2eeChannel("planted");
    const service = newService();
    await service.openChannel(channelId);

    // Three leaves, two of them claiming to be Nasrin.
    expect(leavesOf(channelId)).toEqual([
      `${ME}|${fakeSignatureKey(ME)}`,
      `${CHAT_USERS.nasrin.id}|${fakeSignatureKey(CHAT_USERS.nasrin.id)}`,
      `${CHAT_USERS.nasrin.id}|${fakeSignatureKey(CHAT_USERS.omid.id)}`,
    ]);

    server.use(
      http.get("/api/v1/channels/:channelId/mls/member-devices", () =>
        directoryPage([ME, CHAT_USERS.nasrin.id, CHAT_USERS.parisa.id]),
      ),
    );
    await service.memberRemoved(channelId);

    // The plant is gone and Nasrin's real leaf stayed: the sweep read the
    // key, not the name they share.
    expect(leavesOf(channelId)).toEqual([
      `${ME}|${fakeSignatureKey(ME)}`,
      `${CHAT_USERS.nasrin.id}|${fakeSignatureKey(CHAT_USERS.nasrin.id)}`,
    ]);
  });

  it("sweeps nothing when the roster could not be read whole", async () => {
    seedDevice(CHAT_USERS.nasrin.id);
    const channelId = await createE2eeChannel("half-read");
    const service = newService();
    await service.openChannel(channelId);
    const before = leavesOf(channelId);
    expect(before).toHaveLength(2);

    // A first page without Nasrin and a second page that never arrives. A
    // client that swept on what it had would evict her — she is a member,
    // her key is simply on the page it never read.
    server.use(
      http.get("/api/v1/channels/:channelId/mls/member-devices", ({ request }) => {
        if (new URL(request.url).searchParams.get("cursor") === null) {
          return HttpResponse.json({ members: [], next_cursor: "page-2" });
        }
        return HttpResponse.json(
          { error: { code: "internal_error", message: "no." } },
          { status: 500 },
        );
      }),
    );
    await service.syncChannel(channelId);

    expect(latest().channels[channelId]).toEqual({ status: "failed" });
    expect(mockMlsGroup(channelId)?.epoch).toBe(1);
    expect(leavesOf(channelId)).toEqual(before);
  });

  it("does not evict a member's peers because that member has no devices", async () => {
    // Omid and Parisa are members with nothing registered, so they arrive as
    // entries with an empty key list. An implementation that treated "no
    // keys" as anything other than "contributes nothing to the allow-list"
    // would take Nasrin's leaf with them.
    seedDevice(CHAT_USERS.nasrin.id);
    const channelId = await createE2eeChannel("device-less");
    const service = newService();
    await service.openChannel(channelId);
    const before = leavesOf(channelId);

    await service.syncChannel(channelId);

    expect(mockMlsGroup(channelId)?.epoch).toBe(1);
    expect(leavesOf(channelId)).toEqual(before);
  });

  it("does nothing when somebody else already swept", async () => {
    const channelId = await createE2eeChannel("already-gone");
    const service = newService();
    await service.openChannel(channelId);
    const before = mockMlsGroup(channelId)?.epoch;

    await service.memberRemoved(channelId);
    expect(mockMlsGroup(channelId)?.epoch).toBe(before);
  });
});

/* ── verification (ADR 008) ────────────────────────────────────────────── */

const NASRIN = CHAT_USERS.nasrin.id;
const OMID = CHAT_USERS.omid.id;

/**
 * A key the directory hands out that no honest device ever generated, as a
 * LEAF carries it — a bare string, the way `fakeModule` writes one.
 */
const PLANTED_LEAF = "sig:planted";

/** The same key as the directory carries it: base64. */
const PLANTED = toBase64(encoder.encode(PLANTED_LEAF));

/** The directory's claim about somebody, as a second registered device. */
function plantDirectoryKey(userId: string, key = PLANTED) {
  return seedMockMlsDevice(userId, [], key);
}

/** Opens a channel with Nasrin in it and returns the settled service. */
async function openWith(slug: string, users: readonly string[] = [NASRIN]) {
  for (const userId of users) {
    seedDevice(userId);
  }
  const channelId = await createE2eeChannel(slug);
  const service = newService();
  await service.openChannel(channelId);
  return { service, channelId };
}

describe("pinning and the changed-key state", () => {
  it("pins every member on first sight, silently", async () => {
    const { channelId } = await openWith("first-sight");

    // No question was asked and none should have been: first sight is the one
    // moment with nothing to compare against, so a prompt would be noise.
    expect(latest().records[NASRIN]).toMatchObject({
      userId: NASRIN,
      keys: [directoryKey(NASRIN)],
      level: "pinned",
    });
    expect(latest().verification[channelId]).toEqual({ changed: [], uncoveredLeaves: 0 });
  });

  it("reports a changed member while the channel itself stays ready", async () => {
    // Everybody has a device, so the channel reaches `ready` rather than
    // `incomplete` — which is what makes the point here: `ready` and blocked
    // are not the same axis.
    const { service, channelId } = await openWith("swapped", OTHERS);
    expect(latest().channels[channelId]).toEqual({ status: "ready" });
    expect(await service.encrypt(channelId, "before")).not.toBeNull();

    plantDirectoryKey(NASRIN);
    await service.syncChannel(channelId);

    // The two states are held alongside each other on purpose: availability
    // says the group is usable, verification says it is not safe to add to.
    expect(latest().channels[channelId]).toEqual({ status: "ready" });
    expect(latest().verification[channelId]).toEqual({
      changed: [{ userId: NASRIN, kind: "newDevice", added: [PLANTED], removed: [] }],
      uncoveredLeaves: 0,
    });
  });

  it("calls a withdrawn key a replaced key, not a new device", async () => {
    const { service, channelId } = await openWith("replaced");
    // The directory drops her real device and offers another in its place.
    server.use(
      http.get("/api/v1/channels/:channelId/mls/member-devices", () =>
        HttpResponse.json({
          members: [
            { user_id: ME, signature_public_keys: [directoryKey(ME)] },
            { user_id: NASRIN, signature_public_keys: [PLANTED] },
          ],
        }),
      ),
    );
    await service.syncChannel(channelId);

    expect(latest().verification[channelId]?.changed).toEqual([
      { userId: NASRIN, kind: "replacedKey", added: [PLANTED], removed: [directoryKey(NASRIN)] },
    ]);
  });
});

describe("the send gate", () => {
  it("refuses to encrypt while a member's keys are unaccepted", async () => {
    const { service, channelId } = await openWith("blocked");
    plantDirectoryKey(NASRIN);
    await service.syncChannel(channelId);

    expect(await service.encrypt(channelId, "to whoever that is")).toBeNull();
  });

  it("keeps reading and decrypting while sending is blocked", async () => {
    const { service, channelId } = await openWith("read-on");
    const sent = await service.encrypt(channelId, "still readable");
    expect(sent).not.toBeNull();

    plantDirectoryKey(NASRIN);
    await service.syncChannel(channelId);
    expect(await service.encrypt(channelId, "nope")).toBeNull();

    // Blocking reads would protect nothing and would hide the context a human
    // needs in order to judge the warning.
    await service.decrypt(channelId, "m1", sent?.ciphertext ?? "");
    expect(latest().decrypted.m1).toBe("still readable");
  });

  it("keeps sweeping while sending is blocked", async () => {
    const { service, channelId } = await openWith("sweep-on", [NASRIN, OMID]);
    expect(leavesOf(channelId)).toHaveLength(3);

    plantDirectoryKey(NASRIN);
    await service.syncChannel(channelId);
    expect(await service.encrypt(channelId, "nope")).toBeNull();

    // Omid leaves. The sweep is itself a defence and must not pause for a
    // warning, so his leaf goes even though this channel is blocked.
    server.use(
      http.get("/api/v1/channels/:channelId/mls/member-devices", () =>
        HttpResponse.json({
          members: [
            { user_id: ME, signature_public_keys: [directoryKey(ME)] },
            { user_id: NASRIN, signature_public_keys: [directoryKey(NASRIN), PLANTED] },
          ],
        }),
      ),
    );
    await service.memberRemoved(channelId);

    expect(leavesOf(channelId)).toEqual([
      `${ME}|${fakeSignatureKey(ME)}`,
      `${NASRIN}|${fakeSignatureKey(NASRIN)}`,
    ]);
    expect(await service.encrypt(channelId, "still nope")).toBeNull();
  });

  it("blocks on a leaf the directory attributes to nobody, then heals when the sweep evicts it", async () => {
    const { service, channelId } = await openWith("uncovered");

    // Omid registers, so the next reconcile has an add to make — and `addUsers`
    // catches up on the log AFTER the sweep has already run. That is the
    // window ADR 007 names, and this is it being a non-sending window.
    seedDevice(OMID);
    // One catch-up for `syncChannel`, one inside the sweep, and a third
    // inside `addUsers` — that third one is the only commit this pass applies
    // after the sweep is already behind it.
    let calls = 0;
    server.use(
      http.get("/api/v1/channels/:channelId/mls/commits", ({ params, request }) => {
        calls += 1;
        if (calls === 3) {
          // Somebody else's commit lands between our sweep and our adds,
          // carrying a leaf whose key the directory lists for no member.
          injectCommit(params.channelId as string, [
            `${ME}|${fakeSignatureKey(ME)}`,
            `${NASRIN}|${fakeSignatureKey(NASRIN)}`,
            "ghost|sig:ghost",
          ]);
        }
        return passThroughCommitPage(params.channelId as string, request);
      }),
    );
    await service.syncChannel(channelId);

    expect(latest().verification[channelId]?.uncoveredLeaves).toBe(1);
    expect(await service.encrypt(channelId, "not into that")).toBeNull();

    // Nothing was asked of anybody: the next reconcile's allow-list sweep is
    // what resolves this branch.
    server.resetHandlers();
    await service.syncChannel(channelId);

    expect(latest().verification[channelId]?.uncoveredLeaves).toBe(0);
    expect(await service.encrypt(channelId, "fine now")).not.toBeNull();
  });
});

/* ── the media gate's timing (ADR 009, decision 3) ─────────────────────── */

describe("the epoch and the gate that guards it", () => {
  /**
   * The published epoch and the published verification state have to reach an
   * observer TOGETHER.
   *
   * The call layer rotates its outbound key to whatever `epochs` last said and
   * withholds publishing on whatever `verification` last said, and ADR 009
   * decision 3 puts the check before the outbound slot switches. Published in
   * two updates, `catchUp` announces the new epoch while the gate still reads
   * the previous, clear state — so between the two, a device seals audio under
   * an epoch a leaf it has never accepted is already a member of, for as long
   * as the directory round trip takes.
   *
   * Which is why this asserts over the whole SEQUENCE of published states
   * rather than over the last one. The last one is correct either way: the
   * reconcile that follows re-reads the directory and closes the gate. Only
   * the states in between can tell a single update from two.
   */
  it("never shows an advanced epoch beside a clear gate", async () => {
    const { service, channelId } = await openWith("rotation-gate");
    const settled = latest().epochs[channelId] ?? 0;
    expect(settled).toBe(1);
    expect(needsAttention(latest().verification[channelId])).toBe(false);

    // Somebody else's commit lands, carrying a second leaf under Nasrin's id
    // whose key she never published. The directory is made to vouch for it in
    // the same breath, which is what stops the allow-list sweep from evicting
    // it: this stays a decision for a human rather than a window that heals
    // itself, so the gate is still closed at the end and a test that looked
    // only at the end would see nothing wrong in either version.
    plantDirectoryKey(NASRIN);
    injectCommit(channelId, [
      `${ME}|${fakeSignatureKey(ME)}`,
      `${NASRIN}|${fakeSignatureKey(NASRIN)}`,
      `${NASRIN}|${PLANTED_LEAF}`,
    ]);
    await service.syncChannel(channelId);

    const advanced = states.filter((state) => (state.epochs[channelId] ?? 0) > settled);
    // Without this the loop below is vacuously true — a service that stopped
    // publishing epochs at all would pass it.
    expect(advanced.length, "no published state ever carried the new epoch").toBeGreaterThan(0);
    for (const state of advanced) {
      expect(
        needsAttention(state.verification[channelId]),
        `epoch ${String(state.epochs[channelId])} was published while the gate still read clear`,
      ).toBe(true);
    }
  });
});

describe("the two exits", () => {
  async function blocked(slug: string) {
    const opened = await openWith(slug);
    plantDirectoryKey(NASRIN);
    await opened.service.syncChannel(opened.channelId);
    expect(await opened.service.encrypt(opened.channelId, "blocked")).toBeNull();
    return opened;
  }

  it("accepting re-pins the current set and unblocks sending", async () => {
    const { service, channelId } = await blocked("accepted");

    await service.acceptPeer(NASRIN);

    expect(latest().records[NASRIN]).toMatchObject({
      keys: [directoryKey(NASRIN), PLANTED],
      level: "pinned",
    });
    expect(latest().verification[channelId]).toEqual({ changed: [], uncoveredLeaves: 0 });
    expect(await service.encrypt(channelId, "on we go")).not.toBeNull();
  });

  it("verifying records the ceremony's verdict and unblocks sending", async () => {
    const { service, channelId } = await blocked("verified");

    await service.verifyPeer(NASRIN);

    expect(latest().records[NASRIN]).toMatchObject({
      keys: [directoryKey(NASRIN), PLANTED],
      level: "verified",
    });
    expect(await service.encrypt(channelId, "compared out of band")).not.toBeNull();
  });

  it("downgrades a verified peer to pinned when accepted without a ceremony", async () => {
    const { service } = await blocked("downgraded");
    await service.verifyPeer(NASRIN);
    expect(latest().records[NASRIN]?.level).toBe("verified");

    await service.acceptPeer(NASRIN);

    // An unceremonied acceptance is a pin, not a proof, and the badge has to
    // say so rather than keeping a claim nobody re-earned.
    expect(latest().records[NASRIN]?.level).toBe("pinned");
  });

  it("accepts only the set it was shown, so the next change warns again", async () => {
    const { service, channelId } = await blocked("named-set");
    await service.acceptPeer(NASRIN);

    plantDirectoryKey(NASRIN, toBase64(encoder.encode("sig:planted-again")));
    await service.syncChannel(channelId);

    expect(latest().verification[channelId]?.changed).toHaveLength(1);
    expect(await service.encrypt(channelId, "no")).toBeNull();
  });
});

describe("the own-account prompt", () => {
  it("raises the prompt rather than pinning a new key under this account", async () => {
    const { service, channelId } = await openWith("my-devices");
    expect(latest().ownDevices).toBeNull();

    // Either my other browser profile or an attack. No software on this
    // device can tell, so nothing here decides it quietly.
    const mine = toBase64(encoder.encode("sig:my-other-browser"));
    plantDirectoryKey(ME, mine);
    await service.syncChannel(channelId);

    expect(latest().ownDevices).toEqual({ keys: [mine] });
    expect(latest().records[ME]).toBeUndefined();
    expect(await service.encrypt(channelId, "who else is reading this")).toBeNull();

    await service.acceptOwnDevices();

    expect(latest().ownDevices).toBeNull();
    expect(latest().records[ME]).toMatchObject({ keys: [directoryKey(ME), mine] });
    expect(await service.encrypt(channelId, "mine after all")).not.toBeNull();
  });
});

describe("safety numbers", () => {
  it("is sixty digits and the same for a settled pair", async () => {
    const { service } = await openWith("digits");
    expect(await service.safetyNumberFor(NASRIN)).toMatch(/^(\d{5} ){11}\d{5}$/);
  });

  it("moves when the directory changes what it claims about the peer", async () => {
    const { service, channelId } = await openWith("peer-half");
    const before = await service.safetyNumberFor(NASRIN);

    plantDirectoryKey(NASRIN);
    await service.syncChannel(channelId);

    expect(await service.safetyNumberFor(NASRIN)).not.toBe(before);
  });

  it("computes our own half from our own device key, never from the directory", async () => {
    const { service, channelId } = await openWith("own-half");
    const before = await service.safetyNumberFor(NASRIN);

    // The hostile directory's best move, and the reason this test exists: it
    // claims an extra device for US. Nasrin's screen reads us from the
    // directory and so includes the plant. If this client did the same, both
    // screens would print one identical number, the humans would say "they
    // match", and the ceremony would bless the attack — with nothing thrown
    // and no other test failing.
    plantDirectoryKey(ME);
    await service.syncChannel(channelId);

    expect(await service.safetyNumberFor(NASRIN)).toBe(before);
  });

  it("mismatches exactly when a key is planted, where a circular one would match", async () => {
    // The same argument as the test above, written as the two screens, so the
    // property is legible without a service in the way.
    const alice = { userId: "alice", keys: ["QUxJQ0U="] };
    const bob = { userId: "bob", keys: ["Qk9C"] };
    const bobAsClaimed = { userId: "bob", keys: [...bob.keys, PLANTED] };

    // A circular implementation: every half from the directory. Both people
    // see the same two sets, so both print the same line.
    expect(await safetyNumber(alice, bobAsClaimed)).toBe(await safetyNumber(bobAsClaimed, alice));

    // This one: each side's own half comes from its own device key. Alice
    // sees Bob as the directory claims him; Bob sees himself as he is.
    expect(await safetyNumber(alice, bobAsClaimed)).not.toBe(await safetyNumber(bob, alice));
  });

  it("has no number for somebody no reconcile has ever seen", async () => {
    const { service } = await openWith("stranger");
    expect(await service.safetyNumberFor("00000000-0000-4000-8000-000000000999")).toBeNull();
  });
});

describe("records at rest", () => {
  it("carries pins and verdicts across a restart", async () => {
    const keystore = await Keystore.open(memoryStore());
    seedDevice(NASRIN);
    const channelId = await createE2eeChannel("restart");
    const first = newService(keystore);
    await first.openChannel(channelId);
    await first.verifyPeer(NASRIN);

    const second = newService(keystore);
    await second.start();

    expect(latest().records[NASRIN]).toMatchObject({ level: "verified" });
  });
});

describe("the key-package pool", () => {
  /**
   * Counts pool publishes from here on.
   *
   * It answers the way the default handler does for everything the client
   * reads — replace-all, and the count it hands back is the batch it was given
   * — and skips only the mock device's own bookkeeping, which no case in this
   * group claims against.
   */
  function countPublishes(): () => number {
    let publishes = 0;
    server.use(
      http.put("/api/v1/users/me/mls/devices/:deviceId/key-packages", async ({ request }) => {
        publishes += 1;
        const body = (await request.json()) as { key_packages: string[] };
        return HttpResponse.json({ unclaimed_count: body.key_packages.length });
      }),
    );
    return () => publishes;
  }

  it("publishes on the first start of a device that has never published", async () => {
    const publishes = countPublishes();
    const service = newService(await Keystore.open(memoryStore()));
    expect(await service.start()).toBe(true);
    expect(publishes()).toBe(1);
  });

  it("publishes nothing on a restart while the pool is above the mark", async () => {
    const keystore = await Keystore.open(memoryStore());
    // The first start fills the pool through the ordinary handler: twenty.
    await newService(keystore).start();

    const publishes = countPublishes();
    const second = newService(keystore);
    expect(await second.start()).toBe(true);
    // The whole point of the slice: a reload is not a reason to throw away
    // twenty unclaimed packages and mint twenty more.
    expect(publishes()).toBe(0);
  });

  it("still publishes nothing one package above the mark", async () => {
    const keystore = await Keystore.open(memoryStore());
    await newService(keystore).start();
    // KEY_PACKAGE_LOW_WATER + 1: the last count that is not low.
    await keystore?.saveKeyPackageCount(6);

    const publishes = countPublishes();
    await newService(keystore).start();
    expect(publishes()).toBe(0);
  });

  it("replenishes once the pool has fallen to the mark", async () => {
    const keystore = await Keystore.open(memoryStore());
    await newService(keystore).start();
    // KEY_PACKAGE_LOW_WATER exactly — at the mark counts as low.
    await keystore?.saveKeyPackageCount(5);

    const publishes = countPublishes();
    const second = newService(keystore);
    expect(await second.start()).toBe(true);
    expect(publishes()).toBe(1);
    // And the new count is recorded, so the *next* start publishes nothing.
    expect(await keystore?.loadKeyPackageCount()).toBe(20);
  });

  it("counts a welcome as one package spent", async () => {
    const keystore = await Keystore.open(memoryStore());
    const channelId = await createE2eeChannel("spent");
    const service = newService(keystore);
    await service.start();
    expect(await keystore?.loadKeyPackageCount()).toBe(20);

    const ourDevice = mockMlsDevices().find((device) => device.userId === ME);
    await api.POST("/api/v1/channels/{channelId}/mls/group", {
      params: { path: { channelId } },
      body: { group_id: toBase64(new Uint8Array([7])) },
    });
    await api.POST("/api/v1/channels/{channelId}/mls/commits", {
      params: { path: { channelId } },
      body: {
        epoch: 0,
        message: toBase64(encoder.encode(`commit:7:${ME}|${fakeSignatureKey(ME)}`)),
        welcomes: [
          {
            device_ids: [ourDevice?.id ?? ""],
            welcome: toBase64(encoder.encode(`welcome:7:${ME}|${fakeSignatureKey(ME)}`)),
          },
        ],
      },
    });
    for (const userId of OTHERS) {
      seedDevice(userId);
    }

    await service.syncWelcomes();

    // Nothing reports a claim, so a Welcome addressed to this device is the
    // only evidence the client ever gets that one of its packages was used.
    expect(await keystore?.loadKeyPackageCount()).toBe(19);
  });

  it("replenishes mid-session when welcomes drain the pool to the mark", async () => {
    const keystore = await Keystore.open(memoryStore());
    const channelId = await createE2eeChannel("drained");
    const service = newService(keystore);
    await service.start();
    // One above the mark, so the single welcome below takes it to it.
    await keystore?.saveKeyPackageCount(6);

    const ourDevice = mockMlsDevices().find((device) => device.userId === ME);
    await api.POST("/api/v1/channels/{channelId}/mls/group", {
      params: { path: { channelId } },
      body: { group_id: toBase64(new Uint8Array([7])) },
    });
    await api.POST("/api/v1/channels/{channelId}/mls/commits", {
      params: { path: { channelId } },
      body: {
        epoch: 0,
        message: toBase64(encoder.encode(`commit:7:${ME}|${fakeSignatureKey(ME)}`)),
        welcomes: [
          {
            device_ids: [ourDevice?.id ?? ""],
            welcome: toBase64(encoder.encode(`welcome:7:${ME}|${fakeSignatureKey(ME)}`)),
          },
        ],
      },
    });
    for (const userId of OTHERS) {
      seedDevice(userId);
    }

    // Reloaded from the store, because `saveKeyPackageCount` above wrote past
    // the running service's own copy.
    const reloaded = newService(keystore);
    await reloaded.start();
    const publishes = countPublishes();
    await reloaded.syncWelcomes();

    // A tab open for days must not sit at zero waiting for a restart: at that
    // point every other member's client reports it as unaddable.
    expect(publishes()).toBe(1);
  });
});

describe("own-message history at rest", () => {
  const DAY = 24 * 60 * 60 * 1000;

  it("shows the author their own words again after a reload", async () => {
    const keystore = await Keystore.open(memoryStore());
    seedDevice(NASRIN);
    const channelId = await createE2eeChannel("my-words");
    const first = newService(keystore);
    await first.openChannel(channelId);
    await first.rememberSent("m-1", "what I said");

    // The reload: a brand-new service over the same profile's keystore, which
    // is exactly what a fresh tab is. MLS itself cannot help here — the sender
    // ratchet only produces — so anything that comes back came from the store.
    const second = newService(keystore);
    expect(await second.start()).toBe(true);
    expect(latest().decrypted["m-1"]).toBe("what I said");
  });

  it("replaces an edited message rather than keeping both versions", async () => {
    const keystore = await Keystore.open(memoryStore());
    const service = newService(keystore);
    await service.start();
    await service.rememberSent("m-1", "first draft");
    await service.rememberSent("m-1", "second draft");

    expect(await keystore?.loadSent()).toEqual([
      { id: "m-1", text: "second draft", at: expect.any(Number) as number },
    ]);
  });

  it("keeps the newest 500 and drops the oldest first", async () => {
    const keystore = await Keystore.open(memoryStore());
    const service = newService(keystore);
    await service.start();
    // One past the bound, so exactly one entry has to go and there is no
    // ambiguity about which.
    for (let index = 0; index <= 500; index += 1) {
      await service.rememberSent(`m-${String(index)}`, `line ${String(index)}`);
    }

    const stored = (await keystore?.loadSent()) ?? [];
    expect(stored).toHaveLength(500);
    expect(stored[0]?.id).toBe("m-1");
    expect(stored.at(-1)?.id).toBe("m-500");
  });

  it("drops what has aged out, and drops it from the store and not just the screen", async () => {
    const keystore = await Keystore.open(memoryStore());
    const now = Date.now();
    await keystore?.saveSent([
      { id: "ancient", text: "said last month", at: now - 31 * DAY },
      { id: "recent", text: "said yesterday", at: now - DAY },
    ]);

    const service = newService(keystore);
    await service.start();
    expect(latest().decrypted.recent).toBe("said yesterday");
    expect(latest().decrypted.ancient).toBeUndefined();

    // The bound is a statement about what exists at rest, so the expired entry
    // is written out rather than merely filtered on the way to the screen: a
    // profile that sits closed for two months must not still be holding
    // two-month-old plaintext the moment it opens.
    expect(await keystore?.loadSent()).toEqual([
      { id: "recent", text: "said yesterday", at: now - DAY },
    ]);
  });

  it("degrades to no history when the store will not open, and never throws", async () => {
    const store = memoryStore();
    const keystore = await Keystore.open(store);
    await keystore?.saveSent([{ id: "m-1", text: "what I said", at: Date.now() }]);
    // A history encrypted under a key this profile no longer has.
    await store.put("sent", { iv: new Uint8Array(12), ciphertext: new Uint8Array(32) });

    seedDevice(NASRIN);
    const channelId = await createE2eeChannel("lost-words");
    const service = newService(keystore);
    expect(await service.start()).toBe(true);
    expect(latest().decrypted["m-1"]).toBeUndefined();

    // And the bubble then says the true thing rather than an empty one: MLS
    // cannot open what this device sent, the store no longer can either, so
    // undecryptable is the honest state and it is what renders.
    await service.openChannel(channelId);
    await service.decrypt(channelId, "m-1", toBase64(encoder.encode("unopenable")));
    expect(latest().decrypted["m-1"]).toBeNull();
  });
});

/* ── helpers that re-enter the default handlers ─────────────────────────
 * `server.use` replaces a handler outright, so a case that only wants to
 * interfere once has to do the ordinary thing itself for the other calls.
 */

async function passThroughDeviceRegistration(request: Request) {
  const body = (await request.json()) as { signature_public_key: string };
  const device = seedMockMlsDevice(ME, []);
  device.signaturePublicKey = body.signature_public_key;
  return HttpResponse.json(
    {
      id: device.id,
      signature_public_key: device.signaturePublicKey,
      created_at: "2026-08-29T09:00:00.000Z",
    },
    { status: 201 },
  );
}

/**
 * Appends a commit to the log as another member's client would have, replacing
 * the group's leaves wholesale — the shape `fakeModule` writes and reads.
 *
 * The mock group's epoch moves with it, so the client that catches up on this
 * commit stays in step with the server and its own next commit is not a
 * conflict. A test that only stuffed bytes into the log would be testing the
 * retry loop instead of what it meant to.
 */
function injectCommit(channelId: string, leaves: readonly string[]): void {
  const group = mockMlsGroup(channelId);
  if (group === undefined) {
    throw new Error("no mock group to inject a commit into");
  }
  const groupId = Array.from(fromBase64(group.groupId)).join(",");
  group.epoch += 1;
  group.commits.push({
    epoch: group.epoch,
    message: toBase64(encoder.encode(`commit:${groupId}:${leaves.join(",")}`)),
    created_at: new Date().toISOString(),
  });
}

/** The commit page the default handler would have returned. */
function passThroughCommitPage(channelId: string, request: Request) {
  const group = mockMlsGroup(channelId);
  const url = new URL(request.url);
  const after = Number(url.searchParams.get("after_epoch") ?? "0");
  const limit = Number(url.searchParams.get("limit") ?? "50");
  return HttpResponse.json({
    commits: (group?.commits ?? []).filter((commit) => commit.epoch > after).slice(0, limit),
  });
}

async function passThroughCommit(request: Request, channelId: string) {
  const body = (await request.json()) as {
    epoch: number;
    message: string;
    welcomes?: { device_ids: string[]; welcome: string }[];
  };
  const group = mockMlsGroup(channelId);
  if (group === undefined) {
    return HttpResponse.json(
      { error: { code: "channel_not_found", message: "no." } },
      { status: 404 },
    );
  }
  if (body.epoch !== group.epoch) {
    return HttpResponse.json(
      { error: { code: "mls_epoch_conflict", message: "too late." } },
      { status: 409 },
    );
  }
  group.epoch += 1;
  group.commits.push({
    epoch: group.epoch,
    message: body.message,
    created_at: new Date().toISOString(),
  });
  return new HttpResponse(null, { status: 201 });
}
