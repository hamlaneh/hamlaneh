import { packFrames, unpackFrames } from "./bytes";
import type { CommitBundleHandle, EncryptedMessageHandle, MlsDeviceHandle, MlsModule } from "./wasm";

/**
 * A stand-in for the wasm wrapper, for tests about the *service*.
 *
 * It performs no cryptography and pretends to none: "ciphertext" is the
 * plaintext with a marker, and a "key package" is a pair of strings. What it
 * does reproduce faithfully is the state machine the service has to drive —
 * epochs that only advance on `commit_accepted`, a pending commit that must be
 * merged or cleared, eviction that selects on the signature key, and the
 * refusal to decrypt a message from an epoch this device was not in.
 *
 * A leaf here carries its claimed identity and its signature key as two
 * independent strings, and that separation is the whole reason this file can
 * be trusted about eviction: a double that derived one from the other could
 * not represent a leaf credentialed under somebody else's id, so a test
 * against it would pass whether the sweep selected by key or by name.
 *
 * Real MLS behaviour is proved against the real artifact in
 * `wasm.roundtrip.test.ts`. Mixing the two would make the service tests slow,
 * dependent on a build step, and no better at catching service bugs.
 */

const encoder = new TextEncoder();
const decoder = new TextDecoder();

const KEY_PACKAGE_PREFIX = "kp:";
const WELCOME_PREFIX = "welcome:";
const COMMIT_PREFIX = "commit:";
const MESSAGE_PREFIX = "msg:";

/**
 * A group's leaves: signature key → claimed identity.
 *
 * Keyed by the key because that is what a leaf cannot fake, and because it
 * gives this double the same dedupe the real `add_members` has.
 */
type FakeLeaves = Map<string, string>;

interface FakeGroup {
  epoch: number;
  members: FakeLeaves;
  pending: { members: FakeLeaves } | null;
  /** The epoch this device joined at; earlier messages stay unreadable. */
  joinedAtEpoch: number;
}

/** This world's signature key for a device enrolled under `identity`. */
export function fakeSignatureKey(identity: string): string {
  return `sig:${identity}`;
}

/**
 * A key package as this world mints them: a claimed identity and the key the
 * resulting leaf will carry.
 *
 * The two are separate arguments rather than one derived from the other so a
 * test can mint the package this whole slice exists for — one that claims a
 * current member's id while carrying a key the directory lists for nobody.
 */
export function fakeKeyPackage(identity: string, key = fakeSignatureKey(identity)): Uint8Array {
  return encoder.encode(`${KEY_PACKAGE_PREFIX}${leafOf(identity, key)}`);
}

/** A leaf on the wire: `identity|key`. */
function leafOf(identity: string, key: string): string {
  return `${identity}|${key}`;
}

function packLeaves(leaves: FakeLeaves): string {
  return [...leaves].map(([key, identity]) => leafOf(identity, key)).join(",");
}

function parseLeaves(packed: string): FakeLeaves {
  const leaves: FakeLeaves = new Map();
  for (const leaf of packed === "" ? [] : packed.split(",")) {
    const separator = leaf.indexOf("|");
    if (separator < 0) {
      // Strict on purpose. A double that quietly accepted a leaf with no key
      // would let a test pass against a service that never sent one.
      throw new Error(`not a leaf: ${leaf}`);
    }
    leaves.set(leaf.slice(separator + 1), leaf.slice(0, separator));
  }
  return leaves;
}

/** Shared across every device in one test, standing in for the wire. */
export interface FakeWorld {
  /**
   * Every device built in this world, by identity — the latest one under
   * that identity, since a restore builds a fresh object. A test reads it to
   * ask a device what it holds without going through the service.
   */
  devices: Map<string, FakeDevice>;
}

export function fakeWorld(): FakeWorld {
  return { devices: new Map() };
}

function key(groupId: Uint8Array): string {
  return Array.from(groupId).join(",");
}

class FakeDevice implements MlsDeviceHandle {
  readonly groups = new Map<string, FakeGroup>();

  /** This device's own leaf key — the one leaf it can never evict. */
  private readonly ownKey: string;

  constructor(
    readonly identity: string,
    world: FakeWorld,
  ) {
    this.ownKey = fakeSignatureKey(identity);
    world.devices.set(identity, this);
  }

  export_state(): Uint8Array {
    return encoder.encode(
      JSON.stringify({
        identity: this.identity,
        groups: [...this.groups].map(([id, group]) => [
          id,
          { ...group, members: [...group.members], pending: null },
        ]),
      }),
    );
  }

  signature_public_key(): Uint8Array {
    return encoder.encode(fakeSignatureKey(this.identity));
  }

  group_ids(): Uint8Array {
    return packFrames([...this.groups.keys()].map((id) => Uint8Array.from(id.split(",").map(Number))));
  }

  generate_key_packages(count: number): Uint8Array {
    return packFrames(Array.from({ length: count }, () => fakeKeyPackage(this.identity)));
  }

  create_group(groupId: Uint8Array): void {
    this.groups.set(key(groupId), {
      epoch: 0,
      members: new Map([[this.ownKey, this.identity]]),
      pending: null,
      joinedAtEpoch: 0,
    });
  }

  has_group(groupId: Uint8Array): boolean {
    return this.groups.has(key(groupId));
  }

  epoch(groupId: Uint8Array): bigint {
    return BigInt(this.require(groupId).epoch);
  }

  member_identities(groupId: Uint8Array): Uint8Array {
    return packFrames(
      [...this.require(groupId).members.values()].map((id) => encoder.encode(id)),
    );
  }

  member_signature_keys(groupId: Uint8Array): Uint8Array {
    return packFrames([...this.require(groupId).members.keys()].map((id) => encoder.encode(id)));
  }

  add_members(groupId: Uint8Array, packedKeyPackages: Uint8Array): CommitBundleHandle {
    const group = this.require(groupId);
    const added = parseLeaves(
      unpackFrames(packedKeyPackages)
        .map((item) => decoder.decode(item).slice(KEY_PACKAGE_PREFIX.length))
        .join(","),
    );
    // Skipped by key, as the real one does: two clients racing to add the
    // same newcomer is normal, and the loser must submit nothing.
    for (const existing of group.members.keys()) {
      added.delete(existing);
    }
    if (added.size === 0) {
      return { commit: undefined, welcome: undefined };
    }
    const members = new Map([...group.members, ...added]);
    group.pending = { members };
    return {
      commit: encoder.encode(`${COMMIT_PREFIX}${key(groupId)}:${packLeaves(members)}`),
      welcome: encoder.encode(`${WELCOME_PREFIX}${key(groupId)}:${packLeaves(members)}`),
    };
  }

  retain_leaves(groupId: Uint8Array, packedAllowedKeys: Uint8Array): CommitBundleHandle {
    const group = this.require(groupId);
    const allowed = new Set(unpackFrames(packedAllowedKeys).map((item) => decoder.decode(item)));
    const members = new Map(
      // Own leaf survives whatever the allow-list says: a group cannot evict
      // itself, which is the real wrapper's rule and not a convenience here.
      [...group.members].filter(([leafKey]) => allowed.has(leafKey) || leafKey === this.ownKey),
    );
    if (members.size === group.members.size) {
      return { commit: undefined, welcome: undefined };
    }
    group.pending = { members };
    return {
      commit: encoder.encode(`${COMMIT_PREFIX}${key(groupId)}:${packLeaves(members)}`),
      welcome: undefined,
    };
  }

  commit_accepted(groupId: Uint8Array): void {
    const group = this.require(groupId);
    if (group.pending === null) {
      throw new Error("no pending commit to merge");
    }
    group.members = group.pending.members;
    group.pending = null;
    group.epoch += 1;
  }

  commit_rejected(groupId: Uint8Array): void {
    this.require(groupId).pending = null;
  }

  apply_commit(groupId: Uint8Array, message: Uint8Array): void {
    const group = this.require(groupId);
    group.pending = null;
    const body = decoder.decode(message);
    if (!body.startsWith(COMMIT_PREFIX)) {
      throw new Error("not a commit");
    }
    group.members = parseLeaves(body.slice(`${COMMIT_PREFIX}${key(groupId)}:`.length));
    group.epoch += 1;
  }

  join_from_welcome(welcome: Uint8Array): Uint8Array {
    const body = decoder.decode(welcome);
    if (!body.startsWith(WELCOME_PREFIX)) {
      throw new Error("not a welcome");
    }
    const [id, members] = body.slice(WELCOME_PREFIX.length).split(":");
    const groupId = Uint8Array.from((id ?? "").split(",").map(Number));
    // Welcomes in this world always land at epoch 1, which is what an add
    // commit advances the group to.
    this.groups.set(key(groupId), {
      epoch: 1,
      members: parseLeaves(members ?? ""),
      pending: null,
      joinedAtEpoch: 1,
    });
    return groupId;
  }

  encrypt(groupId: Uint8Array, plaintext: string): EncryptedMessageHandle {
    const group = this.require(groupId);
    return {
      epoch: BigInt(group.epoch),
      ciphertext: encoder.encode(`${MESSAGE_PREFIX}${String(group.epoch)}:${plaintext}`),
    };
  }

  decrypt(groupId: Uint8Array, ciphertext: Uint8Array): string {
    const group = this.require(groupId);
    const body = decoder.decode(ciphertext);
    if (!body.startsWith(MESSAGE_PREFIX)) {
      throw new Error("not an application message");
    }
    const rest = body.slice(MESSAGE_PREFIX.length);
    const separator = rest.indexOf(":");
    const epoch = Number(rest.slice(0, separator));
    if (epoch < group.joinedAtEpoch) {
      // The real condition this stands in for: a message sealed before this
      // device had the group's secrets can never be opened by it.
      throw new Error("no secrets for that epoch");
    }
    return rest.slice(separator + 1);
  }

  private require(groupId: Uint8Array): FakeGroup {
    const group = this.groups.get(key(groupId));
    if (group === undefined) {
      throw new Error("this device holds no state for that group");
    }
    return group;
  }
}

/** A module double over one shared world. */
export function fakeMlsModule(world: FakeWorld): MlsModule {
  return {
    create: (identity) => new FakeDevice(identity, world),
    restore: (state) => {
      const parsed = JSON.parse(decoder.decode(state)) as {
        identity: string;
        groups: [string, { epoch: number; members: [string, string][]; joinedAtEpoch: number }][];
      };
      const device = new FakeDevice(parsed.identity, world);
      for (const [id, group] of parsed.groups) {
        device.groups.set(id, {
          epoch: group.epoch,
          members: new Map(group.members),
          pending: null,
          joinedAtEpoch: group.joinedAtEpoch,
        });
      }
      return device;
    },
  };
}
