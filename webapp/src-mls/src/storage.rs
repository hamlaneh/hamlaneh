//! The one thing OpenMLS asks an application to supply: somewhere to put
//! group state.
//!
//! This is an implementation of `openmls_traits::storage::StorageProvider`
//! over a plain in-memory key-value map that this crate owns. It is
//! deliberately not `openmls_rust_crypto::MemoryStorage`: that type's map is
//! reachable only through its public `values` field, which is an
//! implementation detail nobody has promised to keep, and its serialization
//! helpers live behind `test-utils`. The trait is the stable surface, so the
//! trait is what we implement (docs/spikes/mls-wasm-integration.md §4).
//!
//! No cryptography happens here. Every value handed to `write_*` was produced
//! by OpenMLS and is stored verbatim; this file decides only where bytes go.
//!
//! **The values include raw MLS secrets in the clear** — the signature private
//! key, epoch secrets, message secrets. That is a property of the trait, not a
//! choice made here: OpenMLS hands the provider unwrapped key material. What
//! protects those bytes once they leave this map is the keystore at rest
//! (`webapp/src/mls/keystore.ts`), which encrypts every export before it
//! reaches IndexedDB.

use std::collections::BTreeMap;
use std::fmt;
use std::sync::RwLock;

use openmls_traits::storage::{traits, StorageProvider, CURRENT_VERSION};
use serde::de::DeserializeOwned;
use serde::Serialize;

/// Everything that can go wrong in a map lookup: a value that will not
/// serialize, or a poisoned lock. Both are bugs rather than conditions a
/// caller can recover from, so they share one variant each and no payload.
#[derive(Debug, PartialEq, Eq, Clone, Copy)]
pub enum KvStorageError {
    /// A value could not be encoded to, or decoded from, its stored form.
    Serialization,
    /// The lock guarding the map was poisoned by a panic in another borrow.
    Poisoned,
}

impl fmt::Display for KvStorageError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Serialization => write!(f, "could not serialize a stored value"),
            Self::Poisoned => write!(f, "the storage lock was poisoned"),
        }
    }
}

impl std::error::Error for KvStorageError {}

impl From<serde_json::Error> for KvStorageError {
    fn from(_: serde_json::Error) -> Self {
        Self::Serialization
    }
}

type Result<T> = std::result::Result<T, KvStorageError>;

/* ── labels ──────────────────────────────────────────────────────────────
 * One per addressable kind of value. They are namespacing, not a wire
 * format: nothing outside this crate ever sees a key, because the whole map
 * is exported and imported wholesale.
 */

const JOIN_CONFIG: &str = "MlsGroupJoinConfig";
const OWN_LEAF_NODES: &str = "OwnLeafNodes";
const QUEUED_PROPOSAL: &str = "QueuedProposal";
const PROPOSAL_QUEUE_REFS: &str = "ProposalQueueRefs";
const TREE: &str = "Tree";
const INTERIM_TRANSCRIPT_HASH: &str = "InterimTranscriptHash";
const GROUP_CONTEXT: &str = "GroupContext";
const CONFIRMATION_TAG: &str = "ConfirmationTag";
const GROUP_STATE: &str = "GroupState";
const MESSAGE_SECRETS: &str = "MessageSecrets";
const RESUMPTION_PSK_STORE: &str = "ResumptionPsk";
const OWN_LEAF_NODE_INDEX: &str = "OwnLeafNodeIndex";
const GROUP_EPOCH_SECRETS: &str = "GroupEpochSecrets";
const SIGNATURE_KEY_PAIR: &str = "SignatureKeyPair";
const ENCRYPTION_KEY_PAIR: &str = "EncryptionKeyPair";
const EPOCH_KEY_PAIRS: &str = "EpochKeyPairs";
const KEY_PACKAGE: &str = "KeyPackage";
const PSK: &str = "Psk";

/// Reserved prefix for the handles this crate keeps alongside OpenMLS's own
/// entries — the device's signature public key and the list of group ids.
/// Both are needed to find a way back into the map after a restart
/// (`SignatureKeyPair::read` cannot enumerate, `MlsGroup::load` needs a
/// GroupId), and keeping them *in* the map is what lets an export be exactly
/// the map and nothing else.
const WRAPPER: &str = "hamlaneh";

/// An in-memory key-value map with an OpenMLS storage provider over it.
///
/// `BTreeMap` rather than `HashMap` so an export is byte-identical for
/// identical state — that makes the round-trip test able to compare exports,
/// and it costs nothing at this size (a two-member group is ~12 entries).
#[derive(Debug, Default)]
pub struct KvStorage {
    values: RwLock<BTreeMap<Vec<u8>, Vec<u8>>>,
}

impl KvStorage {
    pub fn new() -> Self {
        Self::default()
    }

    /* ── the wrapper's own handles ─────────────────────────────────────── */

    fn wrapper_key(name: &str) -> Vec<u8> {
        // Not via serde: these keys must never collide with a trait key, and
        // the trait keys are all JSON arrays starting with the version number.
        format!("{WRAPPER}:{name}").into_bytes()
    }

    pub fn put_handle(&self, name: &str, value: &[u8]) -> Result<()> {
        let mut values = self.values.write().map_err(|_| KvStorageError::Poisoned)?;
        values.insert(Self::wrapper_key(name), value.to_vec());
        Ok(())
    }

    pub fn handle(&self, name: &str) -> Result<Option<Vec<u8>>> {
        let values = self.values.read().map_err(|_| KvStorageError::Poisoned)?;
        Ok(values.get(&Self::wrapper_key(name)).cloned())
    }

    /* ── wholesale export / import ─────────────────────────────────────── */

    /// The whole map, length-prefixed: `count, (klen, k, vlen, v)*`, all
    /// lengths big-endian u32. Deliberately not JSON — the values are
    /// arbitrary bytes, and a framing this small needs no dependency and no
    /// escaping.
    pub fn export(&self) -> Result<Vec<u8>> {
        let values = self.values.read().map_err(|_| KvStorageError::Poisoned)?;
        let mut out = Vec::new();
        out.extend_from_slice(&(values.len() as u32).to_be_bytes());
        for (key, value) in values.iter() {
            out.extend_from_slice(&(key.len() as u32).to_be_bytes());
            out.extend_from_slice(key);
            out.extend_from_slice(&(value.len() as u32).to_be_bytes());
            out.extend_from_slice(value);
        }
        Ok(out)
    }

    /// Replaces the map with the contents of an `export`. A truncated or
    /// malformed blob is an error, never a partially loaded map.
    pub fn import(blob: &[u8]) -> Result<Self> {
        let mut rest = blob;
        let count = take_u32(&mut rest)?;
        let mut map = BTreeMap::new();
        for _ in 0..count {
            let key = take_bytes(&mut rest)?;
            let value = take_bytes(&mut rest)?;
            map.insert(key, value);
        }
        if !rest.is_empty() {
            return Err(KvStorageError::Serialization);
        }
        Ok(Self {
            values: RwLock::new(map),
        })
    }

    /* ── primitives every trait method is built from ───────────────────── */

    fn key_of<K: Serialize>(label: &str, key: &K) -> Result<Vec<u8>> {
        Ok(serde_json::to_vec(&(CURRENT_VERSION, label, key))?)
    }

    fn write<K: Serialize, V: Serialize>(&self, label: &str, key: &K, value: &V) -> Result<()> {
        let encoded = serde_json::to_vec(value)?;
        let mut values = self.values.write().map_err(|_| KvStorageError::Poisoned)?;
        values.insert(Self::key_of(label, key)?, encoded);
        Ok(())
    }

    // Bounded on DeserializeOwned rather than on Entity: the trait's epoch
    // key-pair methods store a *list* of entities, and a Vec of entities is
    // not itself one.
    fn read<K: Serialize, V: DeserializeOwned>(
        &self,
        label: &str,
        key: &K,
    ) -> Result<Option<V>> {
        let values = self.values.read().map_err(|_| KvStorageError::Poisoned)?;
        match values.get(&Self::key_of(label, key)?) {
            Some(bytes) => Ok(Some(serde_json::from_slice(bytes)?)),
            None => Ok(None),
        }
    }

    fn delete<K: Serialize>(&self, label: &str, key: &K) -> Result<()> {
        let mut values = self.values.write().map_err(|_| KvStorageError::Poisoned)?;
        values.remove(&Self::key_of(label, key)?);
        Ok(())
    }

    /// Lists are stored as a JSON array of the members' own JSON encodings, so
    /// appending never has to know the element type.
    fn read_raw_list<K: Serialize>(&self, label: &str, key: &K) -> Result<Vec<Vec<u8>>> {
        let values = self.values.read().map_err(|_| KvStorageError::Poisoned)?;
        match values.get(&Self::key_of(label, key)?) {
            Some(bytes) => Ok(serde_json::from_slice(bytes)?),
            None => Ok(Vec::new()),
        }
    }

    fn write_raw_list<K: Serialize>(&self, label: &str, key: &K, list: &[Vec<u8>]) -> Result<()> {
        let encoded = serde_json::to_vec(list)?;
        let mut values = self.values.write().map_err(|_| KvStorageError::Poisoned)?;
        values.insert(Self::key_of(label, key)?, encoded);
        Ok(())
    }

    fn append<K: Serialize, V: Serialize>(&self, label: &str, key: &K, value: &V) -> Result<()> {
        let mut list = self.read_raw_list(label, key)?;
        list.push(serde_json::to_vec(value)?);
        self.write_raw_list(label, key, &list)
    }

    fn remove_from_list<K: Serialize, V: Serialize>(
        &self,
        label: &str,
        key: &K,
        value: &V,
    ) -> Result<()> {
        let encoded = serde_json::to_vec(value)?;
        let mut list = self.read_raw_list(label, key)?;
        if let Some(index) = list.iter().position(|entry| entry == &encoded) {
            list.remove(index);
        }
        self.write_raw_list(label, key, &list)
    }

    fn read_list<K: Serialize, V: DeserializeOwned>(
        &self,
        label: &str,
        key: &K,
    ) -> Result<Vec<V>> {
        self.read_raw_list(label, key)?
            .iter()
            .map(|bytes| serde_json::from_slice(bytes).map_err(KvStorageError::from))
            .collect()
    }
}

fn take_u32(rest: &mut &[u8]) -> Result<usize> {
    if rest.len() < 4 {
        return Err(KvStorageError::Serialization);
    }
    let (head, tail) = rest.split_at(4);
    *rest = tail;
    let bytes: [u8; 4] = head.try_into().map_err(|_| KvStorageError::Serialization)?;
    Ok(u32::from_be_bytes(bytes) as usize)
}

fn take_bytes(rest: &mut &[u8]) -> Result<Vec<u8>> {
    let len = take_u32(rest)?;
    if rest.len() < len {
        return Err(KvStorageError::Serialization);
    }
    let (head, tail) = rest.split_at(len);
    *rest = tail;
    Ok(head.to_vec())
}

impl StorageProvider<CURRENT_VERSION> for KvStorage {
    type Error = KvStorageError;

    /* ── writers ───────────────────────────────────────────────────────── */

    fn write_mls_join_config<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        MlsGroupJoinConfig: traits::MlsGroupJoinConfig<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        config: &MlsGroupJoinConfig,
    ) -> Result<()> {
        self.write(JOIN_CONFIG, group_id, config)
    }

    fn append_own_leaf_node<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        LeafNode: traits::LeafNode<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        leaf_node: &LeafNode,
    ) -> Result<()> {
        self.append(OWN_LEAF_NODES, group_id, leaf_node)
    }

    fn queue_proposal<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        ProposalRef: traits::ProposalRef<CURRENT_VERSION>,
        QueuedProposal: traits::QueuedProposal<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        proposal_ref: &ProposalRef,
        proposal: &QueuedProposal,
    ) -> Result<()> {
        self.write(QUEUED_PROPOSAL, &(group_id, proposal_ref), proposal)?;
        self.append(PROPOSAL_QUEUE_REFS, group_id, proposal_ref)
    }

    fn write_tree<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        TreeSync: traits::TreeSync<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        tree: &TreeSync,
    ) -> Result<()> {
        self.write(TREE, group_id, tree)
    }

    fn write_interim_transcript_hash<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        InterimTranscriptHash: traits::InterimTranscriptHash<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        interim_transcript_hash: &InterimTranscriptHash,
    ) -> Result<()> {
        self.write(INTERIM_TRANSCRIPT_HASH, group_id, interim_transcript_hash)
    }

    fn write_context<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        GroupContext: traits::GroupContext<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        group_context: &GroupContext,
    ) -> Result<()> {
        self.write(GROUP_CONTEXT, group_id, group_context)
    }

    fn write_confirmation_tag<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        ConfirmationTag: traits::ConfirmationTag<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        confirmation_tag: &ConfirmationTag,
    ) -> Result<()> {
        self.write(CONFIRMATION_TAG, group_id, confirmation_tag)
    }

    fn write_group_state<
        GroupState: traits::GroupState<CURRENT_VERSION>,
        GroupId: traits::GroupId<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        group_state: &GroupState,
    ) -> Result<()> {
        self.write(GROUP_STATE, group_id, group_state)
    }

    fn write_message_secrets<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        MessageSecrets: traits::MessageSecrets<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        message_secrets: &MessageSecrets,
    ) -> Result<()> {
        self.write(MESSAGE_SECRETS, group_id, message_secrets)
    }

    fn write_resumption_psk_store<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        ResumptionPskStore: traits::ResumptionPskStore<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        resumption_psk_store: &ResumptionPskStore,
    ) -> Result<()> {
        self.write(RESUMPTION_PSK_STORE, group_id, resumption_psk_store)
    }

    fn write_own_leaf_index<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        LeafNodeIndex: traits::LeafNodeIndex<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        own_leaf_index: &LeafNodeIndex,
    ) -> Result<()> {
        self.write(OWN_LEAF_NODE_INDEX, group_id, own_leaf_index)
    }

    fn write_group_epoch_secrets<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        GroupEpochSecrets: traits::GroupEpochSecrets<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        group_epoch_secrets: &GroupEpochSecrets,
    ) -> Result<()> {
        self.write(GROUP_EPOCH_SECRETS, group_id, group_epoch_secrets)
    }

    fn write_signature_key_pair<
        SignaturePublicKey: traits::SignaturePublicKey<CURRENT_VERSION>,
        SignatureKeyPair: traits::SignatureKeyPair<CURRENT_VERSION>,
    >(
        &self,
        public_key: &SignaturePublicKey,
        signature_key_pair: &SignatureKeyPair,
    ) -> Result<()> {
        self.write(SIGNATURE_KEY_PAIR, public_key, signature_key_pair)
    }

    fn write_encryption_key_pair<
        EncryptionKey: traits::EncryptionKey<CURRENT_VERSION>,
        HpkeKeyPair: traits::HpkeKeyPair<CURRENT_VERSION>,
    >(
        &self,
        public_key: &EncryptionKey,
        key_pair: &HpkeKeyPair,
    ) -> Result<()> {
        self.write(ENCRYPTION_KEY_PAIR, public_key, key_pair)
    }

    fn write_encryption_epoch_key_pairs<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        EpochKey: traits::EpochKey<CURRENT_VERSION>,
        HpkeKeyPair: traits::HpkeKeyPair<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        epoch: &EpochKey,
        leaf_index: u32,
        key_pairs: &[HpkeKeyPair],
    ) -> Result<()> {
        self.write(EPOCH_KEY_PAIRS, &(group_id, epoch, leaf_index), &key_pairs)
    }

    fn write_key_package<
        HashReference: traits::HashReference<CURRENT_VERSION>,
        KeyPackage: traits::KeyPackage<CURRENT_VERSION>,
    >(
        &self,
        hash_ref: &HashReference,
        key_package: &KeyPackage,
    ) -> Result<()> {
        self.write(KEY_PACKAGE, hash_ref, key_package)
    }

    fn write_psk<
        PskId: traits::PskId<CURRENT_VERSION>,
        PskBundle: traits::PskBundle<CURRENT_VERSION>,
    >(
        &self,
        psk_id: &PskId,
        psk: &PskBundle,
    ) -> Result<()> {
        self.write(PSK, psk_id, psk)
    }

    /* ── readers ───────────────────────────────────────────────────────── */

    fn mls_group_join_config<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        MlsGroupJoinConfig: traits::MlsGroupJoinConfig<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Option<MlsGroupJoinConfig>> {
        self.read(JOIN_CONFIG, group_id)
    }

    fn own_leaf_nodes<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        LeafNode: traits::LeafNode<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Vec<LeafNode>> {
        self.read_list(OWN_LEAF_NODES, group_id)
    }

    fn queued_proposal_refs<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        ProposalRef: traits::ProposalRef<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Vec<ProposalRef>> {
        self.read_list(PROPOSAL_QUEUE_REFS, group_id)
    }

    fn queued_proposals<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        ProposalRef: traits::ProposalRef<CURRENT_VERSION>,
        QueuedProposal: traits::QueuedProposal<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Vec<(ProposalRef, QueuedProposal)>> {
        let refs: Vec<ProposalRef> = self.read_list(PROPOSAL_QUEUE_REFS, group_id)?;
        let mut out = Vec::with_capacity(refs.len());
        for proposal_ref in refs {
            // A ref with no proposal behind it would mean the two writes in
            // queue_proposal came apart; skip rather than fail the load, and
            // let the group repair itself by committing past it.
            if let Some(proposal) = self.read(QUEUED_PROPOSAL, &(group_id, &proposal_ref))? {
                out.push((proposal_ref, proposal));
            }
        }
        Ok(out)
    }

    fn tree<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        TreeSync: traits::TreeSync<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Option<TreeSync>> {
        self.read(TREE, group_id)
    }

    fn group_context<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        GroupContext: traits::GroupContext<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Option<GroupContext>> {
        self.read(GROUP_CONTEXT, group_id)
    }

    fn interim_transcript_hash<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        InterimTranscriptHash: traits::InterimTranscriptHash<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Option<InterimTranscriptHash>> {
        self.read(INTERIM_TRANSCRIPT_HASH, group_id)
    }

    fn confirmation_tag<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        ConfirmationTag: traits::ConfirmationTag<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Option<ConfirmationTag>> {
        self.read(CONFIRMATION_TAG, group_id)
    }

    fn group_state<
        GroupState: traits::GroupState<CURRENT_VERSION>,
        GroupId: traits::GroupId<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Option<GroupState>> {
        self.read(GROUP_STATE, group_id)
    }

    fn message_secrets<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        MessageSecrets: traits::MessageSecrets<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Option<MessageSecrets>> {
        self.read(MESSAGE_SECRETS, group_id)
    }

    fn resumption_psk_store<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        ResumptionPskStore: traits::ResumptionPskStore<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Option<ResumptionPskStore>> {
        self.read(RESUMPTION_PSK_STORE, group_id)
    }

    fn own_leaf_index<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        LeafNodeIndex: traits::LeafNodeIndex<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Option<LeafNodeIndex>> {
        self.read(OWN_LEAF_NODE_INDEX, group_id)
    }

    fn group_epoch_secrets<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        GroupEpochSecrets: traits::GroupEpochSecrets<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<Option<GroupEpochSecrets>> {
        self.read(GROUP_EPOCH_SECRETS, group_id)
    }

    fn signature_key_pair<
        SignaturePublicKey: traits::SignaturePublicKey<CURRENT_VERSION>,
        SignatureKeyPair: traits::SignatureKeyPair<CURRENT_VERSION>,
    >(
        &self,
        public_key: &SignaturePublicKey,
    ) -> Result<Option<SignatureKeyPair>> {
        self.read(SIGNATURE_KEY_PAIR, public_key)
    }

    fn encryption_key_pair<
        HpkeKeyPair: traits::HpkeKeyPair<CURRENT_VERSION>,
        EncryptionKey: traits::EncryptionKey<CURRENT_VERSION>,
    >(
        &self,
        public_key: &EncryptionKey,
    ) -> Result<Option<HpkeKeyPair>> {
        self.read(ENCRYPTION_KEY_PAIR, public_key)
    }

    fn encryption_epoch_key_pairs<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        EpochKey: traits::EpochKey<CURRENT_VERSION>,
        HpkeKeyPair: traits::HpkeKeyPair<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        epoch: &EpochKey,
        leaf_index: u32,
    ) -> Result<Vec<HpkeKeyPair>> {
        Ok(self
            .read::<_, Vec<HpkeKeyPair>>(EPOCH_KEY_PAIRS, &(group_id, epoch, leaf_index))?
            .unwrap_or_default())
    }

    fn key_package<
        KeyPackageRef: traits::HashReference<CURRENT_VERSION>,
        KeyPackage: traits::KeyPackage<CURRENT_VERSION>,
    >(
        &self,
        hash_ref: &KeyPackageRef,
    ) -> Result<Option<KeyPackage>> {
        self.read(KEY_PACKAGE, hash_ref)
    }

    fn psk<
        PskBundle: traits::PskBundle<CURRENT_VERSION>,
        PskId: traits::PskId<CURRENT_VERSION>,
    >(
        &self,
        psk_id: &PskId,
    ) -> Result<Option<PskBundle>> {
        self.read(PSK, psk_id)
    }

    /* ── deleters ──────────────────────────────────────────────────────── */

    fn remove_proposal<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        ProposalRef: traits::ProposalRef<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        proposal_ref: &ProposalRef,
    ) -> Result<()> {
        self.delete(QUEUED_PROPOSAL, &(group_id, proposal_ref))?;
        self.remove_from_list(PROPOSAL_QUEUE_REFS, group_id, proposal_ref)
    }

    fn delete_own_leaf_nodes<GroupId: traits::GroupId<CURRENT_VERSION>>(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        self.delete(OWN_LEAF_NODES, group_id)
    }

    fn delete_group_config<GroupId: traits::GroupId<CURRENT_VERSION>>(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        self.delete(JOIN_CONFIG, group_id)
    }

    fn delete_tree<GroupId: traits::GroupId<CURRENT_VERSION>>(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        self.delete(TREE, group_id)
    }

    fn delete_confirmation_tag<GroupId: traits::GroupId<CURRENT_VERSION>>(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        self.delete(CONFIRMATION_TAG, group_id)
    }

    fn delete_group_state<GroupId: traits::GroupId<CURRENT_VERSION>>(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        self.delete(GROUP_STATE, group_id)
    }

    fn delete_context<GroupId: traits::GroupId<CURRENT_VERSION>>(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        self.delete(GROUP_CONTEXT, group_id)
    }

    fn delete_interim_transcript_hash<GroupId: traits::GroupId<CURRENT_VERSION>>(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        self.delete(INTERIM_TRANSCRIPT_HASH, group_id)
    }

    fn delete_message_secrets<GroupId: traits::GroupId<CURRENT_VERSION>>(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        self.delete(MESSAGE_SECRETS, group_id)
    }

    fn delete_all_resumption_psk_secrets<GroupId: traits::GroupId<CURRENT_VERSION>>(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        self.delete(RESUMPTION_PSK_STORE, group_id)
    }

    fn delete_own_leaf_index<GroupId: traits::GroupId<CURRENT_VERSION>>(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        self.delete(OWN_LEAF_NODE_INDEX, group_id)
    }

    fn delete_group_epoch_secrets<GroupId: traits::GroupId<CURRENT_VERSION>>(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        self.delete(GROUP_EPOCH_SECRETS, group_id)
    }

    fn clear_proposal_queue<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        ProposalRef: traits::ProposalRef<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
    ) -> Result<()> {
        // The proposals themselves go with the refs — a queued proposal that
        // outlived its ref would be unreachable state holding key material.
        let refs: Vec<ProposalRef> = self.read_list(PROPOSAL_QUEUE_REFS, group_id)?;
        for proposal_ref in &refs {
            self.delete(QUEUED_PROPOSAL, &(group_id, proposal_ref))?;
        }
        self.delete(PROPOSAL_QUEUE_REFS, group_id)
    }

    fn delete_signature_key_pair<
        SignaturePublicKey: traits::SignaturePublicKey<CURRENT_VERSION>,
    >(
        &self,
        public_key: &SignaturePublicKey,
    ) -> Result<()> {
        self.delete(SIGNATURE_KEY_PAIR, public_key)
    }

    fn delete_encryption_key_pair<EncryptionKey: traits::EncryptionKey<CURRENT_VERSION>>(
        &self,
        public_key: &EncryptionKey,
    ) -> Result<()> {
        self.delete(ENCRYPTION_KEY_PAIR, public_key)
    }

    fn delete_encryption_epoch_key_pairs<
        GroupId: traits::GroupId<CURRENT_VERSION>,
        EpochKey: traits::EpochKey<CURRENT_VERSION>,
    >(
        &self,
        group_id: &GroupId,
        epoch: &EpochKey,
        leaf_index: u32,
    ) -> Result<()> {
        self.delete(EPOCH_KEY_PAIRS, &(group_id, epoch, leaf_index))
    }

    fn delete_key_package<KeyPackageRef: traits::HashReference<CURRENT_VERSION>>(
        &self,
        hash_ref: &KeyPackageRef,
    ) -> Result<()> {
        self.delete(KEY_PACKAGE, hash_ref)
    }

    fn delete_psk<PskKey: traits::PskId<CURRENT_VERSION>>(&self, psk_id: &PskKey) -> Result<()> {
        self.delete(PSK, psk_id)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn export_round_trips_through_import() {
        let storage = KvStorage::new();
        storage.put_handle("signature_public_key", b"pub").unwrap();
        storage.write(TREE, &b"group".to_vec(), &vec![1u8, 2, 3]).unwrap();

        let restored = KvStorage::import(&storage.export().unwrap()).unwrap();

        assert_eq!(restored.handle("signature_public_key").unwrap().as_deref(), Some(&b"pub"[..]));
        let tree: Option<Vec<u8>> = restored.read(TREE, &b"group".to_vec()).unwrap();
        assert_eq!(tree, Some(vec![1, 2, 3]));
        assert_eq!(restored.export().unwrap(), storage.export().unwrap());
    }

    #[test]
    fn a_truncated_export_is_an_error_not_a_partial_map() {
        let storage = KvStorage::new();
        storage.put_handle("h", b"value").unwrap();
        let blob = storage.export().unwrap();
        assert!(KvStorage::import(&blob[..blob.len() - 1]).is_err());
        assert!(KvStorage::import(&[]).is_err());
    }

    #[test]
    fn lists_append_and_remove_by_value() {
        let storage = KvStorage::new();
        let key = b"group".to_vec();
        storage.append(OWN_LEAF_NODES, &key, &vec![1u8]).unwrap();
        storage.append(OWN_LEAF_NODES, &key, &vec![2u8]).unwrap();
        let all: Vec<Vec<u8>> = storage.read_list(OWN_LEAF_NODES, &key).unwrap();
        assert_eq!(all, vec![vec![1u8], vec![2u8]]);

        storage.remove_from_list(OWN_LEAF_NODES, &key, &vec![1u8]).unwrap();
        let left: Vec<Vec<u8>> = storage.read_list(OWN_LEAF_NODES, &key).unwrap();
        assert_eq!(left, vec![vec![2u8]]);
    }

    #[test]
    fn wrapper_handles_cannot_collide_with_trait_keys() {
        // Trait keys are JSON arrays; wrapper handles are a bare prefixed
        // string. If that ever stops being true this test fails first.
        let trait_key = KvStorage::key_of(TREE, &b"group".to_vec()).unwrap();
        assert_eq!(trait_key.first(), Some(&b'['));
        assert_eq!(KvStorage::wrapper_key("x").first(), Some(&b'h'));
    }
}
