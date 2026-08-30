import { api } from "../api/client";
import type { components } from "../api/schema";
import { fromBase64, packFrames, toBase64, unpackFrames, unpackStrings } from "./bytes";
import { Keystore, openKeystore } from "./keystore";
import { safetyNumber } from "./safetyNumber";
import type {
  ChannelMlsState,
  ChannelVerification,
  MediaKey,
  MlsState,
  MlsUnavailableReason,
  VerificationLevel,
  VerificationRecords,
} from "./types";
import { clearVerification, initialMlsState, needsAttention } from "./types";
import { acceptedKeys, inspect } from "./verification";
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
 *  - **One MLS operation at a time, device-wide.** MLS state is a ratchet, and
 *    the device is one handle holding every group, which `persist` and
 *    `rollback` read and replace whole. Per-group serialization would let a
 *    rollback in one conversation discard a ratchet advance another had
 *    already sent ciphertext for, so `runOn` is a single chain (see its own
 *    note). `start()` stays off it deliberately, because its callers hold it.
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

  /**
   * This device's own leaf signature key, base64 — read from the wrapper's
   * `signature_public_key()` at start.
   *
   * The one key in the system this device did not learn from the server, which
   * is why it and not the directory anchors the own half of every safety
   * number (see `acceptedKeys` in `verification.ts`).
   */
  private ownKey: string | null = null;

  /** Accepted key sets per person, loaded once and written on every change. */
  private records: VerificationRecords = {};

  /**
   * channel id → (user id → the keys the directory listed for them), cached at
   * the last reconcile.
   *
   * Cached rather than fetched because the send path must not touch the
   * network: `encrypt` re-checks the invariant against this, and a gate that
   * needed a round trip would be a gate that fails open when the server is
   * slow. Rebuilt from the server on every reconcile, never persisted.
   */
  private readonly directory = new Map<string, Map<string, string[]>>();

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
    this.ownKey = toBase64(device.signature_public_key());
    this.records = (await this.keystore?.loadRecords()) ?? {};

    const registered = await this.registerAndPublish(device);
    if (!registered) {
      this.device = null;
      return this.unavailable("server");
    }
    await this.persist();
    this.publish({ device: { status: "ready" }, records: this.records });
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
    // Before the keystore, and unconditionally: `persist` is called after
    // every mutation, which makes it the one place that cannot miss a merge —
    // and a device running without a keystore (the test seam) still advances
    // epochs that the call layer has to rotate on.
    this.publishEpochs();
    if (this.device === null || this.keystore === null) {
      return;
    }
    await this.keystore.save(this.device.export_state());
  }

  /**
   * Republishes every held group's epoch.
   *
   * Hooked to `persist` rather than to each of `catchUp`, `commit_accepted`
   * and `join_from_welcome` on purpose: those are the merge points today, and
   * a fourth added later would silently stop the media key rotating. Persist
   * is what they all already share.
   */
  private publishEpochs(): void {
    const device = this.device;
    if (device === null) {
      return;
    }
    const epochs: Record<string, number> = {};
    for (const [channelId, groupId] of this.groups) {
      try {
        epochs[channelId] = Number(device.epoch(groupId));
      } catch (error) {
        // A group id in the map the device has no state for: a rolled-back
        // creation, mid-flight. Reporting no epoch is right — the call layer
        // then holds no key rather than an imagined one.
        console.warn(`Could not read the epoch for ${channelId}:`, error);
      }
    }
    this.publish({ epochs });
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

    // Trust before the tree: pinning first sight, noticing a changed set and
    // raising the own-account prompt are all decisions about *people*, and
    // they are the same whatever the tree happens to hold this second.
    await this.absorbDirectory(channelId, members);

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
      // Never this device's own user. A device that reached here is in the
      // group — `openChannelOnce` sends it to `waiting` otherwise, and a
      // device outside the group could not commit an add in any case — so
      // "we are missing" can only ever be a lie told by a stale read, and
      // acting on it would consume this device's own key packages to add a
      // leaf it already has.
      .filter((userId) => userId !== this.options.currentUserId && !inGroup.has(userId));
    if (missing.length === 0) {
      this.setChannel(channelId, { status: "ready" });
      this.publishVerification(channelId);
      return;
    }
    await this.addUsers(channelId, missing);
    // After the adds, not before: the leaves this pass just committed are
    // exactly the ones the invariant has to be re-read over.
    this.publishVerification(channelId);
  }

  /* ── verification (ADR 008) ────────────────────────────────────────── */

  /**
   * Turns one directory read into trust decisions about people.
   *
   * Three outcomes per person, and they are the whole of decision 3's state
   * machine: no record → pin now, silently, because first sight is the only
   * moment at which there is nothing to compare against and a question would
   * be noise; record matches → nothing to say; record differs → left alone, so
   * the stored set stays the one a human accepted and `changed` can be derived
   * from the difference for as long as it stands.
   *
   * This device's own account is deliberately excluded from pinning. A key
   * appearing under your own id is either your other browser profile or an
   * attack, no software here can tell the two apart, and silently pinning it
   * would answer the one question ADR 008 insists a human answers.
   */
  private async absorbDirectory(
    channelId: string,
    members: readonly MlsMemberDevice[],
  ): Promise<void> {
    this.directory.set(
      channelId,
      new Map(members.map((member) => [member.user_id, [...member.signature_public_keys]])),
    );

    let pinned = false;
    for (const member of members) {
      if (member.user_id === this.options.currentUserId) {
        continue;
      }
      // Somebody who has registered nothing is not a first sight — there is no
      // key set to have seen. Pinning their emptiness would make their first
      // real device a "changed keys" warning, which is the commonest ordinary
      // event there is: a colleague signing in after the channel was opened.
      // They are pinned on the reconcile that first shows them a device.
      if (member.signature_public_keys.length === 0) {
        continue;
      }
      if (this.records[member.user_id] === undefined) {
        this.records = {
          ...this.records,
          [member.user_id]: {
            userId: member.user_id,
            keys: [...member.signature_public_keys],
            level: "pinned",
            at: Date.now(),
          },
        };
        pinned = true;
      }
    }
    if (pinned) {
      await this.saveRecords();
    }
    this.publishOwnDevices(members);
  }

  /**
   * Raises the own-account prompt when the directory lists a key under this
   * account that no human here has accepted — the loudest thing in the slice.
   */
  private publishOwnDevices(members: readonly MlsMemberDevice[]): void {
    const listed = members.find((member) => member.user_id === this.options.currentUserId);
    if (listed === undefined) {
      return;
    }
    const accepted = new Set(this.acceptedFor(this.options.currentUserId));
    const unaccepted = listed.signature_public_keys.filter((key) => !accepted.has(key));
    this.publish({ ownDevices: unaccepted.length === 0 ? null : { keys: unaccepted } });
  }

  /** The keys this device accepts for a person — see `verification.ts`. */
  private acceptedFor(userId: string): readonly string[] {
    return acceptedKeys(userId, this.records, this.options.currentUserId, this.ownKey);
  }

  /**
   * The conversation's verification state, computed from what is already in
   * hand: the cached directory, the records, and the tree.
   *
   * Purely local, and that is a requirement rather than an optimization — this
   * runs on the send path, where a network call would be a gate that fails
   * open whenever the server is slow or hostile enough to stall it.
   */
  private verificationOf(channelId: string): ChannelVerification {
    const directory = this.directory.get(channelId);
    if (directory === undefined || this.device === null || !this.groups.has(channelId)) {
      // Nothing has been reconciled here yet, so there is nothing to have
      // changed. The channel's own availability state is what withholds the
      // composer until then; this state answers a different question.
      return clearVerification;
    }
    const treeKeys = unpackFrames(
      this.device.member_signature_keys(this.requireGroup(channelId)),
    ).map(toBase64);
    return inspect(directory, treeKeys, this.ownKey, (userId) => this.acceptedFor(userId));
  }

  private publishVerification(channelId: string): ChannelVerification {
    const verification = this.verificationOf(channelId);
    this.publish({
      verification: { ...this.state.verification, [channelId]: verification },
    });
    return verification;
  }

  private async saveRecords(): Promise<void> {
    await this.keystore?.saveRecords(this.records);
    this.publish({ records: this.records });
  }

  /**
   * Records `userId`'s current directory set at `level`, and re-checks every
   * conversation the decision touches.
   *
   * The set is stored rather than a flag, so an acceptance names exactly what
   * it accepted and can never generalize to the next change (ADR 008,
   * decision 3). Both exits come through here and there is no third: nothing
   * in this service clears a warning on a timer, on a dismissal, or per
   * message.
   */
  private async record(userId: string, level: VerificationLevel): Promise<void> {
    const keys = this.currentKeysOf(userId);
    if (keys === null) {
      return;
    }
    this.records = {
      ...this.records,
      [userId]: { userId, keys: [...keys], level, at: Date.now() },
    };
    await this.saveRecords();
    for (const channelId of this.directory.keys()) {
      this.publishVerification(channelId);
    }
    if (userId === this.options.currentUserId) {
      this.publish({ ownDevices: null });
    }
  }

  /**
   * The directory's current claim about a person, from whichever conversation
   * has read it most recently.
   *
   * Trust is per person and instance-global while the directory read is per
   * channel, so this is the join between the two. Null when no reconcile has
   * ever seen them, in which case there is nothing to accept.
   */
  private currentKeysOf(userId: string): readonly string[] | null {
    for (const members of this.directory.values()) {
      const keys = members.get(userId);
      if (keys !== undefined) {
        return keys;
      }
    }
    return null;
  }

  /**
   * The safety number for this conversation with `userId` — sixty digits, and
   * the same string on both screens (ADR 008, decision 4).
   *
   * **The two halves come from deliberately different places, and that
   * asymmetry is the entire security of the ceremony.** Our own half is
   * computed from this device's own signature key plus own devices a human
   * explicitly accepted — never from the directory. The peer's half is
   * computed from the directory's current claim about them, because that claim
   * is precisely what the ceremony puts under test.
   *
   * Read the other way round it is easier to see why it cannot drift: if this
   * function took both halves from the directory, a server that planted a key
   * on the peer would plant it in both computations, both screens would print
   * the same number, the humans would say "they match", and the ceremony would
   * bless the attack while looking exactly like it does now. Nothing would
   * throw and no other test would fail. `safetyNumberFor` is therefore the one
   * place allowed to ask the directory about a peer and the one place forbidden
   * to ask it about us.
   */
  async safetyNumberFor(userId: string): Promise<string | null> {
    const theirs = this.currentKeysOf(userId);
    if (theirs === null || this.ownKey === null) {
      return null;
    }
    return safetyNumber(
      { userId: this.options.currentUserId, keys: this.acceptedFor(this.options.currentUserId) },
      { userId, keys: theirs },
    );
  }

  /** Exit 1: the humans compared the number out of band and it matched. */
  verifyPeer(userId: string): Promise<void> {
    return this.record(userId, "verified");
  }

  /**
   * Exit 2: "I checked", with no ceremony behind it.
   *
   * Always `pinned`, including on a peer who was `verified` a moment ago —
   * which visibly downgrades the badge, and is meant to. An unceremonied
   * acceptance is a pin, and a UI that let it keep a verified badge would be
   * claiming a proof that nobody produced.
   */
  acceptPeer(userId: string): Promise<void> {
    return this.record(userId, "pinned");
  }

  /**
   * The own-account prompt's yes: this new device under your id is yours.
   *
   * `pinned` rather than `verified` for the same reason as `acceptPeer` — it
   * is a human judgment call about a plausible-looking device, which the
   * multi-device slice's authenticated linking is what actually replaces.
   */
  acceptOwnDevices(): Promise<void> {
    return this.record(this.options.currentUserId, "pinned");
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

        // Reconcile before calling it ready, rather than setting ready here.
        // A Welcome puts this device into a tree somebody else assembled, and
        // `ready` is what enables the composer — so announcing ready straight
        // from the join would let this device encrypt into a tree it has
        // never swept, to whatever leaves that tree happens to hold. This is
        // the first pass the ADR 007 guarantee is stated in terms of, and it
        // belongs before the first message, not after it. It also sets the
        // channel state itself (ready, incomplete, or failed on a directory
        // it could not read whole), which is why nothing is set here.
        try {
          await this.reconcileMembers(welcome.channel_id);
        } catch (error) {
          console.warn(`Could not reconcile the joined channel ${welcome.channel_id}:`, error);
          this.setChannel(welcome.channel_id, { status: "failed" });
        }
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

  /* ── media (ADR 009) ───────────────────────────────────────────────── */

  /**
   * The media key for this conversation's current epoch, or null when this
   * device holds no group for it.
   *
   * Deliberately NOT gated on the verification state, and the asymmetry is
   * ADR 008's rather than a shortcut: deriving a key in order to *listen*
   * hands an attacker nothing, and refusing to would only stop this device
   * hearing a call everyone else is in. Sealing outbound frames under it is
   * the act that matters, and that gate lives on the publish side.
   *
   * Synchronous and derived on demand: `export_secret` is a read of state this
   * device already holds, so there is nothing to cache and nothing to go
   * stale — and a stored media key would be one more copy of a secret whose
   * whole appeal is that it never has to be stored.
   */
  mediaKey(channelId: string): MediaKey | null {
    const groupId = this.groups.get(channelId);
    if (this.device === null || groupId === undefined) {
      return null;
    }
    try {
      return {
        epoch: Number(this.device.epoch(groupId)),
        secret: this.device.exporter(groupId),
      };
    } catch (error) {
      // An inactive group — this device was evicted from it. No key exists,
      // and the call layer refuses rather than joining unencrypted.
      console.warn(`Could not derive the media key for ${channelId}:`, error);
      return null;
    }
  }

  /* ── messages ──────────────────────────────────────────────────────── */

  /**
   * Encrypts one message, or null when this device cannot — in which case
   * nothing is sent, because a plaintext fallback in an encrypted channel is
   * the silent downgrade the whole design exists to prevent.
   *
   * Two different refusals come out of here as the same null, and the caller
   * tells them apart by state rather than by the return value: the channel's
   * own status says "unavailable", and `state.verification[channelId]` says
   * "blocked pending verification". Keeping the second in published state
   * rather than in a return code is what lets the composer be *replaced* by
   * the warning — a send that merely failed would leave the user retyping.
   */
  encrypt(channelId: string, text: string): Promise<{ epoch: number; ciphertext: string } | null> {
    return this.runOn(channelId, async () => {
      if (this.device === null || !this.groups.has(channelId)) {
        return null;
      }

      // Re-checked here and not merely trusted from the last reconcile: this
      // is the only operation wholly this client's own, so it is the only
      // place refusal can bite (ADR 007 — a sequenced commit cannot be
      // vetoed, so receipt is not consent). Local, and published as it is
      // computed, so a tree that changed under a stale screen raises the
      // warning at the moment of the send rather than swallowing it.
      if (needsAttention(this.publishVerification(channelId))) {
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
