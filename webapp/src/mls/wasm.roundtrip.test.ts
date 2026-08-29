import { existsSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { packFrames, unpackFrames, unpackStrings } from "./bytes";
import type { MlsDeviceHandle } from "./wasm";

/**
 * The real OpenMLS wrapper, exercised end to end.
 *
 * These are the only tests that touch actual cryptography. Everything else
 * about MLS in this app is orchestration, tested against a double; this file
 * exists to prove that the double is not lying about the shape of the thing —
 * that `hamlaneh-mls.d.ts` matches the crate, that a Welcome carries its own
 * ratchet tree, that an export really does restore a working device.
 *
 * They need `webapp/src-mls/pkg-node/`, which `src-mls/build.sh` produces.
 * When it is missing the whole file skips with a banner: CI always builds the
 * crate, so a skip there means a broken workflow rather than a missing
 * artifact, and locally it means "you have not built it yet" rather than a
 * failure you did not cause.
 */

const here = path.dirname(fileURLToPath(import.meta.url));
const packageDir = path.resolve(here, "../../src-mls/pkg-node");
const entry = path.join(packageDir, "hamlaneh_mls.js");
const built = existsSync(entry);

if (!built) {
  const banner = "═".repeat(72);
  console.warn(
    [
      "",
      banner,
      "  SKIPPING the MLS round-trip tests: webapp/src-mls/pkg-node is absent.",
      "",
      "  Nothing here has been verified against real OpenMLS. Build it with:",
      "      webapp/src-mls/build.sh",
      "  CI always builds the crate, so a skip there is a broken workflow.",
      banner,
      "",
    ].join("\n"),
  );
}

interface WasmModule {
  MlsDevice: {
    create: (identity: string) => MlsDeviceHandle;
    restore: (state: Uint8Array) => MlsDeviceHandle;
  };
}

function loadModule(): WasmModule {
  return createRequire(import.meta.url)(entry) as WasmModule;
}

const GROUP_ID = new TextEncoder().encode("channel-under-test");

describe.skipIf(!built)("the OpenMLS wrapper", () => {
  it("carries a group from creation through a two-way conversation", () => {
    const { MlsDevice } = loadModule();
    const alice = MlsDevice.create("user-alice");
    const bob = MlsDevice.create("user-bob");

    alice.create_group(GROUP_ID);
    expect(alice.epoch(GROUP_ID)).toBe(0n);
    expect(unpackStrings(alice.member_identities(GROUP_ID))).toEqual(["user-alice"]);

    const keyPackages = unpackFrames(bob.generate_key_packages(3));
    expect(keyPackages).toHaveLength(3);

    const bundle = alice.add_members(GROUP_ID, packFrames([keyPackages[0] ?? new Uint8Array()]));
    expect(bundle.commit).toBeDefined();
    expect(bundle.welcome).toBeDefined();
    // Not merged yet: the server has not accepted it.
    expect(alice.epoch(GROUP_ID)).toBe(0n);

    alice.commit_accepted(GROUP_ID);
    expect(alice.epoch(GROUP_ID)).toBe(1n);

    // The Welcome is self-contained — no ratchet tree travels separately.
    const joined = bob.join_from_welcome(bundle.welcome ?? new Uint8Array());
    expect(new TextDecoder().decode(joined)).toBe("channel-under-test");
    expect(bob.epoch(GROUP_ID)).toBe(1n);

    const persian = "سلام دنیا 👋";
    const sealed = alice.encrypt(GROUP_ID, persian);
    expect(sealed.epoch).toBe(1n);
    expect(bob.decrypt(GROUP_ID, sealed.ciphertext)).toBe(persian);

    const back = bob.encrypt(GROUP_ID, "and back again");
    expect(alice.decrypt(GROUP_ID, back.ciphertext)).toBe("and back again");
  });

  it("restores a working device from an export — the reload case", () => {
    const { MlsDevice } = loadModule();
    const alice = MlsDevice.create("user-alice");
    const bob = MlsDevice.create("user-bob");
    alice.create_group(GROUP_ID);
    const keyPackages = unpackFrames(bob.generate_key_packages(1));
    const bundle = alice.add_members(GROUP_ID, packFrames(keyPackages));
    alice.commit_accepted(GROUP_ID);
    bob.join_from_welcome(bundle.welcome ?? new Uint8Array());

    const exported = alice.export_state();
    const restored = MlsDevice.restore(exported);

    expect(restored.identity).toBe("user-alice");
    expect(unpackFrames(restored.group_ids())).toHaveLength(1);
    expect(restored.epoch(GROUP_ID)).toBe(1n);

    // Sealed *after* the export, so this proves the restored device holds
    // live secrets rather than a snapshot that happens to look right.
    const later = bob.encrypt(GROUP_ID, "sent after the reload");
    expect(restored.decrypt(GROUP_ID, later.ciphertext)).toBe("sent after the reload");

    const outgoing = restored.encrypt(GROUP_ID, "and it can still send");
    expect(bob.decrypt(GROUP_ID, outgoing.ciphertext)).toBe("and it can still send");
  });

  it("skips a device that is already a member instead of adding it twice", () => {
    const { MlsDevice } = loadModule();
    const alice = MlsDevice.create("user-alice");
    const bob = MlsDevice.create("user-bob");
    alice.create_group(GROUP_ID);
    const keyPackages = unpackFrames(bob.generate_key_packages(2));

    alice.add_members(GROUP_ID, packFrames([keyPackages[0] ?? new Uint8Array()]));
    alice.commit_accepted(GROUP_ID);

    // A second key package of the same device: the race two clients run when
    // both notice the same newcomer.
    const again = alice.add_members(GROUP_ID, packFrames([keyPackages[1] ?? new Uint8Array()]));
    expect(again.commit).toBeUndefined();
    expect(alice.epoch(GROUP_ID)).toBe(1n);
  });

  it("drops a rejected commit and leaves the epoch where it was", () => {
    const { MlsDevice } = loadModule();
    const alice = MlsDevice.create("user-alice");
    const bob = MlsDevice.create("user-bob");
    alice.create_group(GROUP_ID);

    alice.add_members(GROUP_ID, packFrames(unpackFrames(bob.generate_key_packages(1))));
    alice.commit_rejected(GROUP_ID);

    expect(alice.epoch(GROUP_ID)).toBe(0n);
    expect(unpackStrings(alice.member_identities(GROUP_ID))).toEqual(["user-alice"]);
  });

  it("applies another member's commit from the log", () => {
    const { MlsDevice } = loadModule();
    const alice = MlsDevice.create("user-alice");
    const bob = MlsDevice.create("user-bob");
    const carol = MlsDevice.create("user-carol");
    alice.create_group(GROUP_ID);

    const first = alice.add_members(
      GROUP_ID,
      packFrames(unpackFrames(bob.generate_key_packages(1))),
    );
    alice.commit_accepted(GROUP_ID);
    bob.join_from_welcome(first.welcome ?? new Uint8Array());

    // Bob adds Carol; Alice learns about it only from the commit log.
    const second = bob.add_members(
      GROUP_ID,
      packFrames(unpackFrames(carol.generate_key_packages(1))),
    );
    bob.commit_accepted(GROUP_ID);
    alice.apply_commit(GROUP_ID, second.commit ?? new Uint8Array());

    expect(alice.epoch(GROUP_ID)).toBe(2n);
    expect(unpackStrings(alice.member_identities(GROUP_ID)).sort()).toEqual([
      "user-alice",
      "user-bob",
      "user-carol",
    ]);

    carol.join_from_welcome(second.welcome ?? new Uint8Array());
    const message = alice.encrypt(GROUP_ID, "everyone can read this");
    expect(carol.decrypt(GROUP_ID, message.ciphertext)).toBe("everyone can read this");
  });

  it("evicts every leaf outside the allow-list, in one commit", () => {
    const { MlsDevice } = loadModule();
    const alice = MlsDevice.create("user-alice");
    const bobPhone = MlsDevice.create("user-bob");
    const bobLaptop = MlsDevice.create("user-bob");
    alice.create_group(GROUP_ID);

    alice.add_members(
      GROUP_ID,
      packFrames([
        ...unpackFrames(bobPhone.generate_key_packages(1)),
        ...unpackFrames(bobLaptop.generate_key_packages(1)),
      ]),
    );
    alice.commit_accepted(GROUP_ID);
    expect(unpackStrings(alice.member_identities(GROUP_ID))).toHaveLength(3);
    expect(unpackFrames(alice.member_signature_keys(GROUP_ID))).toHaveLength(3);

    // Nobody's key is missing from this list, so there is nothing to evict —
    // including Alice's own, which the sweep must not treat as a target.
    expect(
      alice.retain_leaves(
        GROUP_ID,
        packFrames([
          bobPhone.signature_public_key(),
          bobLaptop.signature_public_key(),
        ]),
      ).commit,
    ).toBeUndefined();

    const sweep = alice.retain_leaves(GROUP_ID, packFrames([alice.signature_public_key()]));
    expect(sweep.commit).toBeDefined();
    alice.commit_accepted(GROUP_ID);
    // Both of Bob's leaves went in one commit, selected by key rather than by
    // the name they happened to carry.
    expect(unpackStrings(alice.member_identities(GROUP_ID))).toEqual(["user-alice"]);

    expect(
      alice.retain_leaves(GROUP_ID, packFrames([alice.signature_public_key()])).commit,
    ).toBeUndefined();
  });

  it("evicts a leaf credentialed under a staying member's id", () => {
    // The regression this slice exists for (ADR 007). Mallory enrols a device
    // whose credential says "user-bob" — a member who is staying — so no
    // removal that selects on the credential identity can ever name it: it is
    // not the departing user's, and it is never stale.
    const { MlsDevice } = loadModule();
    const alice = MlsDevice.create("user-alice");
    const bob = MlsDevice.create("user-bob");
    const mallory = MlsDevice.create("user-bob");
    alice.create_group(GROUP_ID);

    alice.add_members(
      GROUP_ID,
      packFrames([
        ...unpackFrames(bob.generate_key_packages(1)),
        ...unpackFrames(mallory.generate_key_packages(1)),
      ]),
    );
    alice.commit_accepted(GROUP_ID);

    // Three leaves, two of them claiming to be Bob, and no way to tell them
    // apart by name — which is the whole problem.
    expect(unpackStrings(alice.member_identities(GROUP_ID))).toEqual([
      "user-alice",
      "user-bob",
      "user-bob",
    ]);

    // The directory maps Bob to Bob's key and knows nothing of Mallory's, so
    // the allow-list is those two keys. Nothing here consults a credential.
    const allowed = packFrames([alice.signature_public_key(), bob.signature_public_key()]);
    const sweep = alice.retain_leaves(GROUP_ID, allowed);
    expect(sweep.commit).toBeDefined();
    alice.commit_accepted(GROUP_ID);

    const keys = unpackFrames(alice.member_signature_keys(GROUP_ID));
    expect(keys).toHaveLength(2);
    expect(keys.map(String)).not.toContain(String(mallory.signature_public_key()));
    expect(unpackStrings(alice.member_identities(GROUP_ID))).toEqual([
      "user-alice",
      "user-bob",
    ]);
  });

  it("shuts a planted leaf out of the epochs after the sweep", () => {
    // The same plant, carried through to the property that matters: not that
    // the leaf is absent from a list, but that the device behind it cannot
    // read what is said next.
    const { MlsDevice } = loadModule();
    const alice = MlsDevice.create("user-alice");
    const bob = MlsDevice.create("user-bob");
    const mallory = MlsDevice.create("user-bob");
    alice.create_group(GROUP_ID);

    const added = alice.add_members(
      GROUP_ID,
      packFrames([
        ...unpackFrames(bob.generate_key_packages(1)),
        ...unpackFrames(mallory.generate_key_packages(1)),
      ]),
    );
    alice.commit_accepted(GROUP_ID);
    bob.join_from_welcome(added.welcome ?? new Uint8Array());
    mallory.join_from_welcome(added.welcome ?? new Uint8Array());

    // Before the sweep the plant works: this is the hole, demonstrated.
    const before = alice.encrypt(GROUP_ID, "while the plant is still in");
    expect(mallory.decrypt(GROUP_ID, before.ciphertext)).toBe("while the plant is still in");

    const sweep = alice.retain_leaves(
      GROUP_ID,
      packFrames([alice.signature_public_key(), bob.signature_public_key()]),
    );
    alice.commit_accepted(GROUP_ID);
    bob.apply_commit(GROUP_ID, sweep.commit ?? new Uint8Array());

    const after = alice.encrypt(GROUP_ID, "after the sweep");
    expect(bob.decrypt(GROUP_ID, after.ciphertext)).toBe("after the sweep");
    expect(() => mallory.decrypt(GROUP_ID, after.ciphertext)).toThrow();
  });

  it("refuses a message from before this device joined", () => {
    const { MlsDevice } = loadModule();
    const alice = MlsDevice.create("user-alice");
    const bob = MlsDevice.create("user-bob");
    const carol = MlsDevice.create("user-carol");
    alice.create_group(GROUP_ID);

    const first = alice.add_members(
      GROUP_ID,
      packFrames(unpackFrames(bob.generate_key_packages(1))),
    );
    alice.commit_accepted(GROUP_ID);
    bob.join_from_welcome(first.welcome ?? new Uint8Array());
    const early = alice.encrypt(GROUP_ID, "before carol arrived");

    const second = alice.add_members(
      GROUP_ID,
      packFrames(unpackFrames(carol.generate_key_packages(1))),
    );
    alice.commit_accepted(GROUP_ID);
    carol.join_from_welcome(second.welcome ?? new Uint8Array());

    // The condition the "cannot be decrypted" bubble renders, and the reason
    // it is a state rather than an error.
    expect(() => carol.decrypt(GROUP_ID, early.ciphertext)).toThrow();
  });

  it("reports an unusable state instead of restoring half a device", () => {
    const { MlsDevice } = loadModule();
    const alice = MlsDevice.create("user-alice");
    alice.create_group(GROUP_ID);
    const exported = alice.export_state();

    expect(() => MlsDevice.restore(exported.slice(0, exported.length - 1))).toThrow();
    expect(() => MlsDevice.restore(new Uint8Array())).toThrow();
  });
});
