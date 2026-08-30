/**
 * Loading the MLS wrapper, and the only place in the app that knows it is
 * WebAssembly.
 *
 * The chunk is ~480 KB gzipped — roughly the size of the rest of the
 * application (docs/spikes/mls-wasm-integration.md §2) — so it is imported
 * dynamically and only when someone actually opens an encrypted
 * conversation. Nothing on the login or plaintext-chat path pays for it.
 *
 * The import specifier is the `hamlaneh-mls` alias (vite.config.ts), typed by
 * the ambient declaration in `hamlaneh-mls.d.ts`. That indirection is what
 * lets `tsc` and the unit tests run against a checkout where the crate has
 * never been built: the types are always present, the artifact is resolved
 * only when this function is actually called.
 */

/** The `MlsDevice` surface, as `webapp/src-mls/src/lib.rs` exports it. */
export interface MlsDeviceHandle {
  readonly identity: string;
  export_state: () => Uint8Array;
  signature_public_key: () => Uint8Array;
  group_ids: () => Uint8Array;
  generate_key_packages: (count: number) => Uint8Array;
  create_group: (groupId: Uint8Array) => void;
  has_group: (groupId: Uint8Array) => boolean;
  epoch: (groupId: Uint8Array) => bigint;
  /** 32 exporter bytes for the current epoch — the media key (ADR 009). */
  exporter: (groupId: Uint8Array) => Uint8Array;
  member_identities: (groupId: Uint8Array) => Uint8Array;
  member_signature_keys: (groupId: Uint8Array) => Uint8Array;
  add_members: (groupId: Uint8Array, packedKeyPackages: Uint8Array) => CommitBundleHandle;
  retain_leaves: (groupId: Uint8Array, packedAllowedKeys: Uint8Array) => CommitBundleHandle;
  commit_accepted: (groupId: Uint8Array) => void;
  commit_rejected: (groupId: Uint8Array) => void;
  apply_commit: (groupId: Uint8Array, message: Uint8Array) => void;
  join_from_welcome: (welcome: Uint8Array) => Uint8Array;
  encrypt: (groupId: Uint8Array, plaintext: string) => EncryptedMessageHandle;
  decrypt: (groupId: Uint8Array, ciphertext: Uint8Array) => string;
}

export interface CommitBundleHandle {
  readonly commit: Uint8Array | undefined;
  readonly welcome: Uint8Array | undefined;
}

export interface EncryptedMessageHandle {
  readonly epoch: bigint;
  readonly ciphertext: Uint8Array;
}

/** The two constructors the wrapper exposes as statics. */
export interface MlsModule {
  create: (identity: string) => MlsDeviceHandle;
  restore: (state: Uint8Array) => MlsDeviceHandle;
}

let loading: Promise<MlsModule> | null = null;
let override: MlsModule | null = null;

/**
 * Test seam. Unit tests drive the service against an in-memory double so they
 * do not need the compiled crate; `wasm.roundtrip.test.ts` is where the real
 * artifact is exercised.
 */
export function setMlsModule(module: MlsModule | null): void {
  override = module;
  loading = null;
}

/**
 * Resolves to the wrapper, loading it on first use.
 *
 * Rejects when the artifact is absent (a checkout where `src-mls/build.sh` has
 * not run) or fails to instantiate. Callers turn that into the
 * MLS-unavailable state — never into a crash.
 */
export async function loadMlsModule(): Promise<MlsModule> {
  if (override !== null) {
    return override;
  }
  loading ??= import("hamlaneh-mls").then((module) => ({
    create: (identity: string) => module.MlsDevice.create(identity),
    restore: (state: Uint8Array) => module.MlsDevice.restore(state),
  }));
  return loading;
}
