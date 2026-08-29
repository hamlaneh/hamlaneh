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

const DATABASE_NAME = "hamlaneh-mls";
const DATABASE_VERSION = 1;
const STORE_NAME = "device";
const WRAPPING_KEY_ID = "wrapping-key";
const STATE_ID = "state";
const IV_BYTES = 12;

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

  /** Encrypts and stores one device-state export. False when it could not. */
  async save(state: Uint8Array): Promise<boolean> {
    try {
      const iv = crypto.getRandomValues(new Uint8Array(IV_BYTES));
      const ciphertext = await crypto.subtle.encrypt(
        { name: "AES-GCM", iv },
        this.wrappingKey,
        copyOf(state),
      );
      const record: StoredState = { iv, ciphertext: new Uint8Array(ciphertext) };
      // Serialized against every other writer in this profile, so a write is
      // never interleaved with another tab's. It does NOT make two tabs share
      // one device: each still holds its own in wasm memory and the last one
      // to save still wins the stored copy. What the lock buys is that the
      // stored copy is always exactly one tab's state and never a torn mix of
      // two. One device per profile needs a SharedWorker and is filed as its
      // own roadmap item.
      await withKeystoreLock(() => this.store.put(STATE_ID, record));
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
      const record = readStoredState(await this.store.get(STATE_ID));
      if (record === null) {
        return null;
      }
      const plaintext = await crypto.subtle.decrypt(
        { name: "AES-GCM", iv: record.iv },
        this.wrappingKey,
        record.ciphertext,
      );
      return new Uint8Array(plaintext);
    } catch (error) {
      console.warn("Could not read the stored MLS device state:", error);
      return null;
    }
  }

  /** Drops the stored state (a different user signing in on this profile). */
  async clear(): Promise<void> {
    try {
      await this.store.delete(STATE_ID);
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
