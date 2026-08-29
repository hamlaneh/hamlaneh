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
type MlsMemberDevice = components["schemas"]["MlsMemberDevice"];

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

/** The contract's page cap on the member-device directory. */
const MEMBER_DEVICE_PAGE_SIZE = 200;

/**
 * Bound on member-device paging. Exhausting it throws rather than sweeping
 * with what has been read, which is why it is generous: 20 pages is 4000
 * members, and a channel past that has a bigger problem than this loop.
 */
const MEMBER_DEVICE_PAGES = 20;

/** The single chain every MLS operation runs on — see {@link MlsService.runOn}. */
const DEVICE_CHAIN = "device";

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

  /**
   * message id → the ciphertext that was successfully opened for it.
   *
   * Holds successes only. A failure is deliberately absent, so it is retried
   * rather than cached, and the value is the ciphertext rather than a flag so
   * an edited message — same id, new bytes — is opened again.
   */
  private readonly opened = new Map<string, string>();

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
   * Serializes work. Rejections are absorbed by the chain so one failed
   * operation cannot strand every later one behind it, while the caller still
   * sees its own rejection.
   *
   * One chain, not one per channel, and the key is kept only to name the
   * caller in a stack trace. Per-channel chains would be enough if a channel
   * owned its own state, but `this.device` is a single handle holding every
   * group, and both `persist()` and `rollback()` read and replace the whole
   * of it. With per-channel chains, a rollback in one channel could discard a
   * ratchet advance another channel had already sent ciphertext for, or
   * resurrect a group whose creation had just lost its race, and a caller
   * that captured `this.device` could persist a handle it no longer held.
   * Serializing everything makes the chain match what it protects.
   */
  private runOn<T>(_key: string, work: () => Promise<T>): Promise<T> {
    const next = (this.chains.get(DEVICE_CHAIN) ?? Promise.resolve())
      .catch(() => undefined)
      .then(work);
    this.chains.set(
      DEVICE_CHAIN,
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
    // Only a SUCCESSFUL attempt is memoized. `startOnce` reports failure by
    // resolving false rather than rejecting, and a settled promise is never
    // nullish, so memoizing unconditionally would pin the device at
    // unavailable for the life of the session over one offline blip — and
    // every retry the app has runs through here, so nothing could heal it
    // short of a reload.
    //
    // Not on the chain, deliberately: every internal caller reaches this from
    // inside chained work, so queueing here would put start() behind the
    // operation waiting for it and neither would ever run. The `starting`
    // promise is the single-flight guard this needs, and the chain the
    // callers already hold is what serializes it against everything else.
    this.starting ??= this.startOnce().then(
      (ok) => {
        if (!ok) {
          this.starting = null;
        }
        return ok;
      },
      (error: unknown) => {
        this.starting = null;
        throw error;
      },
    );
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
      if (!device.has_group(existing)) {
        // Deliberately NOT recorded in `groups` yet. The map means "this
        // device holds this group", and every guard that consults it —
        // syncChannel, encrypt, decrypt — would otherwise reach into a group
        // the device has no state for. An unrelated commit nudge would then
        // throw inside catchUp and mark the channel failed, which also stops
        // the Welcome poll that was the only thing that could rescue it.
        this.setChannel(channelId, { status: "waiting" });
        return;
      }
      this.groups.set(channelId, existing);
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
      // New epochs are exactly what an earlier failure was missing.
      this.retryFailedDecryptions();
      if (commits.length < COMMIT_PAGE_SIZE) {
        return;
      }
    }
  }

  /**
   * Makes the group's membership match the channel's, in BOTH directions.
   *
   * The channel's membership is the transport authority and the group is the
   * confidentiality authority; the server cannot check that they agree
   * (ADR 006), so this is the client doing it.
   *
   * Eviction is an allow-list sweep over the whole tree, keyed on leaf
   * signature keys, and both halves of that carry weight (ADR 007,
   * decision 2). Keyed on signature keys because the credential identity is a
   * string the enrolling client chose, so a leaf can claim to be anyone. An
   * allow-list rather than "look up the removed person's keys and evict
   * those" because the leaf this defends against carries a key the directory
   * never listed under the removed user at all — a per-user question cannot
   * name it, and a sweep does not have to.
   *
   * Sweeping is also not optional and not merely tidy. `member_removed` is a
   * live frame over a replay buffer bounded at 256 events and five minutes,
   * so a removal that happened while nobody was connected reaches nobody, and
   * no other path recovers it: a resync backfills messages only. A leaf left
   * in the tree is a leaf that was never cryptographically evicted, and it
   * reads every epoch that follows.
   */
  private async reconcileMembers(channelId: string): Promise<void> {
    // The whole roster before the tree is touched. A page that failed is not
    // a shorter allow-list, it is a wrong one — every key it would have
    // carried belongs to a member the sweep would then evict — so this
    // throws rather than sweeping on a partial answer, and the caller renders
    // the channel as failed.
    const members = await this.listMemberDevices(channelId);

    // Before the adds: handing a new epoch's secrets to a leaf that should be
    // gone is the one ordering mistake this whole reconcile can make.
    await this.sweepLeaves(
      channelId,
      members.flatMap((member) => member.signature_public_keys),
    );

    // Read after the sweep, not before. A member whose only leaf the sweep
    // evicted — because the directory had dropped that device — is absent
    // now, and this is what re-adds them on the same pass.
    //
    // Identities and not keys, and only here: an add is not a security
    // decision (`add_members` validates key-package cryptography and skips
    // keys already in the tree, and a leaf the directory does not vouch for
    // is swept out on the next pass regardless), so this asks the cheaper
    // question. Its known ceiling is that a second device of somebody
    // already present is not noticed — the multi-device slice's problem, not
    // one the sweep introduces.
    const inGroup = new Set(
      unpackStrings(this.requireDevice().member_identities(this.requireGroup(channelId))),
    );
    const missing = members
      .map((member) => member.user_id)
      .filter((userId) => !inGroup.has(userId));
    if (missing.length === 0) {
      this.setChannel(channelId, { status: "ready" });
      return;
    }
    await this.addUsers(channelId, missing);
  }

  /**
   * Every current member of the channel and the signature keys of their
   * registered devices — the allow-list, read whole or not at all.
   *
   * Any failure throws, including running out of pages: the caller sweeps
   * with what this returns, so returning less than the roster would evict
   * real members. That is why there is no partial-result path here even
   * though a partial one would be easy to write.
   */
  private async listMemberDevices(channelId: string): Promise<MlsMemberDevice[]> {
    const members: MlsMemberDevice[] = [];
    let cursor: string | undefined;
    for (let page = 0; page < MEMBER_DEVICE_PAGES; page += 1) {
      const { data, error } = await api.GET(
        "/api/v1/channels/{channelId}/mls/member-devices",
        {
          params: {
            path: { channelId },
            query: { limit: MEMBER_DEVICE_PAGE_SIZE, ...(cursor === undefined ? {} : { cursor }) },
          },
        },
      );
      if (data === undefined) {
        throw new Error(
          `the channel's member devices could not be read (${errorCode(error) ?? "unknown"})`,
        );
      }
      members.push(...data.members);
      if (data.next_cursor === undefined) {
        return members;
      }
      cursor = data.next_cursor;
    }
    throw new Error("the channel's member devices did not end");
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

      // Before the POST, not after: the server's log advances the moment it
      // answers 201, and if this device dies between that answer and
      // `commit_accepted` it has to be able to recognise its own commit in
      // the log. That recovery reads the pending commit, so the pending
      // commit has to have been written down first.
      await this.persist();

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
   * `member_removed`: reconcile, which is what evicts them.
   *
   * Takes no user id, and that is the point rather than an omission. What
   * leaves the tree is decided by the directory's roster and not by the id on
   * the frame, so this cannot be walked around by a leaf that credentialed
   * itself as somebody who is staying (ADR 007). Every remaining member's
   * client does this and all but one lose the race, which is why a no-op and
   * a 409 are both ordinary outcomes underneath.
   */
  memberRemoved(channelId: string): Promise<void> {
    return this.syncChannel(channelId);
  }

  /**
   * Commits the eviction of every leaf outside `allowedKeys`. Caller holds
   * the chain.
   *
   * The device handle is read fresh from `this` at each step rather than
   * captured, so a rollback elsewhere cannot leave this loop accepting a
   * commit on one handle while `persist` writes another.
   */
  private async sweepLeaves(channelId: string, allowedKeys: readonly string[]): Promise<void> {
    // Decoded once, before anything is committed: a key the directory sent as
    // malformed base64 must stop the sweep rather than shrink the allow-list
    // it is part of.
    const allowed = packFrames(allowedKeys.map(fromBase64));

    for (let attempt = 0; attempt < COMMIT_ATTEMPTS; attempt += 1) {
      await this.catchUp(channelId);
      const groupId = this.requireGroup(channelId);
      const bundle = this.requireDevice().retain_leaves(groupId, allowed);
      const commit = bundle.commit;
      if (commit === undefined) {
        // Every leaf is allowed — the ordinary case — or somebody else's
        // commit swept first, which is the ordinary race.
        return;
      }
      // Written before the POST, for the reason addUsers states.
      await this.persist();
      const { response, error } = await api.POST("/api/v1/channels/{channelId}/mls/commits", {
        params: { path: { channelId } },
        body: {
          epoch: Number(this.requireDevice().epoch(groupId)),
          message: toBase64(commit),
        },
      });
      if (response.status === 201) {
        this.requireDevice().commit_accepted(groupId);
        await this.persist();
        return;
      }
      this.requireDevice().commit_rejected(groupId);
      await this.persist();
      if (errorCode(error) !== "mls_epoch_conflict") {
        throw new Error(`the eviction was refused (${errorCode(error) ?? "unknown"})`);
      }
    }
    throw new Error("the group's epoch kept moving; giving up on this sweep");
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
    if (this.alreadyOpen(messageId, ciphertext)) {
      return Promise.resolve();
    }
    return this.runOn(channelId, async () => {
      // Re-checked INSIDE the chain, not only at enqueue. `decryptAll` runs
      // again on every change to the message list, so the same message is
      // routinely enqueued twice before the first attempt has run. The real
      // module consumes a message's ratchet secrets on use, so the second
      // attempt fails on a message the first one opened — and the catch below
      // would then replace good plaintext with "cannot decrypt".
      if (this.alreadyOpen(messageId, ciphertext)) {
        return;
      }
      if (this.device === null || !this.groups.has(channelId)) {
        // Not remembered in `opened`: this device may hold the group a moment
        // from now, and a verdict recorded before it does would outlive the
        // condition that produced it.
        this.setDecrypted(messageId, null);
        return;
      }
      const groupId = this.requireGroup(channelId);
      try {
        const text = this.device.decrypt(groupId, fromBase64(ciphertext));
        // Keyed on the ciphertext, not the message id: an edit reuses the id
        // with new bytes, and an id-keyed cache would show every recipient
        // the text from before the edit for the rest of the session.
        this.opened.set(messageId, ciphertext);
        this.setDecrypted(messageId, text);
        // Decrypting advances the receiving ratchet, which is state.
        await this.persist();
      } catch (error) {
        console.warn(`Message ${messageId} could not be decrypted:`, error);
        // A failure is rendered but not remembered. The ordinary causes are
        // temporary — a message sealed at an epoch this device has not caught
        // up to yet, or a group it is still waiting to join — and catchUp
        // clears the failed verdicts so they are attempted again.
        //
        // It never overwrites a plaintext that is already known, which is the
        // author's own message: `rememberSent` holds text no decryption
        // produced, and MLS gives a sender no way to open what it sent.
        if (typeof this.state.decrypted[messageId] !== "string") {
          this.setDecrypted(messageId, null);
        }
        await this.persist();
      }
    });
  }

  /**
   * Whether this exact ciphertext has already been opened for this message.
   *
   * Two ways to be done with a message: it was decrypted from these very
   * bytes, or its plaintext is known without ever having been decrypted —
   * this device sent it, and `rememberSent` recorded it. The second needs its
   * own arm because the sender genuinely cannot decrypt its own message, so
   * without it every author's bubble would be retried on every render and
   * fail every time.
   */
  private alreadyOpen(messageId: string, ciphertext: string): boolean {
    if (this.opened.get(messageId) === ciphertext) {
      return true;
    }
    return !this.opened.has(messageId) && typeof this.state.decrypted[messageId] === "string";
  }

  /**
   * Forgets every failed decryption, so the next pass retries them.
   *
   * Called after applying commits: the commonest reason a message would not
   * open is that its epoch had not arrived yet, and without this the first
   * attempt's verdict would stand for the rest of the session.
   */
  private retryFailedDecryptions(): void {
    const entries = Object.entries(this.state.decrypted);
    const kept = entries.filter(([, text]) => text !== null);
    if (kept.length === entries.length) {
      return;
    }
    this.publish({ decrypted: Object.fromEntries(kept) });
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
