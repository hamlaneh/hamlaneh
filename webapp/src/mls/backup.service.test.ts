import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";

import { api } from "../api/client";
import { CHAT_USERS } from "../mocks/chat";
import { resetMockAuth } from "../mocks/handlers";
import { mockMlsBackup, seedMockMlsBackup, seedMockMlsDevice } from "../mocks/mls";
import { server } from "../mocks/node";
import {
  deriveBackupKey,
  formatRecoveryKey,
  parseRecoveryKey,
  sealBackup,
  unsealBackup,
} from "./backup";
import { fromBase64, toBase64 } from "./bytes";
import { fakeKeyPackage, fakeMlsModule, fakeSignatureKey, fakeWorld } from "./fakeModule";
import { Keystore, memoryStore } from "./keystore";
import { MlsService } from "./service";
import type { MlsState } from "./types";
import { setMlsModule } from "./wasm";

/**
 * The backup, driven through the service against the contract mocks
 * (ADR 010).
 *
 * The two that matter most are at the bottom. One proves the floor refuses a
 * rolled-back envelope; the other proves the number being compared is the one
 * sealed inside the header and never the copy the server mirrors back in JSON
 * — which is the difference between a control and a formality, and cannot be
 * seen by reading the happy path.
 */

const ME = CHAT_USERS.me.id;
const NASRIN = CHAT_USERS.nasrin.id;

const encoder = new TextEncoder();

/** The fixed recovery key the planted-envelope tests seal with. */
const FIXED_KEY = new Uint8Array(Array.from({ length: 32 }, (_, index) => index + 1));

let world = fakeWorld();
let states: MlsState[] = [];

function directoryKey(identity: string): string {
  return toBase64(encoder.encode(fakeSignatureKey(identity)));
}

function seedDevice(userId: string) {
  return seedMockMlsDevice(userId, [toBase64(fakeKeyPackage(userId))], directoryKey(userId));
}

async function newService() {
  states = [];
  const keystore = await Keystore.open(memoryStore());
  if (keystore === null) {
    throw new Error("this environment cannot open a keystore");
  }
  return {
    service: new MlsService({
      currentUserId: ME,
      onChange: (state) => {
        states.push(state);
      },
      keystore,
    }),
    keystore,
  };
}

function latest(): MlsState {
  const state = states.at(-1);
  if (state === undefined) {
    throw new Error("the service published no state");
  }
  return state;
}

async function createE2eeChannel(slug: string): Promise<string> {
  const { data } = await api.POST("/api/v1/channels", {
    body: { slug, kind: "private", e2ee: true },
  });
  if (data === undefined) {
    throw new Error("the fixture channel could not be created");
  }
  return data.id;
}

/** Plants an envelope the client never uploaded — the lying server. */
async function plantEnvelope(sealedCounter: number, mirroredCounter = sealedCounter) {
  const envelope = await sealBackup(await deriveBackupKey(FIXED_KEY), ME, sealedCounter, {
    v: 1,
    createdAt: "2026-03-03T09:15:00.000Z",
    verificationRecords: {
      [NASRIN]: {
        userId: NASRIN,
        keys: [directoryKey(NASRIN)],
        level: "verified",
        at: 1_772_000_000_000,
      },
    },
  });
  seedMockMlsBackup({
    envelope: toBase64(envelope),
    counter: mirroredCounter,
    updatedAt: "2026-03-03T09:15:01.000Z",
  });
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});

beforeEach(() => {
  world = fakeWorld();
  setMlsModule(fakeMlsModule(world));
});

afterEach(() => {
  server.resetHandlers();
  server.events.removeAllListeners();
  resetMockAuth();
  setMlsModule(null);
});

afterAll(() => {
  server.close();
});

describe("the offer", () => {
  it("is offered rather than switched on, and uploads nothing until it is taken", async () => {
    const { service } = await newService();
    expect(await service.start()).toBe(true);

    // A backup sealed under a key the user never saw is unrestorable by
    // construction, so "on by default" is not honestly available (ADR 010,
    // decision 4).
    expect(latest().backup.status).toBe("offer");
    expect(mockMlsBackup()).toBeNull();
  });

  it("shows the key once, keeps only the handle, and uploads the records", async () => {
    const { service, keystore } = await newService();
    await service.start();

    const text = await service.enableBackup();
    if (text === null) {
      throw new Error("the ceremony showed no recovery key");
    }
    // What was shown is a real recovery key: it passes its own checksum.
    const bytes = await parseRecoveryKey(text);
    if (bytes === null) {
      throw new Error("the shown recovery key does not parse");
    }

    expect(latest().backup).toMatchObject({ status: "on", floor: 1, writeFailed: false });
    const stored = mockMlsBackup();
    expect(stored?.counter).toBe(1);

    // The handle survives; the string does not exist anywhere but the return
    // value the ceremony just showed.
    await expect(keystore.loadBackupKey()).resolves.not.toBeNull();
    const wrapped = JSON.stringify(await keystore.loadBackupState());
    expect(wrapped).not.toContain(text.slice(0, 8));

    // And the envelope really is this account's records under that key.
    const opened = await unsealBackup(
      await deriveBackupKey(bytes),
      fromBase64(stored?.envelope ?? ""),
    );
    expect(opened.counter).toBe(1);
    expect(opened.userId).toBe(ME);
  });

  it("records a decline and respects it on the next start", async () => {
    const { service, keystore } = await newService();
    await service.start();
    await service.declineBackup();
    expect(latest().backup.status).toBe("declined");

    // The same profile, a new session: the offer does not come back as a nag.
    states = [];
    const again = new MlsService({
      currentUserId: ME,
      onChange: (state) => {
        states.push(state);
      },
      keystore,
    });
    await again.start();
    expect(latest().backup.status).toBe("declined");
    expect(mockMlsBackup()).toBeNull();
  });

  it("turning it off deletes the blob and the handle but keeps the floor", async () => {
    const { service, keystore } = await newService();
    await service.start();
    await service.enableBackup();
    expect(mockMlsBackup()).not.toBeNull();

    await service.disableBackup();

    expect(mockMlsBackup()).toBeNull();
    await expect(keystore.loadBackupKey()).resolves.toBeNull();
    // The floor is not a feature toggle: resetting it would re-open the
    // rollback window turning backup off has nothing to do with.
    expect(latest().backup).toMatchObject({ status: "offer", floor: 1 });
  });
});

describe("write-behind", () => {
  it("re-seals and bumps the counter when the records change", async () => {
    seedDevice(NASRIN);
    const channelId = await createE2eeChannel("write-behind");
    const { service } = await newService();
    await service.start();
    await service.enableBackup();
    expect(mockMlsBackup()?.counter).toBe(1);

    // Opening the channel pins Nasrin on first sight, which is a records
    // change and therefore an upload.
    await service.openChannel(channelId);
    await service.whenBackupSettled();

    expect(mockMlsBackup()?.counter).toBe(2);
    expect(latest().backup).toMatchObject({ floor: 2, writeFailed: false });
  });

  it("reports a refused upload rather than moving the floor", async () => {
    seedDevice(NASRIN);
    const channelId = await createE2eeChannel("stale-write");
    const { service } = await newService();
    await service.start();
    await service.enableBackup();

    // Another of this account's own profiles got there first, so the next
    // upload does not advance and the server refuses it. ADR 010 decision 3
    // wants that visible rather than silently interleaved.
    seedMockMlsBackup({ envelope: "AAAA", counter: 99, updatedAt: "2026-03-03T00:00:00.000Z" });

    await service.openChannel(channelId);
    await service.whenBackupSettled();

    expect(latest().backup).toMatchObject({ floor: 1, writeFailed: true });
    // The refusal did not cost the trust decision: the record is on this
    // device whether or not its copy reached the server.
    expect(latest().records[NASRIN]).toBeDefined();
  });
});

describe("the floor", () => {
  it("refuses an envelope sealed below it", async () => {
    seedDevice(NASRIN);
    const channelId = await createE2eeChannel("rolled-back");
    const { service } = await newService();
    await service.start();
    await service.enableBackup();
    await service.openChannel(channelId);
    await service.whenBackupSettled();
    expect(latest().backup.floor).toBe(2);

    // The server now serves an older blob. It opens — it is genuinely this
    // account's, sealed under a key that works — and it is still refused,
    // because a device that has seen counter 2 never accepts 1 again.
    await plantEnvelope(1);
    const outcome = await service.openBackup(await formatRecoveryKey(FIXED_KEY));
    expect(outcome).toEqual({ status: "refused", reason: "rolledBack" });

    // Refused loudly rather than quietly ignored: nothing was applied and no
    // restore is left waiting for a confirmation.
    expect(latest().backup.pending).toBeNull();
  });

  it("checks the SEALED counter, never the server's mirrored copy", async () => {
    seedDevice(NASRIN);
    const channelId = await createE2eeChannel("mirrored-counter");
    const { service } = await newService();
    await service.start();
    await service.enableBackup();
    await service.openChannel(channelId);
    await service.whenBackupSettled();
    expect(latest().backup.floor).toBe(2);

    // The response a hostile server sends: a JSON counter far above the floor
    // beside an envelope sealed far below it. Believing the JSON would accept
    // the rollback; the JSON is not what is compared.
    await plantEnvelope(1, 999);
    expect(mockMlsBackup()?.counter).toBe(999);

    const outcome = await service.openBackup(await formatRecoveryKey(FIXED_KEY));
    expect(outcome).toEqual({ status: "refused", reason: "rolledBack" });
  });
});

describe("restore", () => {
  it("refuses a typo before it makes a request", async () => {
    let requests = 0;
    server.events.on("request:start", () => {
      requests += 1;
    });

    const { service } = await newService();
    const typo = (await formatRecoveryKey(FIXED_KEY)).replace(/.$/u, "Z");
    const outcome = await service.openBackup(typo);

    expect(outcome).toEqual({ status: "refused", reason: "badKey" });
    // Mistyping a character must not look like a server that has no backup,
    // and must not cost a round trip to find out.
    expect(requests).toBe(0);
  });

  it("says plainly when the server has nothing", async () => {
    const { service } = await newService();
    await service.start();

    const outcome = await service.openBackup(await formatRecoveryKey(FIXED_KEY));
    // A state a person can genuinely be in, and a distinct answer from a key
    // that did not work.
    expect(outcome).toEqual({ status: "refused", reason: "noBackup" });
  });

  it("refuses a key that does not open the envelope", async () => {
    await plantEnvelope(1);
    const { service } = await newService();
    await service.start();

    const other = new Uint8Array(32).fill(7);
    const outcome = await service.openBackup(await formatRecoveryKey(other));
    expect(outcome).toEqual({ status: "refused", reason: "wrongKey" });
  });

  it("opens for confirmation and applies only on the person's yes", async () => {
    await plantEnvelope(4);
    const { service, keystore } = await newService();
    await service.start();

    const outcome = await service.openBackup(await formatRecoveryKey(FIXED_KEY));
    expect(outcome).toEqual({
      status: "opened",
      restore: {
        createdAt: "2026-03-03T09:15:00.000Z",
        serverUpdatedAt: "2026-03-03T09:15:01.000Z",
        records: 1,
      },
    });
    // Nothing has landed yet: the sealed date is a fact for a human to
    // confirm, and confirming is what applies it.
    expect(latest().records).toEqual({});

    expect(await service.applyRestore()).toBe(true);
    expect(latest().records[NASRIN]).toMatchObject({ level: "verified" });
    // The floor moves to what was accepted, so this envelope can never be
    // replayed afterwards.
    expect(latest().backup.floor).toBe(4);
    // And the device keeps backing up under the key the person already holds.
    expect(latest().backup.status).toBe("on");
    await expect(keystore.loadBackupKey()).resolves.not.toBeNull();
  });

  it("drops the own row instead of restoring it", async () => {
    // The security content of the restore: a backup that carried "these are
    // my devices" would silently re-accept the key on the device that was
    // lost. It must instead raise the own-account prompt.
    const envelope = await sealBackup(await deriveBackupKey(FIXED_KEY), ME, 2, {
      v: 1,
      createdAt: "2026-03-03T09:15:00.000Z",
      verificationRecords: {
        [ME]: { userId: ME, keys: ["bG9zdC1kZXZpY2U="], level: "verified", at: 1 },
        [NASRIN]: { userId: NASRIN, keys: [directoryKey(NASRIN)], level: "pinned", at: 1 },
      },
    });
    seedMockMlsBackup({
      envelope: toBase64(envelope),
      counter: 2,
      updatedAt: "2026-03-03T09:15:01.000Z",
    });

    const { service } = await newService();
    await service.start();
    await service.openBackup(await formatRecoveryKey(FIXED_KEY));
    await service.applyRestore();

    expect(latest().records[ME]).toBeUndefined();
    expect(latest().records[NASRIN]).toBeDefined();
  });

  it("backing out changes nothing", async () => {
    await plantEnvelope(3);
    const { service } = await newService();
    await service.start();
    await service.openBackup(await formatRecoveryKey(FIXED_KEY));

    service.discardRestore();

    expect(latest().backup.pending).toBeNull();
    expect(latest().records).toEqual({});
    expect(await service.applyRestore()).toBe(false);
  });

  it("refuses to write over records this device already holds", async () => {
    seedDevice(NASRIN);
    const channelId = await createE2eeChannel("not-empty");
    const { service } = await newService();
    await service.start();
    await service.openChannel(channelId);
    expect(latest().records[NASRIN]).toBeDefined();

    await plantEnvelope(9);
    const outcome = await service.openBackup(await formatRecoveryKey(FIXED_KEY));
    // v1 does not merge; it refuses, which sidesteps merge semantics rather
    // than inventing them.
    expect(outcome).toEqual({ status: "refused", reason: "notEmpty" });
    expect(latest().records[NASRIN]).toBeDefined();
  });

  it("refuses an envelope sealed for another account", async () => {
    const envelope = await sealBackup(
      await deriveBackupKey(FIXED_KEY),
      NASRIN,
      1,
      { v: 1, createdAt: "2026-03-03T09:15:00.000Z", verificationRecords: {} },
    );
    seedMockMlsBackup({
      envelope: toBase64(envelope),
      counter: 1,
      updatedAt: "2026-03-03T09:15:01.000Z",
    });

    const { service } = await newService();
    await service.start();
    const outcome = await service.openBackup(await formatRecoveryKey(FIXED_KEY));
    expect(outcome).toEqual({ status: "refused", reason: "wrongAccount" });
  });
});
