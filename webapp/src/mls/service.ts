import { api } from "../api/client";
import type { components } from "../api/schema";
import { fromBase64, packFrames, toBase64, unpackFrames, unpackStrings } from "./bytes";
import { Keystore, openKeystore } from "./keystore";
import type { ChannelMlsState, MlsState, MlsUnavailableReason } from "./types";
import { initialMlsState } from "./types";
import type { MlsDeviceHandle, MlsModule } from "./wasm";
import { loadMlsModule } from "./wasm";

type MlsWelcome = components["schemas"]["MlsWelcome"];
type MlsKeyPackageClaim = components["schemas"]["MlsKeyPackageClaim"];

/**
 * Everything the client does with MLS, in one object.
 *
 * The server is a delivery service and a key-package directory and knows
 * nothing else (ADR 006, decision 2), so every rule about who is in a group,
 * which epoch is current and what a message says is enforced here.
 *
 * Two invariants run through the whole file:
 *
 *  - **One operation per group at a time.** MLS state is a ratchet; two
 *    concurrent encrypts or two concurrent commit attempts on one group would
 *    interleave into state neither of them expected. Every mutation goes
 *    through `runOn(channelId, …)`, a per-channel promise chain, and device
 *    setup goes through its own.
 *  - **Persist after every mutation.** The exported map is the device; a
 *    change that is not written is a change a reload loses, which for a
 *    ratchet means a conversation this device can no longer read.
 */

/** How many key packages to keep published. The contract's cap is 50. */
const KEY_PACKAGE_BATCH = 20;

/** The contract's page cap on the commit log. */
const COMMIT_PAGE_SIZE = 50;

/** Bound on the 409 → refetch → rebuild loop, so a hot group cannot spin. */
const COMMIT_ATTEMPTS = 4;

/** Bound on commit-log paging, for the same reason. */
const CATCH_UP_PAGES = 50;

function errorCode(error: unknown): string | null {
  if (typeof error !== "object" || error === null) {
    return null;
  }
  const body = error as { error?: { code?: unknown } };
  return typeof body.error?.code === "string" ? body.error.code : null;
}

/** A fresh MLS group id: 32 random bytes, well inside the contract's 64. */
function newGroupId(): Uint8Array {
  return crypto.getRandomValues(new Uint8Array(32));
}

export interface MlsServiceOptions {
  currentUserId: string;
  /** Called on every state change; the hook turns this into React state. */
  onChange: (state: MlsState) => void;
  /** Test seam: an already-open keystore, or null to skip persistence. */
  keystore?: Keystore | null;
}

export class MlsService {
  private state: MlsState = initialMlsState;

  private module: MlsModule | null = null;
  private device: MlsDeviceHandle | null = null;
  private deviceId: string | null = null;
  private keystore: Keystore | null = null;
  private keystoreProvided = false;

  /** channel id → MLS group id. Rebuilt from the server, never persisted. */
  private readonly groups = new Map<string, Uint8Array>();

  private readonly chains = new Map<string, Promise<unknown>>();
  private starting: Promise<boolean> | null = null;

  constructor(private readonly options: MlsServiceOptions) {
    if (options.keystore !== undefined) {
      this.keystore = options.keystore;
      this.keystoreProvided = true;
    }
  }

  /* ── state ─────────────────────────────────────────────────────────── */

  private publish(next: Partial<MlsState>): void {
    this.state = { ...this.state, ...next };
    this.options.onChange(this.state);
  }

  private setChannel(channelId: string, channel: ChannelMlsState): void {
    this.publish({ channels: { ...this.state.channels, [channelId]: channel } });
  }

  private setDecrypted(messageId: string, text: string | null): void {
    if (messageId in this.state.decrypted && this.state.decrypted[messageId] === text) {
      return;
    }
    this.publish({ decrypted: { ...this.state.decrypted, [messageId]: text } });
  }

  private unavailable(reason: MlsUnavailableReason, error?: unknown): false {
    if (error !== undefined) {
      console.warn(`Encryption is unavailable (${reason}):`, error);
    }
    this.publish({ device: { status: "unavailable", reason } });
    return false;
  }

  /**
   * Serializes work per key. Rejections are absorbed by the chain so one
   * failed operation cannot strand every later one behind it, while the
   * caller still sees its own rejection.
   */
  private runOn<T>(key: string, work: () => Promise<T>): Promise<T> {
    const next = (this.chains.get(key) ?? Promise.resolve())
      .catch(() => undefined)
      .then(work);
    this.chains.set(
      key,
      next.catch(() => undefined),
    );
    return next;
  }

  /* ── device ────────────────────────────────────────────────────────── */

  /**
   * Brings the device up: state from the keystore or a fresh one, registered
   * with the directory, with a full key-package pool published.
   *
   * Idempotent and single-flighted — every entry point calls it, and a second
   * caller joins the first attempt rather than racing it.
   */
  start(): Promise<boolean> {
    this.starting ??= this.runOn("device", () => this.startOnce());
    return this.starting;
  }

  private async startOnce(): Promise<boolean> {
    if (this.device !== null) {
      return true;
    }
    this.publish({ device: { status: "starting" } });

    if (!this.keystoreProvided) {
      this.keystore = await openKeystore();
      if (this.keystore === null) {
        return this.unavailable("keystore");
      }
    }

    try {
      this.module = await loadMlsModule();
    } catch (error) {
      return this.unavailable("wasm", error);
    }

    const restored = (await this.keystore?.load()) ?? null;
    let device: MlsDeviceHandle | null = null;
    if (restored !== null) {
      try {
        const candidate = this.module.restore(restored);
        // A different person signing in on this profile must not inherit the
        // previous one's leaves: their credential would say someone else.
        device = candidate.identity === this.options.currentUserId ? candidate : null;
      } catch (error) {
        console.warn("The stored MLS device state could not be restored:", error);
      }
      if (device === null) {
        await this.keystore?.clear();
      }
    }
    if (device === null) {
      try {
        device = this.module.create(this.options.currentUserId);
      } catch (error) {
        return this.unavailable("wasm", error);
      }
    }
    this.device = device;

    const registered = await this.registerAndPublish(device);
    if (!registered) {
      this.device = null;
      return this.unavailable("server");
    }
    await this.persist();
    this.publish({ device: { status: "ready" } });
    return true;
  }

  private async registerAndPublish(device: MlsDeviceHandle): Promise<boolean> {
    try {
      const { data } = await api.POST("/api/v1/users/me/mls/device", {
        body: { signature_public_key: toBase64(device.signature_public_key()) },
      });
      if (data === undefined) {
        return false;
      }
      this.deviceId = data.id;

      // Replace-all, on every start: a key package carries an expiry the
      // server cannot read, so the contract's answer to staleness is a fresh
      // batch per connect rather than a server-side guess (openapi.yaml,
      // replaceMlsKeyPackages).
      const packages = unpackFrames(device.generate_key_packages(KEY_PACKAGE_BATCH)).map(
        toBase64,
      );
      const { response } = await api.PUT(
        "/api/v1/users/me/mls/devices/{deviceId}/key-packages",
        {
          params: { path: { deviceId: data.id } },
          body: { key_packages: packages },
        },
      );
      return response.ok;
    } catch (error) {
      console.warn("Could not register this MLS device:", error);
      return false;
    }
  }

  private async persist(): Promise<void> {
    if (this.device === null || this.keystore === null) {
      return;
    }
    await this.keystore.save(this.device.export_state());
  }

  /* ── per-channel group ─────────────────────────────────────────────── */

  /**
   * Makes this device a working member of the channel's group: finds or
   * creates the group, catches up on the commit log, and adds whichever
   * members are not in it yet.
   */
  openChannel(channelId: string): Promise<void> {
    return this.runOn(channelId, async () => {
      if (!(await this.start())) {
        return;
      }
      this.setChannel(channelId, { status: "opening" });
      try {
        await this.openChannelOnce(channelId);
      } catch (error) {
        console.warn(`Could not open the encrypted channel ${channelId}:`, error);
        this.setChannel(channelId, { status: "failed" });
      }
    });
  }

  private async openChannelOnce(channelId: string): Promise<void> {
    const device = this.requireDevice();
    const existing = await this.fetchGroup(channelId);

    if (existing === null) {
      // Nobody has created the group. We do, and if we lose that race the
      // winner will add us — so the loser's job is to wait, not to retry.
      const created = await this.createGroup(channelId);
      if (!created) {
        this.setChannel(channelId, { status: "waiting" });
        return;
      }
    } else {
      this.groups.set(channelId, existing);
      if (!device.has_group(existing)) {
        this.setChannel(channelId, { status: "waiting" });
        return;
      }
      await this.catchUp(channelId);
    }

    await this.reconcileMembers(channelId);
  }

  /** The group id the server holds for this channel, or null when none. */
  private async fetchGroup(channelId: string): Promise<Uint8Array | null> {
    const { data, error } = await api.GET("/api/v1/channels/{channelId}/mls/group", {
      params: { path: { channelId } },
    });
    if (data !== undefined) {
      return fromBase64(data.group_id);
    }
    if (errorCode(error) === "mls_group_not_found") {
      return null;
    }
    throw new Error(`the channel's group could not be read (${errorCode(error) ?? "unknown"})`);
  }

  /**
   * Creates the group locally and registers it. False when another member won
   * the race — in which case the local group is rolled back by reloading the
   * last persisted state, so this device is not left holding a group the
   * instance has never heard of.
   */
  private async createGroup(channelId: string): Promise<boolean> {
    const device = this.requireDevice();
    const groupId = newGroupId();
    device.create_group(groupId);

    const { response, error } = await api.POST("/api/v1/channels/{channelId}/mls/group", {
      params: { path: { channelId } },
      body: { group_id: toBase64(groupId) },
    });
    if (response.status === 201) {
      this.groups.set(channelId, groupId);
      await this.persist();
      return true;
    }
    await this.rollback();
    if (errorCode(error) === "mls_group_exists") {
      return false;
    }
    throw new Error(`the group could not be created (${errorCode(error) ?? "unknown"})`);
  }

  /**
   * Discards in-memory group state by rebuilding the device from what was
   * last written. Cheap (~8 KB) and exact, which is what a rollback has to be
   * when the alternative is a forked ratchet.
   */
  private async rollback(): Promise<void> {
    const stored = (await this.keystore?.load()) ?? null;
    if (stored === null || this.module === null) {
      return;
    }
    try {
      this.device = this.module.restore(stored);
    } catch (error) {
      console.warn("Could not roll back the MLS device state:", error);
    }
  }

  /** Applies every commit the log holds past this device's epoch. */
  private async catchUp(channelId: string): Promise<void> {
    const device = this.requireDevice();
    const groupId = this.requireGroup(channelId);
    for (let page = 0; page < CATCH_UP_PAGES; page += 1) {
      const { data } = await api.GET("/api/v1/channels/{channelId}/mls/commits", {
        params: {
          path: { channelId },
          query: { after_epoch: Number(device.epoch(groupId)), limit: COMMIT_PAGE_SIZE },
        },
      });
      const commits = data?.commits ?? [];
      if (commits.length === 0) {
        return;
      }
      for (const commit of commits) {
        // In order, one at a time: a commit applied out of order is a commit
        // that cannot be applied at all.
        device.apply_commit(groupId, fromBase64(commit.message));
      }
      await this.persist();
      if (commits.length < COMMIT_PAGE_SIZE) {
        return;
      }
    }
  }

  /**
   * Adds every channel member whose devices are not in the group yet.
   *
   * The channel's membership is the transport authority and the group is the
   * confidentiality authority; the server cannot check that they agree
   * (ADR 006), so this is the client doing it.
   */
  private async reconcileMembers(channelId: string): Promise<void> {
    const device = this.requireDevice();
    const groupId = this.requireGroup(channelId);
    const inGroup = new Set(unpackStrings(device.member_identities(groupId)));

    const members = await this.listMembers(channelId);
    const missing = members.filter((userId) => !inGroup.has(userId));
    if (missing.length === 0) {
      this.setChannel(channelId, { status: "ready" });
      return;
    }
    await this.addUsers(channelId, missing);
  }

  private async listMembers(channelId: string): Promise<string[]> {
    const ids: string[] = [];
    let cursor: string | undefined;
    // Bounded: a channel larger than this is beyond what one commit can add
    // anyway, and an endless cursor must not become an endless loop.
    for (let page = 0; page < 20; page += 1) {
      const { data } = await api.GET("/api/v1/channels/{channelId}/members", {
        params: {
          path: { channelId },
          query: { limit: 100, ...(cursor === undefined ? {} : { cursor }) },
        },
      });
      if (data === undefined) {
        break;
      }
      ids.push(...data.members.map((member) => member.id));
      if (data.next_cursor === undefined || data.next_cursor === cursor) {
        break;
      }
      cursor = data.next_cursor;
    }
    return ids;
  }

  /**
   * Claims one key package per device of each user and commits the adds.
   *
   * On 409 the epoch moved under us: catch up on what we missed and rebuild.
   * The claims from the losing attempt are already consumed — a key package
   * is single-use by protocol, so a lost race costs one package per device,
   * which is why the catch-up happens before the claim on every retry.
   */
  private async addUsers(channelId: string, userIds: readonly string[]): Promise<void> {
    const device = this.requireDevice();
    const groupId = this.requireGroup(channelId);

    for (let attempt = 0; attempt < COMMIT_ATTEMPTS; attempt += 1) {
      await this.catchUp(channelId);

      const claims: MlsKeyPackageClaim[] = [];
      const unreachable: string[] = [];
      for (const userId of userIds) {
        const { data } = await api.POST("/api/v1/channels/{channelId}/mls/key-package-claims", {
          params: { path: { channelId } },
          body: { user_id: userId },
        });
        if (data === undefined || data.claims.length === 0) {
          // No devices at all, or every pool empty. Either way this person
          // cannot be added yet, and the screen says exactly that.
          unreachable.push(userId);
          continue;
        }
        claims.push(...data.claims);
        if (data.missing_device_ids.length > 0) {
          unreachable.push(userId);
        }
      }

      const bundle = device.add_members(
        groupId,
        packFrames(claims.map((claim) => fromBase64(claim.key_package))),
      );
      const commit = bundle.commit;
      const welcome = bundle.welcome;
      if (commit === undefined || welcome === undefined) {
        // Every claimed device was already a member: somebody else added
        // them while we were claiming. Nothing to submit.
        this.settle(channelId, unreachable);
        return;
      }

      const { response, error } = await api.POST("/api/v1/channels/{channelId}/mls/commits", {
        params: { path: { channelId } },
        body: {
          epoch: Number(device.epoch(groupId)),
          message: toBase64(commit),
          // One entry, not one per device: OpenMLS puts an encrypted
          // group-secrets entry per new leaf inside a single Welcome, so the
          // same blob is addressed to every device the commit added and each
          // finds its own entry.
          welcomes: [{ device_ids: claims.map((claim) => claim.device_id), welcome: toBase64(welcome) }],
        },
      });
      if (response.status === 201) {
        device.commit_accepted(groupId);
        await this.persist();
        this.settle(channelId, unreachable);
        return;
      }

      device.commit_rejected(groupId);
      await this.persist();
      if (errorCode(error) !== "mls_epoch_conflict") {
        throw new Error(`the commit was refused (${errorCode(error) ?? "unknown"})`);
      }
    }
    throw new Error("the group's epoch kept moving; giving up on this attempt");
  }

  private settle(channelId: string, unreachable: readonly string[]): void {
    this.setChannel(
      channelId,
      unreachable.length === 0
        ? { status: "ready" }
        : { status: "incomplete", unreachableUserIds: [...unreachable] },
    );
  }

  /* ── events ────────────────────────────────────────────────────────── */

  /** `mls_commit`, and every reconnect: catch up, then re-check membership. */
  syncChannel(channelId: string): Promise<void> {
    return this.runOn(channelId, async () => {
      if (this.device === null || !this.groups.has(channelId)) {
        return;
      }
      try {
        await this.catchUp(channelId);
        await this.reconcileMembers(channelId);
      } catch (error) {
        console.warn(`Could not sync the encrypted channel ${channelId}:`, error);
        this.setChannel(channelId, { status: "failed" });
      }
    });
  }

  /**
   * `mls_welcome`, and every reconnect: join every Welcome addressed to this
   * device, acknowledging each only after the join actually succeeded.
   */
  syncWelcomes(): Promise<void> {
    return this.runOn("welcomes", async () => {
      if (!(await this.start())) {
        return;
      }
      const device = this.requireDevice();
      let welcomes: MlsWelcome[];
      try {
        const { data } = await api.GET("/api/v1/users/me/mls/welcomes");
        welcomes = data?.welcomes ?? [];
      } catch (error) {
        console.warn("Could not read pending welcomes:", error);
        return;
      }

      for (const welcome of welcomes) {
        if (welcome.device_id !== this.deviceId) {
          // A sibling device's Welcome: bytes this device cannot open, and
          // acknowledging it would delete it out from under that device.
          continue;
        }
        try {
          const groupId = device.join_from_welcome(fromBase64(welcome.welcome));
          this.groups.set(welcome.channel_id, groupId);
          await this.persist();
        } catch (error) {
          // Left unacknowledged on purpose: a Welcome that cannot be joined
          // today may be joinable after a restart, and deleting it would make
          // the failure permanent.
          console.warn(`Could not join the group for ${welcome.channel_id}:`, error);
          continue;
        }
        await api
          .DELETE("/api/v1/users/me/mls/welcomes/{welcomeId}", {
            params: { path: { welcomeId: welcome.id } },
          })
          .catch((error: unknown) => {
            // Always 204 by contract — an unknown or foreign id is a silent
            // no-op — so only a transport failure lands here, and it costs
            // one wasted join attempt next time.
            console.warn("Could not acknowledge a welcome:", error);
          });
        this.setChannel(welcome.channel_id, { status: "ready" });
      }
    });
  }

  /** `member_added` in an e2ee channel. */
  memberAdded(channelId: string): Promise<void> {
    return this.syncChannel(channelId);
  }

  /**
   * `member_removed`: commit the removal of that person's devices.
   *
   * Every remaining member's client does this, and all but one lose the race
   * — which is why a no-op and a 409 are both ordinary outcomes here.
   */
  memberRemoved(channelId: string, userId: string): Promise<void> {
    return this.runOn(channelId, async () => {
      if (this.device === null || !this.groups.has(channelId)) {
        return;
      }
      const device = this.device;
      const groupId = this.requireGroup(channelId);
      try {
        for (let attempt = 0; attempt < COMMIT_ATTEMPTS; attempt += 1) {
          await this.catchUp(channelId);
          const bundle = device.remove_user(groupId, userId);
          const commit = bundle.commit;
          if (commit === undefined) {
            return; // already gone: somebody else's commit removed them
          }
          const { response, error } = await api.POST(
            "/api/v1/channels/{channelId}/mls/commits",
            {
              params: { path: { channelId } },
              body: { epoch: Number(device.epoch(groupId)), message: toBase64(commit) },
            },
          );
          if (response.status === 201) {
            device.commit_accepted(groupId);
            await this.persist();
            return;
          }
          device.commit_rejected(groupId);
          await this.persist();
          if (errorCode(error) !== "mls_epoch_conflict") {
            throw new Error(`the removal was refused (${errorCode(error) ?? "unknown"})`);
          }
        }
      } catch (error) {
        console.warn(`Could not remove ${userId} from the group:`, error);
        this.setChannel(channelId, { status: "failed" });
      }
    });
  }

  /* ── messages ──────────────────────────────────────────────────────── */

  /**
   * Encrypts one message, or null when this device cannot — in which case
   * nothing is sent, because a plaintext fallback in an encrypted channel is
   * the silent downgrade the whole design exists to prevent.
   */
  encrypt(channelId: string, text: string): Promise<{ epoch: number; ciphertext: string } | null> {
    return this.runOn(channelId, async () => {
      if (this.device === null || !this.groups.has(channelId)) {
        return null;
      }
      const groupId = this.requireGroup(channelId);
      try {
        const message = this.device.encrypt(groupId, text);
        await this.persist();
        return {
          epoch: Number(message.epoch),
          ciphertext: toBase64(message.ciphertext),
        };
      } catch (error) {
        console.warn("Could not encrypt the message:", error);
        return null;
      }
    });
  }

  /**
   * Records the plaintext of a message this device just sent.
   *
   * MLS gives a sender no way to open its own application message — the
   * sender ratchet only produces — so without this the author's own bubbles
   * would render as undecryptable the moment they arrived. Session-scoped:
   * after a reload this device's own history is genuinely unreadable to it,
   * which a local plaintext store would fix and this slice does not build.
   */
  rememberSent(messageId: string, text: string): void {
    this.setDecrypted(messageId, text);
  }

  /**
   * Decrypts one message into the shared `decrypted` map.
   *
   * A failure records null rather than throwing: a message from before this
   * device joined is a normal thing to receive, and the bubble renders that
   * state honestly.
   */
  decrypt(channelId: string, messageId: string, ciphertext: string): Promise<void> {
    if (messageId in this.state.decrypted) {
      return Promise.resolve();
    }
    return this.runOn(channelId, async () => {
      if (this.device === null || !this.groups.has(channelId)) {
        this.setDecrypted(messageId, null);
        return;
      }
      const groupId = this.requireGroup(channelId);
      try {
        const text = this.device.decrypt(groupId, fromBase64(ciphertext));
        this.setDecrypted(messageId, text);
        // Decrypting advances the receiving ratchet, which is state.
        await this.persist();
      } catch (error) {
        console.warn(`Message ${messageId} could not be decrypted:`, error);
        this.setDecrypted(messageId, null);
        await this.persist();
      }
    });
  }

  /* ── helpers ───────────────────────────────────────────────────────── */

  private requireDevice(): MlsDeviceHandle {
    if (this.device === null) {
      throw new Error("the MLS device is not ready");
    }
    return this.device;
  }

  private requireGroup(channelId: string): Uint8Array {
    const groupId = this.groups.get(channelId);
    if (groupId === undefined) {
      throw new Error(`no MLS group is known for channel ${channelId}`);
    }
    return groupId;
  }
}
