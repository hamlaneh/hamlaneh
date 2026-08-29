import { http, HttpResponse } from "msw";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { api } from "../api/client";
import { CHAT_USERS } from "../mocks/chat";
import { resetMockAuth } from "../mocks/handlers";
import { mockMlsDevices, mockMlsGroup, mockMlsWelcomes, seedMockMlsDevice } from "../mocks/mls";
import { server } from "../mocks/node";
import { toBase64 } from "./bytes";
import { fakeMlsModule, fakeWorld } from "./fakeModule";
import { memoryStore, Keystore } from "./keystore";
import { MlsService } from "./service";
import type { MlsState } from "./types";
import { setMlsModule } from "./wasm";

/**
 * The service against the contract mocks, with the wasm wrapper replaced by a
 * double (see `fakeModule.ts`). These are tests about orchestration: who is
 * claimed, what is committed, what happens when the epoch moves, and what the
 * screen is told. Real MLS is proved in `wasm.roundtrip.test.ts`.
 */

const ME = CHAT_USERS.me.id;
const OTHERS = [CHAT_USERS.nasrin.id, CHAT_USERS.omid.id, CHAT_USERS.parisa.id];

/** A key package as the fake wrapper mints them, base64 for the directory. */
function fakeKeyPackage(userId: string): string {
  return toBase64(new TextEncoder().encode(`kp:${userId}`));
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

afterEach(() => {
  server.resetHandlers();
  resetMockAuth();
  setMlsModule(null);
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
  setMlsModule(fakeMlsModule(fakeWorld()));
});

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
      seedMockMlsDevice(userId, [fakeKeyPackage(userId)]);
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
    seedMockMlsDevice(CHAT_USERS.nasrin.id, []);
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
    seedMockMlsDevice(CHAT_USERS.nasrin.id, [
      fakeKeyPackage(CHAT_USERS.nasrin.id),
      fakeKeyPackage(CHAT_USERS.nasrin.id),
    ]);
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
        message: toBase64(new TextEncoder().encode(`commit:7:${ME}`)),
        welcomes: [
          {
            device_ids: [ourDevice?.id ?? ""],
            welcome: toBase64(new TextEncoder().encode(`welcome:7:${ME}`)),
          },
        ],
      },
    });

    await service.syncWelcomes();

    expect(latest().channels[channelId]).toEqual({ status: "ready" });
    // Acknowledged only after the join succeeded.
    expect(mockMlsWelcomes()).toHaveLength(0);
  });

  it("leaves a welcome for a sibling device alone", async () => {
    const channelId = await createE2eeChannel("sibling");
    const sibling = seedMockMlsDevice(ME, [fakeKeyPackage(ME)]);
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
    seedMockMlsDevice(CHAT_USERS.nasrin.id, [fakeKeyPackage(CHAT_USERS.nasrin.id)]);
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
    seedMockMlsDevice(CHAT_USERS.nasrin.id, [fakeKeyPackage(CHAT_USERS.nasrin.id)]);
    const channelId = await createE2eeChannel("older");
    const service = newService();
    await service.openChannel(channelId);

    await service.decrypt(channelId, "m-old", toBase64(new TextEncoder().encode("garbage")));
    expect(latest().decrypted["m-old"]).toBeNull();
  });

  it("keeps the plaintext of what this device sent", async () => {
    seedMockMlsDevice(CHAT_USERS.nasrin.id, [fakeKeyPackage(CHAT_USERS.nasrin.id)]);
    const channelId = await createE2eeChannel("mine");
    const service = newService();
    await service.openChannel(channelId);

    service.rememberSent("m-2", "what I said");
    expect(latest().decrypted["m-2"]).toBe("what I said");
  });
});

describe("membership events", () => {
  it("commits the removal of a departing member's devices", async () => {
    seedMockMlsDevice(CHAT_USERS.nasrin.id, [fakeKeyPackage(CHAT_USERS.nasrin.id)]);
    const channelId = await createE2eeChannel("leavers");
    const service = newService();
    await service.openChannel(channelId);
    expect(mockMlsGroup(channelId)?.epoch).toBe(1);

    await service.memberRemoved(channelId, CHAT_USERS.nasrin.id);
    expect(mockMlsGroup(channelId)?.epoch).toBe(2);
  });

  it("does nothing when somebody else already removed them", async () => {
    const channelId = await createE2eeChannel("already-gone");
    const service = newService();
    await service.openChannel(channelId);
    const before = mockMlsGroup(channelId)?.epoch;

    await service.memberRemoved(channelId, CHAT_USERS.parisa.id);
    expect(mockMlsGroup(channelId)?.epoch).toBe(before);
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
