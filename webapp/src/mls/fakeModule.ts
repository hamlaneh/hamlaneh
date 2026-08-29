import { packFrames, unpackFrames } from "./bytes";
import type { CommitBundleHandle, EncryptedMessageHandle, MlsDeviceHandle, MlsModule } from "./wasm";

/**
 * A stand-in for the wasm wrapper, for tests about the *service*.
 *
 * It performs no cryptography and pretends to none: "ciphertext" is the
 * plaintext with a marker, and a "key package" is a device's identity. What it
 * does reproduce faithfully is the state machine the service has to drive —
 * epochs that only advance on `commit_accepted`, a pending commit that must be
 * merged or cleared, membership by credential identity, and the refusal to
 * decrypt a message from an epoch this device was not in.
 *
 * Real MLS behaviour is proved against the real artifact in
 * `wasm.roundtrip.test.ts`. Mixing the two would make the service tests slow,
 * dependent on a build step, and no better at catching service bugs.
 */

const encoder = new TextEncoder();
const decoder = new TextDecoder();

interface FakeGroup {
  epoch: number;
  members: Set<string>;
  pending: { members: Set<string> } | null;
  /** The epoch this device joined at; earlier messages stay unreadable. */
  joinedAtEpoch: number;
}

/** Shared across every device in one test, standing in for the wire. */
export interface FakeWorld {
  /** Serialized group states, keyed by "identity|groupId". */
  devices: Map<string, FakeDevice>;
}

export function fakeWorld(): FakeWorld {
  return { devices: new Map() };
}

const KEY_PACKAGE_PREFIX = "kp:";
const WELCOME_PREFIX = "welcome:";
const COMMIT_PREFIX = "commit:";
const MESSAGE_PREFIX = "msg:";

function key(groupId: Uint8Array): string {
  return Array.from(groupId).join(",");
}

class FakeDevice implements MlsDeviceHandle {
  readonly groups = new Map<string, FakeGroup>();

  constructor(
    readonly identity: string,
    world: FakeWorld,
  ) {
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
    return encoder.encode(`sig:${this.identity}`);
  }

  group_ids(): Uint8Array {
    return packFrames([...this.groups.keys()].map((id) => Uint8Array.from(id.split(",").map(Number))));
  }

  generate_key_packages(count: number): Uint8Array {
    return packFrames(
      Array.from({ length: count }, () => encoder.encode(`${KEY_PACKAGE_PREFIX}${this.identity}`)),
    );
  }

  create_group(groupId: Uint8Array): void {
    this.groups.set(key(groupId), {
      epoch: 0,
      members: new Set([this.identity]),
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
    return packFrames([...this.require(groupId).members].map((id) => encoder.encode(id)));
  }

  add_members(groupId: Uint8Array, packedKeyPackages: Uint8Array): CommitBundleHandle {
    const group = this.require(groupId);
    const identities = unpackFrames(packedKeyPackages)
      .map((item) => decoder.decode(item).slice(KEY_PACKAGE_PREFIX.length))
      .filter((identity) => !group.members.has(identity));
    if (identities.length === 0) {
      return { commit: undefined, welcome: undefined };
    }
    group.pending = { members: new Set([...group.members, ...identities]) };
    return {
      commit: encoder.encode(`${COMMIT_PREFIX}${key(groupId)}:${[...group.pending.members].join(",")}`),
      welcome: encoder.encode(`${WELCOME_PREFIX}${key(groupId)}:${[...group.pending.members].join(",")}`),
    };
  }

  remove_user(groupId: Uint8Array, identity: string): CommitBundleHandle {
    const group = this.require(groupId);
    if (!group.members.has(identity) || identity === this.identity) {
      return { commit: undefined, welcome: undefined };
    }
    const members = new Set(group.members);
    members.delete(identity);
    group.pending = { members };
    return {
      commit: encoder.encode(`${COMMIT_PREFIX}${key(groupId)}:${[...members].join(",")}`),
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
    const members = body.slice(`${COMMIT_PREFIX}${key(groupId)}:`.length);
    group.members = new Set(members === "" ? [] : members.split(","));
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
      members: new Set((members ?? "").split(",")),
      pending: null,
      joinedAtEpoch: 1,
    });
    return groupId;
  }

  encrypt(groupId: Uint8Array, plaintext: string): EncryptedMessageHandle {
    const group = this.require(groupId);
    return {
      epoch: BigInt(group.epoch),
      ciphertext: encoder.encode(`${MESSAGE_PREFIX}${group.epoch}:${plaintext}`),
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
        groups: [string, { epoch: number; members: string[]; joinedAtEpoch: number }][];
      };
      const device = new FakeDevice(parsed.identity, world);
      for (const [id, group] of parsed.groups) {
        device.groups.set(id, {
          epoch: group.epoch,
          members: new Set(group.members),
          pending: null,
          joinedAtEpoch: group.joinedAtEpoch,
        });
      }
      return device;
    },
  };
}
