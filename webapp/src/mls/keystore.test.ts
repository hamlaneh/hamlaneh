import { describe, expect, it } from "vitest";

import { Keystore, memoryStore } from "./keystore";

/**
 * The wrapping, not IndexedDB. What is worth proving here is that a stored
 * export is ciphertext, that it comes back byte-identical, and that the key
 * cannot be read out of the store — all of which are true of the in-memory
 * store and the browser one alike.
 */

const STATE = new TextEncoder().encode("pretend-this-is-a-signature-private-key");

/**
 * Compared as plain arrays: under jsdom, WebCrypto hands back buffers from
 * node's realm, and two byte-identical Uint8Arrays from different realms are
 * not `toEqual`. The bytes are what this file is about.
 */
function bytes(value: Uint8Array | null | undefined): number[] {
  return value === null || value === undefined ? [] : Array.from(value);
}

describe("Keystore", () => {
  it("round-trips a device-state export", async () => {
    const store = memoryStore();
    const keystore = await Keystore.open(store);
    expect(keystore).not.toBeNull();

    expect(await keystore?.save(STATE)).toBe(true);
    expect(bytes(await keystore?.load())).toEqual(bytes(STATE));
  });

  it("stores ciphertext, never the state itself", async () => {
    const store = memoryStore();
    const keystore = await Keystore.open(store);
    await keystore?.save(STATE);

    const record = (await store.get("state")) as { iv: Uint8Array; ciphertext: Uint8Array };
    const stored = record.ciphertext;
    expect(bytes(stored)).not.toEqual(bytes(STATE));
    // AES-GCM: the tag makes it longer than the plaintext, and no window of
    // the plaintext survives anywhere in it.
    expect(stored.length).toBeGreaterThan(STATE.length);
    expect(new TextDecoder().decode(stored)).not.toContain("signature");
    expect(record.iv).toHaveLength(12);
  });

  it("uses a fresh iv per write", async () => {
    const store = memoryStore();
    const keystore = await Keystore.open(store);
    await keystore?.save(STATE);
    const first = (await store.get("state")) as { iv: Uint8Array };
    const firstIv = new Uint8Array(first.iv);
    await keystore?.save(STATE);
    const second = (await store.get("state")) as { iv: Uint8Array };
    expect(new Uint8Array(second.iv)).not.toEqual(firstIv);
  });

  it("generates the wrapping key non-extractable", async () => {
    const store = memoryStore();
    await Keystore.open(store);
    const key = (await store.get("wrapping-key")) as CryptoKey;
    expect(key.extractable).toBe(false);
    // Which is what makes the claim in the module docs true rather than
    // aspirational: the bytes cannot be read out, here or by anything else
    // holding this handle.
    await expect(crypto.subtle.exportKey("raw", key)).rejects.toThrow();
  });

  it("reuses the wrapping key across opens, so state survives a reload", async () => {
    const store = memoryStore();
    const first = await Keystore.open(store);
    await first?.save(STATE);

    const second = await Keystore.open(store);
    expect(bytes(await second?.load())).toEqual(bytes(STATE));
  });

  it("reports no state rather than throwing when the record will not decrypt", async () => {
    const store = memoryStore();
    const keystore = await Keystore.open(store);
    await keystore?.save(STATE);
    // The shape of a record encrypted under a key this profile no longer has.
    await store.put("state", { iv: new Uint8Array(12), ciphertext: new Uint8Array(32) });
    expect(await keystore?.load()).toBeNull();
  });

  it("reports no state on a record of the wrong shape", async () => {
    const store = memoryStore();
    const keystore = await Keystore.open(store);
    await store.put("state", { nonsense: true });
    expect(await keystore?.load()).toBeNull();
  });

  it("clears the state and keeps the key", async () => {
    const store = memoryStore();
    const keystore = await Keystore.open(store);
    await keystore?.save(STATE);
    await keystore?.clear();
    expect(await keystore?.load()).toBeNull();
    expect(await store.get("wrapping-key")).toBeDefined();
  });

  it("round-trips verification records, wrapped like the state", async () => {
    const store = memoryStore();
    const keystore = await Keystore.open(store);
    const records = {
      nasrin: { userId: "nasrin", keys: ["AQID"], level: "verified" as const, at: 1_700_000_000 },
    };
    expect(await keystore?.saveRecords(records)).toBe(true);
    expect(await keystore?.loadRecords()).toEqual(records);

    // Under the same non-extractable key, in its own slot: what is on disk
    // must not spell out who this device trusts.
    const stored = (await store.get("verification")) as { ciphertext: Uint8Array };
    expect(new TextDecoder().decode(stored.ciphertext)).not.toContain("nasrin");
  });

  it("has no records before anything is written", async () => {
    const keystore = await Keystore.open(memoryStore());
    expect(await keystore?.loadRecords()).toEqual({});
  });

  it("drops a malformed record rather than inventing a level for it", async () => {
    const store = memoryStore();
    const keystore = await Keystore.open(store);
    await keystore?.saveRecords({
      good: { userId: "good", keys: ["AQID"], level: "pinned", at: 1 },
      // Every one of these decides a security badge, so a record that fails
      // any of them is dropped: no record re-pins and warns, which is the safe
      // direction. A half-read one would draw a level nobody recorded.
      mismatched: { userId: "somebody-else", keys: [], level: "verified", at: 1 },
      unleveled: { userId: "unleveled", keys: [], level: "trusted", at: 1 },
      keyless: { userId: "keyless", level: "verified", at: 1 },
    } as never);

    expect(await keystore?.loadRecords()).toEqual({
      good: { userId: "good", keys: ["AQID"], level: "pinned", at: 1 },
    });
  });

  it("reports no records rather than throwing when they will not decrypt", async () => {
    const store = memoryStore();
    const keystore = await Keystore.open(store);
    await keystore?.saveRecords({
      nasrin: { userId: "nasrin", keys: [], level: "verified", at: 1 },
    });
    await store.put("verification", { iv: new Uint8Array(12), ciphertext: new Uint8Array(32) });
    expect(await keystore?.loadRecords()).toEqual({});
  });

  it("clears the records with the state, so the next user inherits no trust", async () => {
    const store = memoryStore();
    const keystore = await Keystore.open(store);
    await keystore?.save(STATE);
    await keystore?.saveRecords({
      nasrin: { userId: "nasrin", keys: ["AQID"], level: "verified", at: 1 },
    });
    await keystore?.clear();
    expect(await keystore?.loadRecords()).toEqual({});
  });

  it("is unavailable rather than unsafe when the store refuses", async () => {
    const broken = {
      get: () => Promise.reject(new Error("no storage here")),
      put: () => Promise.reject(new Error("no storage here")),
      delete: () => Promise.resolve(),
    };
    expect(await Keystore.open(broken)).toBeNull();
  });

  it("reports a failed write instead of throwing into the caller", async () => {
    const store = memoryStore();
    // Opened first so the wrapping key exists, then reopened over a store
    // whose writes fail: the save path, not the setup path.
    await Keystore.open(store);
    const stubborn = await Keystore.open({
      ...store,
      put: () => Promise.reject(new Error("quota exceeded")),
    });
    expect(stubborn).not.toBeNull();
    expect(await stubborn?.save(STATE)).toBe(false);
  });
});
