/**
 * Ambient types for the compiled MLS wrapper.
 *
 * `webapp/src-mls/pkg/` is build output and is not committed, so its generated
 * `.d.ts` cannot be what the app typechecks against — a fresh clone would not
 * compile. This declaration is the contract instead: it must match
 * `webapp/src-mls/src/lib.rs`, and the round-trip test is what proves it does.
 *
 * The specifier is an alias to `src-mls/pkg/hamlaneh_mls.js` (vite.config.ts).
 */
declare module "hamlaneh-mls" {
  export class CommitBundle {
    readonly commit: Uint8Array | undefined;
    readonly welcome: Uint8Array | undefined;
    free(): void;
  }

  export class EncryptedMessage {
    readonly epoch: bigint;
    readonly ciphertext: Uint8Array;
    free(): void;
  }

  export class MlsDevice {
    static create(identity: string): MlsDevice;
    static restore(state: Uint8Array): MlsDevice;

    readonly identity: string;
    export_state(): Uint8Array;
    signature_public_key(): Uint8Array;
    group_ids(): Uint8Array;
    generate_key_packages(count: number): Uint8Array;
    create_group(groupId: Uint8Array): void;
    has_group(groupId: Uint8Array): boolean;
    epoch(groupId: Uint8Array): bigint;
    member_identities(groupId: Uint8Array): Uint8Array;
    add_members(groupId: Uint8Array, packedKeyPackages: Uint8Array): CommitBundle;
    remove_user(groupId: Uint8Array, identity: string): CommitBundle;
    commit_accepted(groupId: Uint8Array): void;
    commit_rejected(groupId: Uint8Array): void;
    apply_commit(groupId: Uint8Array, message: Uint8Array): void;
    join_from_welcome(welcome: Uint8Array): Uint8Array;
    encrypt(groupId: Uint8Array, plaintext: string): EncryptedMessage;
    decrypt(groupId: Uint8Array, ciphertext: Uint8Array): string;
    free(): void;
  }
}
