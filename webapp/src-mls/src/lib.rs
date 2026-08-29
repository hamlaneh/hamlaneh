//! Hamlaneh's MLS wrapper: the product's only MLS-literate component
//! (ADR 006, decision 2).
//!
//! **Glue only.** Every cryptographic operation below is a call into OpenMLS
//! 0.9.0 on the `openmls_rust_crypto` provider. This crate chooses a
//! ciphersuite, moves bytes across the wasm boundary, and keeps a map of
//! group state. It never derives a key, never compares a MAC, never invents a
//! format. If a change to this file would require reasoning about a protocol
//! rule rather than about plumbing, it belongs upstream instead.
//!
//! Two shapes are worth stating up front because the whole API rests on them:
//!
//! - **A device, not a user, is an MLS leaf.** One signature keypair per
//!   browser profile. The credential identity is the *user* id, which is how
//!   a client answers "which leaves belong to the person who just left the
//!   channel" without the server ever listing anyone's devices.
//! - **A commit is not merged when it is created.** `add_members` and
//!   `remove_user` leave the group holding a pending commit; the caller
//!   submits the blob and then calls `commit_accepted` (the server took it)
//!   or `commit_rejected` (409 — somebody else won the epoch). Merging early
//!   would fork the group off the server's log.

mod frames;
mod storage;

use std::collections::HashSet;

use openmls::prelude::{tls_codec::*, *};
use openmls_basic_credential::SignatureKeyPair;
use openmls_rust_crypto::RustCrypto;
use openmls_traits::storage::StorageProvider as _;
use openmls_traits::OpenMlsProvider;
use wasm_bindgen::prelude::*;

use storage::KvStorage;

/// The ciphersuite, fixed. RFC 9420's mandatory-to-implement suite, the one
/// the integration spike measured, and the only one this build negotiates —
/// a per-group choice would be a downgrade surface for no benefit today.
const CIPHERSUITE: Ciphersuite = Ciphersuite::MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519;

/// Handle names inside the storage map. Keeping them there rather than
/// alongside it is what makes an export self-contained: restoring a device is
/// `import` plus three lookups, with nothing carried out of band.
const HANDLE_IDENTITY: &str = "identity";
const HANDLE_SIGNATURE_PUBLIC_KEY: &str = "signature_public_key";
const HANDLE_GROUP_IDS: &str = "group_ids";
const HANDLE_KEY_PACKAGE_BATCHES: &str = "key_package_batches";

/// How many published key-package batches keep their private init keys.
///
/// Not one: the server's replace-all publish deletes the *unclaimed* pool,
/// but a package claimed just before a replenish still has a Welcome in
/// flight, and dropping its private key would make that join fail. Not
/// unbounded either — every batch is 50 private init keys that stay in the
/// keystore forever, which is both growth and a forward-secrecy cost. Two is
/// the smallest number that covers a Welcome outliving one replenish.
const KEY_PACKAGE_BATCHES_RETAINED: usize = 2;

fn fail(context: &str, error: impl std::fmt::Debug) -> JsError {
    // Debug rather than Display: OpenMLS error enums are far more specific in
    // Debug, and these strings are read by developers, never by users — the
    // UI renders its own translated states.
    JsError::new(&format!("{context}: {error:?}"))
}

fn invalid(message: &str) -> JsError {
    JsError::new(message)
}

/// Crypto and randomness from `openmls_rust_crypto`, storage from this crate.
struct Provider {
    crypto: RustCrypto,
    storage: KvStorage,
}

impl OpenMlsProvider for Provider {
    type CryptoProvider = RustCrypto;
    type RandProvider = RustCrypto;
    type StorageProvider = KvStorage;

    fn storage(&self) -> &Self::StorageProvider {
        &self.storage
    }

    fn crypto(&self) -> &Self::CryptoProvider {
        &self.crypto
    }

    fn rand(&self) -> &Self::RandProvider {
        &self.crypto
    }
}

/// A commit the caller must now submit, with the Welcome it produced.
///
/// One Welcome covers every device added by the commit — MLS puts one
/// encrypted group-secrets entry per new leaf inside a single Welcome
/// message, so the same bytes are delivered to each added device and each
/// finds its own entry. That is why this is one blob and not a list.
#[wasm_bindgen]
pub struct CommitBundle {
    commit: Option<Vec<u8>>,
    welcome: Option<Vec<u8>>,
}

#[wasm_bindgen]
impl CommitBundle {
    /// The commit to submit, or `undefined` when the operation was a no-op —
    /// every device was already a member, or nobody matched the removal.
    /// A caller that submits nothing is the point: it is how a lost race
    /// stays quiet instead of committing an empty epoch.
    #[wasm_bindgen(getter)]
    pub fn commit(&self) -> Option<Vec<u8>> {
        self.commit.clone()
    }

    #[wasm_bindgen(getter)]
    pub fn welcome(&self) -> Option<Vec<u8>> {
        self.welcome.clone()
    }
}

/// One encrypted application message, plus the epoch the sender was at.
#[wasm_bindgen]
pub struct EncryptedMessage {
    epoch: u64,
    ciphertext: Vec<u8>,
}

#[wasm_bindgen]
impl EncryptedMessage {
    #[wasm_bindgen(getter)]
    pub fn epoch(&self) -> u64 {
        self.epoch
    }

    #[wasm_bindgen(getter)]
    pub fn ciphertext(&self) -> Vec<u8> {
        self.ciphertext.clone()
    }
}

/// This browser profile's MLS device: one signature keypair, one credential,
/// and every group it belongs to.
#[wasm_bindgen]
pub struct MlsDevice {
    provider: Provider,
    signer: SignatureKeyPair,
    credential: CredentialWithKey,
    identity: String,
}

#[wasm_bindgen]
impl MlsDevice {
    /// A brand-new device: fresh signature keypair, credential bound to
    /// `identity` (the user's id — see the module note on leaves).
    pub fn create(identity: &str) -> Result<MlsDevice, JsError> {
        let provider = Provider {
            crypto: RustCrypto::default(),
            storage: KvStorage::new(),
        };
        let signer = SignatureKeyPair::new(CIPHERSUITE.signature_algorithm())
            .map_err(|error| fail("could not generate a signature keypair", error))?;
        signer
            .store(provider.storage())
            .map_err(|error| fail("could not store the signature keypair", error))?;

        provider
            .storage()
            .put_handle(HANDLE_IDENTITY, identity.as_bytes())
            .map_err(|error| fail("could not store the identity", error))?;
        provider
            .storage()
            .put_handle(HANDLE_SIGNATURE_PUBLIC_KEY, signer.public())
            .map_err(|error| fail("could not store the signature public key", error))?;

        let credential = credential_for(identity, &signer);
        Ok(MlsDevice {
            provider,
            signer,
            credential,
            identity: identity.to_owned(),
        })
    }

    /// Rebuilds a device from an [`MlsDevice::export_state`] blob — the
    /// browser-reload path. Every handle it needs is inside the blob.
    pub fn restore(state: &[u8]) -> Result<MlsDevice, JsError> {
        let storage =
            KvStorage::import(state).map_err(|error| fail("the stored state is unreadable", error))?;
        let provider = Provider {
            crypto: RustCrypto::default(),
            storage,
        };

        let identity_bytes = provider
            .storage()
            .handle(HANDLE_IDENTITY)
            .map_err(|error| fail("could not read the identity", error))?
            .ok_or_else(|| invalid("the stored state carries no identity"))?;
        let identity = String::from_utf8(identity_bytes)
            .map_err(|error| fail("the stored identity is not UTF-8", error))?;

        let public_key = provider
            .storage()
            .handle(HANDLE_SIGNATURE_PUBLIC_KEY)
            .map_err(|error| fail("could not read the signature public key", error))?
            .ok_or_else(|| invalid("the stored state carries no signature public key"))?;

        let signer = SignatureKeyPair::read(
            provider.storage(),
            &public_key,
            CIPHERSUITE.signature_algorithm(),
        )
        .ok_or_else(|| invalid("the signature keypair is missing from the stored state"))?;

        let credential = credential_for(&identity, &signer);
        Ok(MlsDevice {
            provider,
            signer,
            credential,
            identity,
        })
    }

    /// The whole storage map, ready to be encrypted and written to IndexedDB.
    ///
    /// These bytes contain raw MLS secrets, including the signature private
    /// key. They must never be persisted or transmitted unwrapped.
    pub fn export_state(&self) -> Result<Vec<u8>, JsError> {
        self.provider
            .storage()
            .export()
            .map_err(|error| fail("could not export the state", error))
    }

    #[wasm_bindgen(getter)]
    pub fn identity(&self) -> String {
        self.identity.clone()
    }

    /// The device's MLS signature public key — what the server registers as
    /// this device's opaque identifier.
    pub fn signature_public_key(&self) -> Vec<u8> {
        self.signer.public().to_vec()
    }

    /// Every group id this device holds state for, packed (see `frames.rs`).
    pub fn group_ids(&self) -> Result<Vec<u8>, JsError> {
        Ok(frames::pack(&self.stored_group_ids()?))
    }

    /// Fresh single-use key packages for the directory, packed.
    ///
    /// Publishing is replace-all (openapi.yaml), so this generates the whole
    /// pool at once and retires the batches that have aged out.
    pub fn generate_key_packages(&mut self, count: u32) -> Result<Vec<u8>, JsError> {
        if count == 0 || count > 50 {
            return Err(invalid("a key-package batch is between 1 and 50 packages"));
        }
        let mut packages = Vec::with_capacity(count as usize);
        let mut refs = Vec::with_capacity(count as usize);
        for _ in 0..count {
            let bundle = KeyPackage::builder()
                .build(
                    CIPHERSUITE,
                    &self.provider,
                    &self.signer,
                    self.credential.clone(),
                )
                .map_err(|error| fail("could not build a key package", error))?;
            let hash_ref = bundle
                .key_package()
                .hash_ref(self.provider.crypto())
                .map_err(|error| fail("could not reference a key package", error))?;
            refs.push(hash_ref);
            packages.push(
                bundle
                    .key_package()
                    .tls_serialize_detached()
                    .map_err(|error| fail("could not serialize a key package", error))?,
            );
        }
        self.retire_old_key_packages(refs)?;
        Ok(frames::pack(&packages))
    }

    /// Creates the group locally, containing only this device.
    pub fn create_group(&mut self, group_id: &[u8]) -> Result<(), JsError> {
        if group_id.is_empty() || group_id.len() > 64 {
            return Err(invalid("a group id is between 1 and 64 bytes"));
        }
        if self.load_group(group_id)?.is_some() {
            return Err(invalid("this device already holds state for that group"));
        }
        let config = MlsGroupCreateConfig::builder()
            // The ratchet tree rides inside the Welcome, so a joiner needs
            // nothing else from the server — which is why the contract has no
            // tree-transfer endpoint.
            .use_ratchet_tree_extension(true)
            .ciphersuite(CIPHERSUITE)
            .build();

        MlsGroup::new_with_group_id(
            &self.provider,
            &self.signer,
            &config,
            GroupId::from_slice(group_id),
            self.credential.clone(),
        )
        .map_err(|error| fail("could not create the group", error))?;
        self.remember_group(group_id)
    }

    pub fn has_group(&self, group_id: &[u8]) -> Result<bool, JsError> {
        Ok(self.load_group(group_id)?.is_some())
    }

    /// The group's current epoch — this device's own count, not the server's.
    pub fn epoch(&self, group_id: &[u8]) -> Result<u64, JsError> {
        Ok(self.require_group(group_id)?.epoch().as_u64())
    }

    /// The credential identity of every member leaf, packed as UTF-8.
    ///
    /// This is how a client reconciles the group against the channel's member
    /// list without the server ever being asked who is in the group — the
    /// confidentiality authority reading itself (ADR 006, decision 2).
    pub fn member_identities(&self, group_id: &[u8]) -> Result<Vec<u8>, JsError> {
        let group = self.require_group(group_id)?;
        let identities: Vec<Vec<u8>> = group
            .members()
            .map(|member| member.credential.serialized_content().to_vec())
            .collect();
        Ok(frames::pack(&identities))
    }

    /// Adds every device whose key package is in `packed_key_packages` and is
    /// not already a member, in one commit.
    ///
    /// Already-present signature keys are skipped rather than added twice:
    /// two clients racing to add the same newcomer is the normal case, not an
    /// error, and the loser must end up submitting nothing.
    pub fn add_members(
        &mut self,
        group_id: &[u8],
        packed_key_packages: &[u8],
    ) -> Result<CommitBundle, JsError> {
        let mut group = self.require_group(group_id)?;
        let present: HashSet<Vec<u8>> = group
            .members()
            .map(|member| member.signature_key.to_vec())
            .collect();

        let mut to_add = Vec::new();
        for bytes in frames::unpack(packed_key_packages).map_err(invalid)? {
            let key_package = KeyPackageIn::tls_deserialize_exact(&bytes)
                .map_err(|error| fail("a key package could not be parsed", error))?
                .validate(self.provider.crypto(), ProtocolVersion::Mls10)
                .map_err(|error| fail("a key package failed validation", error))?;
            if present.contains(key_package.leaf_node().signature_key().as_slice()) {
                continue;
            }
            to_add.push(key_package);
        }
        if to_add.is_empty() {
            return Ok(CommitBundle {
                commit: None,
                welcome: None,
            });
        }

        let (commit, welcome, _group_info) = group
            .add_members(&self.provider, &self.signer, &to_add)
            .map_err(|error| fail("could not build the add commit", error))?;
        Ok(CommitBundle {
            commit: Some(
                commit
                    .tls_serialize_detached()
                    .map_err(|error| fail("could not serialize the commit", error))?,
            ),
            welcome: Some(
                welcome
                    .tls_serialize_detached()
                    .map_err(|error| fail("could not serialize the welcome", error))?,
            ),
        })
    }

    /// Removes every leaf whose credential identity is `identity` — that is,
    /// all of one person's devices, in one commit.
    ///
    /// Nobody matching is a no-op with no commit: another member removing
    /// them first is the expected race, not a failure.
    pub fn remove_user(&mut self, group_id: &[u8], identity: &str) -> Result<CommitBundle, JsError> {
        let mut group = self.require_group(group_id)?;
        let own_leaf = group.own_leaf_index();
        let targets: Vec<LeafNodeIndex> = group
            .members()
            .filter(|member| {
                member.index != own_leaf
                    && member.credential.serialized_content() == identity.as_bytes()
            })
            .map(|member| member.index)
            .collect();
        if targets.is_empty() {
            return Ok(CommitBundle {
                commit: None,
                welcome: None,
            });
        }

        let (commit, _welcome, _group_info) = group
            .remove_members(&self.provider, &self.signer, &targets)
            .map_err(|error| fail("could not build the remove commit", error))?;
        Ok(CommitBundle {
            commit: Some(
                commit
                    .tls_serialize_detached()
                    .map_err(|error| fail("could not serialize the commit", error))?,
            ),
            welcome: None,
        })
    }

    /// The server took our commit: advance to the epoch we built.
    pub fn commit_accepted(&mut self, group_id: &[u8]) -> Result<(), JsError> {
        let mut group = self.require_group(group_id)?;
        group
            .merge_pending_commit(&self.provider)
            .map_err(|error| fail("could not merge the accepted commit", error))
    }

    /// The server refused our commit (409): drop it, unchanged epoch. The
    /// caller then fetches the log, applies what it missed, and rebuilds.
    pub fn commit_rejected(&mut self, group_id: &[u8]) -> Result<(), JsError> {
        let mut group = self.require_group(group_id)?;
        group
            .clear_pending_commit(self.provider.storage())
            .map_err(|error| fail("could not clear the rejected commit", error))
    }

    /// Applies a commit from the log — somebody else's, or our own.
    ///
    /// Our own needs handling because the server's log advances when it
    /// answers 201 while this device advances when `commit_accepted` runs,
    /// and a crash between the two leaves a device that must read its own
    /// commit back. MLS cannot process a self-authored commit, so processing
    /// is attempted first and a pending commit is the answer to its failure:
    /// the log holding a commit at our epoch while we hold one pending means
    /// ours is the one that landed, and merging it is exactly what the lost
    /// `commit_accepted` would have done. Clearing the pending commit before
    /// processing — which is what this did before — threw away the only copy
    /// of the state needed to recover, and the channel could never advance
    /// again.
    ///
    /// When processing succeeds the incoming commit is somebody else's and
    /// ours has lost, so the pending commit is cleared then rather than now.
    pub fn apply_commit(&mut self, group_id: &[u8], message: &[u8]) -> Result<(), JsError> {
        let mut group = self.require_group(group_id)?;

        let processed = match self.process(&mut group, message) {
            Ok(processed) => processed,
            Err(error) => {
                if group.pending_commit().is_some() {
                    return group
                        .merge_pending_commit(&self.provider)
                        .map_err(|error| fail("could not merge our own accepted commit", error));
                }
                return Err(error);
            }
        };

        group
            .clear_pending_commit(self.provider.storage())
            .map_err(|error| fail("could not clear a superseded commit", error))?;

        match processed.into_content() {
            ProcessedMessageContent::StagedCommitMessage(staged) => group
                .merge_staged_commit(&self.provider, *staged)
                .map_err(|error| fail("could not merge the commit", error)),
            ProcessedMessageContent::ProposalMessage(proposal) => group
                .store_pending_proposal(self.provider.storage(), *proposal)
                .map_err(|error| fail("could not store the proposal", error)),
            // An application message or an external proposal in the commit
            // log is not a thing this client produces; ignoring it keeps a
            // confused peer from stalling everyone else's catch-up.
            _ => Ok(()),
        }
    }

    /// Joins the group a Welcome names, and returns its group id.
    pub fn join_from_welcome(&mut self, welcome: &[u8]) -> Result<Vec<u8>, JsError> {
        let message = MlsMessageIn::tls_deserialize_exact(welcome)
            .map_err(|error| fail("the welcome could not be parsed", error))?;
        let welcome = match message.extract() {
            MlsMessageBodyIn::Welcome(welcome) => welcome,
            _ => return Err(invalid("that message is not a welcome")),
        };
        let staged = StagedWelcome::new_from_welcome(
            &self.provider,
            // Not `default()`: the join config is what a joined group carries
            // forward, so a device that joined with the ratchet-tree
            // extension off would produce tree-less Welcomes of its own and
            // nobody it added could join. Caught exactly that way, by the
            // three-party case in wasm.roundtrip.test.ts.
            &join_config(),
            welcome,
            // None: the ratchet tree rides in the group's extension.
            None,
        )
        .map_err(|error| fail("the welcome could not be staged", error))?;
        let group = staged
            .into_group(&self.provider)
            .map_err(|error| fail("could not join from the welcome", error))?;
        let group_id = group.group_id().as_slice().to_vec();
        self.remember_group(&group_id)?;
        Ok(group_id)
    }

    /// Encrypts one message, reporting the epoch it was sealed at.
    pub fn encrypt(&mut self, group_id: &[u8], plaintext: &str) -> Result<EncryptedMessage, JsError> {
        let mut group = self.require_group(group_id)?;
        let epoch = group.epoch().as_u64();
        let message = group
            .create_message(&self.provider, &self.signer, plaintext.as_bytes())
            .map_err(|error| fail("could not encrypt the message", error))?;
        Ok(EncryptedMessage {
            epoch,
            ciphertext: message
                .tls_serialize_detached()
                .map_err(|error| fail("could not serialize the ciphertext", error))?,
        })
    }

    /// Decrypts one message.
    ///
    /// Failure here is an ordinary MLS condition, not a bug: a message from
    /// before this device joined, or from an epoch whose secrets have been
    /// dropped, cannot be opened by anyone holding this state. The caller
    /// renders that honestly rather than treating it as an error.
    pub fn decrypt(&mut self, group_id: &[u8], ciphertext: &[u8]) -> Result<String, JsError> {
        let mut group = self.require_group(group_id)?;
        let processed = self.process(&mut group, ciphertext)?;
        match processed.into_content() {
            ProcessedMessageContent::ApplicationMessage(message) => {
                String::from_utf8(message.into_bytes())
                    .map_err(|error| fail("the decrypted message is not UTF-8", error))
            }
            _ => Err(invalid("that message is not an application message")),
        }
    }
}

/* ── internals ───────────────────────────────────────────────────────────
 * Not exported to JS: a second `impl` block without #[wasm_bindgen].
 */

impl MlsDevice {
    fn process(
        &self,
        group: &mut MlsGroup,
        bytes: &[u8],
    ) -> Result<ProcessedMessage, JsError> {
        let message = MlsMessageIn::tls_deserialize_exact(bytes)
            .map_err(|error| fail("the message could not be parsed", error))?;
        let protocol = message
            .try_into_protocol_message()
            .map_err(|error| fail("that is not a protocol message", error))?;
        group
            .process_message(&self.provider, protocol)
            .map_err(|error| fail("the message could not be processed", error))
    }

    fn load_group(&self, group_id: &[u8]) -> Result<Option<MlsGroup>, JsError> {
        MlsGroup::load(self.provider.storage(), &GroupId::from_slice(group_id))
            .map_err(|error| fail("could not load the group", error))
    }

    /// Groups are loaded per call rather than cached: OpenMLS writes group
    /// state through to the provider on every mutation, so storage is the
    /// truth and a cached handle would only be a way to disagree with it.
    fn require_group(&self, group_id: &[u8]) -> Result<MlsGroup, JsError> {
        self.load_group(group_id)?
            .ok_or_else(|| invalid("this device holds no state for that group"))
    }

    fn stored_group_ids(&self) -> Result<Vec<Vec<u8>>, JsError> {
        match self
            .provider
            .storage()
            .handle(HANDLE_GROUP_IDS)
            .map_err(|error| fail("could not read the group ids", error))?
        {
            Some(packed) => frames::unpack(&packed).map_err(invalid),
            None => Ok(Vec::new()),
        }
    }

    fn remember_group(&self, group_id: &[u8]) -> Result<(), JsError> {
        let mut ids = self.stored_group_ids()?;
        if ids.iter().any(|id| id == group_id) {
            return Ok(());
        }
        ids.push(group_id.to_vec());
        self.provider
            .storage()
            .put_handle(HANDLE_GROUP_IDS, &frames::pack(&ids))
            .map_err(|error| fail("could not record the group id", error))
    }

    /// Records the new batch of key-package references and deletes the
    /// private material of every batch that has aged out.
    fn retire_old_key_packages(&self, new_batch: Vec<KeyPackageRef>) -> Result<(), JsError> {
        let stored = self
            .provider
            .storage()
            .handle(HANDLE_KEY_PACKAGE_BATCHES)
            .map_err(|error| fail("could not read the key-package batches", error))?;
        let mut batches: Vec<Vec<KeyPackageRef>> = match stored {
            Some(bytes) => serde_json::from_slice(&bytes)
                .map_err(|error| fail("the key-package batch record is unreadable", error))?,
            None => Vec::new(),
        };
        batches.push(new_batch);

        while batches.len() > KEY_PACKAGE_BATCHES_RETAINED {
            for hash_ref in batches.remove(0) {
                self.provider
                    .storage()
                    .delete_key_package(&hash_ref)
                    .map_err(|error| fail("could not delete a retired key package", error))?;
            }
        }

        let encoded = serde_json::to_vec(&batches)
            .map_err(|error| fail("could not record the key-package batches", error))?;
        self.provider
            .storage()
            .put_handle(HANDLE_KEY_PACKAGE_BATCHES, &encoded)
            .map_err(|error| fail("could not record the key-package batches", error))
    }
}

/// The join config every group in this app runs with. Its one setting is the
/// ratchet-tree extension, and it has to be the same on creation and on join:
/// see the comment in `join_from_welcome`.
fn join_config() -> MlsGroupJoinConfig {
    MlsGroupJoinConfig::builder()
        .use_ratchet_tree_extension(true)
        .build()
}

fn credential_for(identity: &str, signer: &SignatureKeyPair) -> CredentialWithKey {
    CredentialWithKey {
        credential: BasicCredential::new(identity.as_bytes().to_vec()).into(),
        signature_key: signer.public().into(),
    }
}
