/**
 * The MLS device state at rest.
 *
 * The wrapper's exported state map holds raw MLS secrets — the signature
 * private key, epoch secrets, message secrets (measured in
 * docs/spikes/mls-wasm-integration.md §4). Writing that map to IndexedDB as-is
 * would put unwrapped key material on disk, so every export is encrypted with
 * AES-GCM under a per-device key that is generated non-extractable and lives
 * in IndexedDB as a `CryptoKey` object. The bytes of that key never exist in
 * JavaScript, in this profile or any other.
 *
 * ## What this resists, honestly
 *
 * It resists a **copy of the database files**: an IndexedDB directory lifted
 * off a disk, a backup, a stolen laptop whose profile is not otherwise
 * running, a piece of software that scrapes browser storage. In all of those
 * the state is ciphertext and the wrapping key is a handle the copier cannot
 * export.
 *
 * It does **not** resist an attacker who can run code as this origin, or who
 * has full live access to the browser profile: they can ask the same
 * `CryptoKey` to decrypt, exactly as this module does. A browser cannot keep
 * a key from the page it belongs to. Nothing here should be described as
 * making the device secure — it raises the cost of offline scraping, and that
 * is the whole claim.
 *
 * Every access is wrapped: a database that will not open (private mode with
 * storage disabled, a quota refusal, a corrupt store) means MLS is
 * unavailable for this session, which is a state the UI renders — never a
 * crash and never a silent fallback to plaintext.
 */

import type { SentMessage, VerificationRecord, VerificationRecords } from "./types";

const DATABASE_NAME = "hamlaneh-mls";
const DATABASE_VERSION = 1;
const STORE_NAME = "device";
const WRAPPING_KEY_ID = "wrapping-key";
const STATE_ID = "state";

/**
 * Verification records, beside the device state and under the same wrapping
 * key (ADR 008, decision 1).
 *
 * Beside rather than inside `export_state`: these are the service's data, not
 * the wasm module's, and nothing about verification enters `src-mls` — the
 * glue-only rule of ADR 006 survives this slice. They carry the same honest
 * ceiling as the state does, stated once in the module note above.
 *
 * The server stores none of these and never sees them. A record whose whole
 * value is that the server did not author it is worth less than nothing stored
 * where the server can write: a forged `verified` marks a planted key safe and
 * the UI then renders the attack as safety.
 */
const RECORDS_ID = "verification";

/**
 * The plaintext of what this device sent, beside the state and under the same
 * wrapping key.
 *
 * Here rather than in `localStorage` or a plain object store for one reason:
 * what you said is as sensitive as what you received, and the state slot next
 * to it holds the keys that opened the latter. It carries the module note's
 * ceiling unchanged and gains no other — a copied database does not read it;
 * an attacker running as this origin does.
 *
 * Bounded by the caller, which owns the retention policy: see `trimSent` in
 * `service.ts`. Nothing here enforces a size, and nothing here needs to —
 * `saveSent` writes exactly the list it is handed.
 */
const SENT_ID = "sent";

/**
 * How many key packages the directory last reported holding for this device.
 *
 * Persisted because it is the input to the low-water replenishment decision
 * (`KEY_PACKAGE_LOW_WATER` in `service.ts`), and the only reading of the pool
 * the contract offers is the PUT that *replaces* it. A count that did not
 * survive the reload would have to be re-established by the very publish it
 * exists to avoid, which is the behaviour the policy replaces.
 *
 * Wrapped like everything else in here, not because a count is a secret but
 * because a second, unwrapped shape in this store would be one more thing to
 * reason about for no gain.
 */
const KEY_PACKAGE_COUNT_ID = "key-packages";

/**
 * The backup key handle (ADR 010, decision 2).
 *
 * A non-extractable `CryptoKey` beside the wrapping key, stored the same way
 * and carrying the same honest ceiling. The recovery key it was derived from
 * is shown once and then discarded — never persisted here, never anywhere —
 * so this handle is what ongoing backups are sealed with and the string is
 * asked for again only on a fresh device's restore.
 */
const BACKUP_KEY_ID = "backup-key";

/**
 * The anti-rollback floor and the enrolment decision, wrapped like everything
 * else in here (ADR 010, decision 3).
 *
 * The floor is the highest counter this device has ever written or accepted,
 * and it is the security control of the whole slice: any envelope whose
 * SEALED counter is below it is a rollback and is refused. It lives beside the
 * records rather than in `localStorage` for the reason everything else here
 * does — one shape, one wrapping, one thing to reason about — and it must
 * survive turning backup off, because a floor that reset would re-open the
 * window it exists to close.
 */
const BACKUP_STATE_ID = "backup";

const IV_BYTES = 12;

/**
 * What this device remembers about its backup between reloads.
 *
 * `declined` is recorded because ADR 010 says a decline is respected rather
 * than re-asked: the offer becomes a passive indicator in settings, never a
 * nag. It is deliberately not the same fact as "has no backup key" — somebody
 * who turned backup off later has neither a key nor a decline, and the offer
 * is right to be available to them again.
 */
export interface StoredBackupState {
  floor: number;
  declined: boolean;
}

const noBackupState: StoredBackupState = { floor: 0, declined: false };

/**
 * Parses the stored backup state, falling back to the safe direction for each
 * field independently.
 *
 * The floor falls back to 0, which accepts any envelope — and that is the only
 * honest answer for a device that cannot read its own floor, because inventing
 * one would refuse the user's real backup. The decline falls back to false,
 * which re-offers rather than silently leaving somebody with no backup they
 * believe they turned on.
 */
function readBackupState(json: string): StoredBackupState {
  const parsed: unknown = JSON.parse(json);
  if (typeof parsed !== "object" || parsed === null) {
    return noBackupState;
  }
  const state = parsed as { floor?: unknown; declined?: unknown };
  return {
    floor:
      typeof state.floor === "number" && Number.isInteger(state.floor) && state.floor >= 0
        ? state.floor
        : 0,
    declined: state.declined === true,
  };
}

/** What a stored state record looks like. `iv` is fresh for every write. */
interface StoredState {
  iv: Uint8Array<ArrayBuffer>;
  ciphertext: Uint8Array<ArrayBuffer>;
}

/**
 * The narrow slice of a key-value store this module needs.
 *
 * An interface rather than direct `indexedDB` calls so the tests can drive the
 * real encryption against an in-memory map: what is worth testing here is the
 * wrapping, and IndexedDB has no business in a unit test.
 */
export interface KeyValueStore {
  get: (key: string) => Promise<unknown>;
  put: (key: string, value: unknown) => Promise<void>;
  delete: (key: string) => Promise<void>;
}

/** An in-memory store — the test double, and nothing persists through it. */
export function memoryStore(): KeyValueStore {
  const entries = new Map<string, unknown>();
  return {
    get: (key) => Promise.resolve(entries.get(key)),
    put: (key, value) => {
      entries.set(key, value);
      return Promise.resolve();
    },
    delete: (key) => {
      entries.delete(key);
      return Promise.resolve();
    },
  };
}

function request<T>(source: IDBRequest<T>): Promise<T> {
  return new Promise((resolve, reject) => {
    source.onsuccess = () => {
      resolve(source.result);
    };
    source.onerror = () => {
      reject(source.error ?? new Error("the IndexedDB request failed"));
    };
  });
}

/**
 * Opens the app's store, or resolves to null when this browser will not give
 * us one. Null is a supported answer, not an error path.
 */
export async function openKeyValueStore(): Promise<KeyValueStore | null> {
  if (typeof indexedDB === "undefined") {
    return null;
  }
  let database: IDBDatabase;
  try {
    database = await new Promise<IDBDatabase>((resolve, reject) => {
      const open = indexedDB.open(DATABASE_NAME, DATABASE_VERSION);
      open.onupgradeneeded = () => {
        if (!open.result.objectStoreNames.contains(STORE_NAME)) {
          open.result.createObjectStore(STORE_NAME);
        }
      };
      open.onsuccess = () => {
        resolve(open.result);
      };
      open.onerror = () => {
        reject(open.error ?? new Error("could not open the MLS database"));
      };
      // Another tab holding an old version open: rather than block forever,
      // give up and let this session run without MLS.
      open.onblocked = () => {
        reject(new Error("the MLS database is blocked by another tab"));
      };
    });
  } catch (error) {
    console.warn("The MLS keystore is unavailable:", error);
    return null;
  }

  const transact = async <T>(
    mode: IDBTransactionMode,
    run: (store: IDBObjectStore) => IDBRequest<T>,
  ): Promise<T> => {
    const transaction = database.transaction(STORE_NAME, mode);
    return request(run(transaction.objectStore(STORE_NAME)));
  };

  return {
    get: (key) => transact("readonly", (store) => store.get(key) as IDBRequest<unknown>),
    put: async (key, value) => {
      await transact("readwrite", (store) => store.put(value, key));
    },
    delete: async (key) => {
      await transact("readwrite", (store) => store.delete(key));
    },
  };
}

function isCryptoKey(value: unknown): value is CryptoKey {
  return typeof CryptoKey !== "undefined" && value instanceof CryptoKey;
}

/**
 * Bytes out of whatever a structured-clone round trip handed back.
 *
 * `instanceof` is not enough: IndexedDB deserializes into its own realm, and
 * under jsdom the constructor a stored `Uint8Array` was built with is not the
 * one this module sees. `ArrayBuffer.isView` and the toString tag both look at
 * internal slots instead, so they answer correctly across realms.
 */
function asBytes(value: unknown): Uint8Array<ArrayBuffer> | null {
  if (ArrayBuffer.isView(value)) {
    // Copied into a buffer of our own: it detaches the result from whatever
    // realm (and whatever buffer kind) the store handed back.
    return copyOf(new Uint8Array(value.buffer as ArrayBuffer, value.byteOffset, value.byteLength));
  }
  const tag = Object.prototype.toString.call(value);
  if (tag === "[object ArrayBuffer]" || tag === "[object SharedArrayBuffer]") {
    return copyOf(new Uint8Array(value as ArrayBuffer));
  }
  return null;
}

function copyOf(bytes: Uint8Array): Uint8Array<ArrayBuffer> {
  const copy = new Uint8Array(bytes.length);
  copy.set(bytes);
  return copy;
}

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/**
 * Parses stored verification records, dropping anything malformed.
 *
 * Validated field by field even though this is our own data: what comes back
 * decides whether a `verified` badge is drawn, so a corrupt or truncated blob
 * must degrade to "no record" — which re-pins and warns — rather than to a
 * half-read object that renders a level nobody ever recorded. The one value
 * that must never be invented is `verified`, so the level is matched exactly
 * and anything else is thrown away with the record.
 */
export function parseVerificationRecords(json: string): VerificationRecords {
  const parsed: unknown = JSON.parse(json);
  if (typeof parsed !== "object" || parsed === null) {
    return {};
  }
  const records: VerificationRecords = {};
  for (const [userId, value] of Object.entries(parsed as Record<string, unknown>)) {
    if (typeof value !== "object" || value === null) {
      continue;
    }
    const record = value as Partial<VerificationRecord>;
    const keys = record.keys;
    if (
      record.userId !== userId ||
      !Array.isArray(keys) ||
      !keys.every((key) => typeof key === "string") ||
      (record.level !== "pinned" && record.level !== "verified") ||
      typeof record.at !== "number"
    ) {
      continue;
    }
    records[userId] = { userId, keys: [...keys], level: record.level, at: record.at };
  }
  return records;
}

/**
 * Parses a stored sent-message history, dropping anything malformed.
 *
 * Validated entry by entry for the same reason the records are, pointed at a
 * different risk: a half-read entry here would put text on the screen inside
 * the author's own bubble. An entry that fails any field is dropped, and the
 * consequence is the honest one — that bubble renders undecryptable, which is
 * exactly what it would have done before this store existed.
 */
function readSent(json: string): SentMessage[] {
  const parsed: unknown = JSON.parse(json);
  if (!Array.isArray(parsed)) {
    return [];
  }
  const sent: SentMessage[] = [];
  for (const value of parsed as unknown[]) {
    if (typeof value !== "object" || value === null) {
      continue;
    }
    const entry = value as Partial<SentMessage>;
    if (
      typeof entry.id !== "string" ||
      typeof entry.text !== "string" ||
      typeof entry.at !== "number"
    ) {
      continue;
    }
    sent.push({ id: entry.id, text: entry.text, at: entry.at });
  }
  return sent;
}

function readStoredState(value: unknown): StoredState | null {
  if (typeof value !== "object" || value === null) {
    return null;
  }
  const record = value as { iv?: unknown; ciphertext?: unknown };
  const iv = asBytes(record.iv);
  const ciphertext = asBytes(record.ciphertext);
  return iv === null || ciphertext === null ? null : { iv, ciphertext };
}

/** Names the Web Lock that serializes keystore writes across tabs. */
const KEYSTORE_LOCK = "hamlaneh.mls.keystore";

/**
 * Runs `work` while holding the keystore lock, or plainly when the browser has
 * no Web Locks API.
 *
 * **Every write in this file goes through here** — `putWrapped`, the wrapping
 * key's first-run creation, `saveBackupKey`, `deleteBackupKey` and `clear` —
 * which is what lets the claim be "serialized against every other writer in
 * this profile" rather than "serialized against most of them". A write that
 * reached `store.put` or `store.delete` directly would be the exception that
 * makes the invariant untrue, so there is no such call.
 *
 * Reads deliberately do not take it: a torn record cannot be produced by a
 * reader, and `readStoredState` already answers null for anything that is not
 * a whole one.
 *
 * The fallback is deliberate rather than a refusal: without the lock the only
 * loss is the cross-tab coordination this adds, and a browser that cannot
 * coordinate is not a reason to leave a single-tab user with no encryption at
 * all.
 */
async function withKeystoreLock<T>(work: () => Promise<T>): Promise<T> {
  // Typed through a shape where `locks` is optional rather than through
  // Navigator, which declares it as always present — it is not, on Safari
  // before 15.4 and in any non-secure context.
  const locks = (navigator as { locks?: LockManager }).locks;
  if (locks === undefined) {
    return work();
  }
  return locks.request(KEYSTORE_LOCK, work);
}

/**
 * The device state, encrypted at rest.
 *
 * Construct with {@link openKeystore} rather than directly: getting the
 * wrapping key is asynchronous and can fail, and a keystore that exists is one
 * whose key is already in hand.
 */
export class Keystore {
  private constructor(
    private readonly store: KeyValueStore,
    private readonly wrappingKey: CryptoKey,
  ) {}

  /**
   * Opens the keystore over `store`, generating the wrapping key on first
   * use. Resolves to null when the key can be neither read nor created — the
   * MLS-unavailable path.
   */
  static async open(store: KeyValueStore): Promise<Keystore | null> {
    // Typed as always present, and absent in reality on an insecure origin —
    // which is exactly the context this has to refuse rather than assume.
    const subtle = (globalThis.crypto as Crypto | undefined)?.subtle;
    if (subtle === undefined) {
      // No WebCrypto means no honest way to store this state. Refusing is the
      // only correct answer: writing the map unwrapped is not a fallback.
      console.warn("The MLS keystore needs WebCrypto, which this context does not provide.");
      return null;
    }
    // Under the lock, because get-then-generate-then-put is read-modify-write
    // and two tabs opening at once would both find no key, both make one, and
    // the second would overwrite the first — leaving whatever the first had
    // already written undecryptable, which reads to the service as "no stored
    // device" and silently starts a fresh one, losing every group.
    return withKeystoreLock(async () => {
      try {
        const existing = await store.get(WRAPPING_KEY_ID);
        if (isCryptoKey(existing)) {
          return new Keystore(store, existing);
        }
        const created = await subtle.generateKey({ name: "AES-GCM", length: 256 }, false, [
          "encrypt",
          "decrypt",
        ]);
        // `false` above is the load-bearing argument: non-extractable, so the
        // key can be used through this handle and never read out of it.
        await store.put(WRAPPING_KEY_ID, created);
        return new Keystore(store, created);
      } catch (error) {
        console.warn("Could not establish the MLS wrapping key:", error);
        return null;
      }
    });
  }

  /** Encrypts `bytes` under the wrapping key and writes them at `id`. */
  private async putWrapped(id: string, bytes: Uint8Array): Promise<void> {
    const iv = crypto.getRandomValues(new Uint8Array(IV_BYTES));
    const ciphertext = await crypto.subtle.encrypt(
      { name: "AES-GCM", iv },
      this.wrappingKey,
      copyOf(bytes),
    );
    const record: StoredState = { iv, ciphertext: new Uint8Array(ciphertext) };
    // Serialized against every other writer in this profile (see
    // withKeystoreLock for why "every" holds), so a write is never
    // interleaved with another tab's. It does NOT make two tabs share one
    // device: each still holds its own in wasm memory and the last one to
    // save still wins the stored copy. What the lock buys is that the stored
    // copy is always exactly one tab's state and never a torn mix of two.
    // One device per profile needs a SharedWorker and is filed as its own
    // roadmap item.
    await withKeystoreLock(() => this.store.put(id, record));
  }

  /** The plaintext at `id`, or null when there is none or it will not open. */
  private async getWrapped(id: string): Promise<Uint8Array | null> {
    const record = readStoredState(await this.store.get(id));
    if (record === null) {
      return null;
    }
    const plaintext = await crypto.subtle.decrypt(
      { name: "AES-GCM", iv: record.iv },
      this.wrappingKey,
      record.ciphertext,
    );
    return new Uint8Array(plaintext);
  }

  /** Encrypts and stores one device-state export. False when it could not. */
  async save(state: Uint8Array): Promise<boolean> {
    try {
      await this.putWrapped(STATE_ID, state);
      return true;
    } catch (error) {
      // A failed write costs this device its groups on the next reload, which
      // is recoverable (it is re-added and re-welcomed). Losing the session to
      // an exception would not be.
      console.warn("Could not store the MLS device state:", error);
      return false;
    }
  }

  /**
   * The stored state, or null when there is none — and also null when there
   * is one that will not decrypt. A record encrypted under a key this profile
   * no longer has is indistinguishable from no record at all, and treating it
   * as a fresh start is the only move that leaves the device usable.
   */
  async load(): Promise<Uint8Array | null> {
    try {
      return await this.getWrapped(STATE_ID);
    } catch (error) {
      console.warn("Could not read the stored MLS device state:", error);
      return null;
    }
  }

  /** Stores the verification records. False when they could not be written. */
  async saveRecords(records: VerificationRecords): Promise<boolean> {
    try {
      await this.putWrapped(RECORDS_ID, encoder.encode(JSON.stringify(records)));
      return true;
    } catch (error) {
      // Losing a write costs the user a re-pin — every peer reads as first
      // sight again on the next load, which warns more rather than less.
      console.warn("Could not store the MLS verification records:", error);
      return false;
    }
  }

  /**
   * The verification records, or an empty set when there are none.
   *
   * Unreadable records read as absent, exactly as an unreadable device state
   * does — and the consequence is the safe direction: every person is pinned
   * again on first sight, so the user is asked more questions rather than
   * fewer. Nothing here ever invents a `verified`.
   */
  async loadRecords(): Promise<VerificationRecords> {
    try {
      const bytes = await this.getWrapped(RECORDS_ID);
      return bytes === null ? {} : parseVerificationRecords(decoder.decode(bytes));
    } catch (error) {
      console.warn("Could not read the stored MLS verification records:", error);
      return {};
    }
  }

  /** Stores the sent-message history. False when it could not be written. */
  async saveSent(sent: readonly SentMessage[]): Promise<boolean> {
    try {
      await this.putWrapped(SENT_ID, encoder.encode(JSON.stringify(sent)));
      return true;
    } catch (error) {
      // A lost write costs the author their own words on the next reload —
      // the undecryptable state this store exists to replace, which is a
      // visible disappointment rather than a broken session.
      console.warn("Could not store the sent-message history:", error);
      return false;
    }
  }

  /**
   * The sent-message history, or an empty list when there is none.
   *
   * Also empty when there is one that will not decrypt or will not parse: a
   * history this profile can no longer open is indistinguishable from never
   * having had one, and the degradation is honest either way — the author's
   * own bubbles read as undecryptable, which is true of them.
   */
  async loadSent(): Promise<SentMessage[]> {
    try {
      const bytes = await this.getWrapped(SENT_ID);
      return bytes === null ? [] : readSent(decoder.decode(bytes));
    } catch (error) {
      console.warn("Could not read the sent-message history:", error);
      return [];
    }
  }

  /** Records how many key packages the directory holds for this device. */
  async saveKeyPackageCount(count: number): Promise<boolean> {
    try {
      await this.putWrapped(KEY_PACKAGE_COUNT_ID, encoder.encode(String(count)));
      return true;
    } catch (error) {
      // A lost write reads as "never published" next time, which republishes
      // a pool that did not need it — the same cost the policy set out to
      // avoid, paid once, and never a device with no packages.
      console.warn("Could not store the MLS key-package count:", error);
      return false;
    }
  }

  /**
   * The last recorded count, or null when this device has never published.
   *
   * Null for an unreadable or nonsensical value too, and that is the safe
   * direction: null publishes, and a device with a fresh pool is never the
   * failure mode — a device the directory has no packages for is.
   */
  async loadKeyPackageCount(): Promise<number | null> {
    try {
      const bytes = await this.getWrapped(KEY_PACKAGE_COUNT_ID);
      if (bytes === null) {
        return null;
      }
      const count = Number(decoder.decode(bytes));
      return Number.isInteger(count) && count >= 0 ? count : null;
    } catch (error) {
      console.warn("Could not read the MLS key-package count:", error);
      return null;
    }
  }

  /**
   * Stores the backup key handle. False when it could not be written — and
   * the caller must treat that as "backup is not on", because a device that
   * lost the handle cannot re-seal and the recovery key is already gone.
   */
  async saveBackupKey(key: CryptoKey): Promise<boolean> {
    try {
      await withKeystoreLock(() => this.store.put(BACKUP_KEY_ID, key));
      return true;
    } catch (error) {
      console.warn("Could not store the backup key:", error);
      return false;
    }
  }

  /** The backup key handle, or null when this device holds none. */
  async loadBackupKey(): Promise<CryptoKey | null> {
    try {
      const stored = await this.store.get(BACKUP_KEY_ID);
      return isCryptoKey(stored) ? stored : null;
    } catch (error) {
      console.warn("Could not read the backup key:", error);
      return null;
    }
  }

  /** Forgets the backup key handle — turning backup off, and signing out. */
  async deleteBackupKey(): Promise<void> {
    try {
      // Under the lock like every other write: a delete racing another tab's
      // save is the same interleaving a save racing a save is.
      await withKeystoreLock(() => this.store.delete(BACKUP_KEY_ID));
    } catch (error) {
      console.warn("Could not drop the backup key:", error);
    }
  }

  /** Stores the floor and the enrolment decision. False when it could not. */
  async saveBackupState(state: StoredBackupState): Promise<boolean> {
    try {
      await this.putWrapped(BACKUP_STATE_ID, encoder.encode(JSON.stringify(state)));
      return true;
    } catch (error) {
      // A lost floor write means the next fetched envelope is compared against
      // a stale number, which is the direction that accepts a rollback — so
      // the caller does not advance its in-memory floor past a failed write.
      console.warn("Could not store the backup floor:", error);
      return false;
    }
  }

  /** The floor and the decision, or the defaults when there are none. */
  async loadBackupState(): Promise<StoredBackupState> {
    try {
      const bytes = await this.getWrapped(BACKUP_STATE_ID);
      return bytes === null ? noBackupState : readBackupState(decoder.decode(bytes));
    } catch (error) {
      console.warn("Could not read the backup floor:", error);
      return noBackupState;
    }
  }

  /**
   * Drops the stored state (a different user signing in on this profile).
   *
   * The whole sequence runs inside ONE hold of the keystore lock, which is
   * what makes it a clear rather than seven deletes another tab can write
   * between: a save landing halfway through would leave the next person's
   * profile holding the previous one's records with no state, or a floor with
   * no key.
   */
  async clear(): Promise<void> {
    try {
      await withKeystoreLock(async () => {
        await this.store.delete(STATE_ID);
        // The records go with it. They are one person's trust decisions, and
        // leaving them for whoever signs in next would show that person a
        // `verified` badge nobody on this device ever earned.
        await this.store.delete(RECORDS_ID);
        // And the sent history, for a blunter reason than the records: it is
        // one person's own words in plaintext, and leaving it behind would hand
        // whoever signs in next the previous user's messages.
        await this.store.delete(SENT_ID);
        // And the key-package count, because the device that replaces this one
        // has published nothing. An inherited count would suppress its first
        // publish and leave it unaddable until something else replenished.
        await this.store.delete(KEY_PACKAGE_COUNT_ID);
        // And the backup: the key handle is one person's, and so is the floor.
        // Leaving the handle would let the next person's records be sealed under
        // a recovery key only the previous one was ever shown — a backup nobody
        // can open — and leaving the floor would have this profile refuse the
        // new person's own real backup as a rollback.
        await this.store.delete(BACKUP_KEY_ID);
        await this.store.delete(BACKUP_STATE_ID);
      });
    } catch (error) {
      console.warn("Could not clear the stored MLS device state:", error);
    }
  }
}

/** Opens the browser keystore, or null when this session cannot have one. */
export async function openKeystore(): Promise<Keystore | null> {
  const store = await openKeyValueStore();
  return store === null ? null : Keystore.open(store);
}
