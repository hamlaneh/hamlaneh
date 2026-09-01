import { useCallback, useMemo, useState } from "react";

import type { Message } from "../chat/types";
import { decodeBody } from "./attachments";
import { MlsService } from "./service";
import type { MediaKey, MessageBody, MlsState, OpenBackupOutcome } from "./types";
import { initialMlsState } from "./types";

export interface MlsController {
  state: MlsState;
  /** Called when an e2ee channel is opened; a no-op for plaintext ones. */
  openChannel: (channelId: string) => void;
  /** `mls_commit`, and every reconnect. */
  syncChannel: (channelId: string) => void;
  /** `mls_welcome`, and every reconnect. */
  syncWelcomes: () => void;
  memberAdded: (channelId: string) => void;
  /** Takes no user id: the directory's roster decides who leaves the tree. */
  memberRemoved: (channelId: string) => void;
  /**
   * Null when nothing can be sent — the caller must not fall back to text.
   *
   * `attachmentIds` names the files this message shares; their keys and real
   * metadata are sealed inside the ciphertext, which is what makes a file
   * readable exactly when its message is (ADR 013). An EDIT passes
   * {@link MlsController.attachmentIdsOf} for the message being edited: the
   * stored ciphertext is replaced whole, so entries that are not re-carried
   * are gone for every reader.
   */
  encrypt: (
    channelId: string,
    text: string,
    attachmentIds?: readonly string[],
  ) => Promise<{ epoch: number; ciphertext: string } | null>;
  /** The files a readable message carries — what an edit must re-carry. */
  attachmentIdsOf: (messageId: string) => string[];
  /**
   * Keeps the plaintext of a message this device sent, on the screen and in
   * the wrapped keystore, so a reload still shows the author their own words
   * (see the service). Awaitable because it writes.
   */
  rememberSent: (messageId: string, text: string) => Promise<void>;
  /** The exporter-derived key a call in this conversation uses (ADR 009). */
  mediaKey: (channelId: string) => MediaKey | null;
  /** Queues the decryption of whatever in this page is still encrypted. */
  decryptAll: (channelId: string, messages: readonly Message[]) => void;
  /** What a bubble should draw for this message. */
  bodyOf: (message: Message) => MessageBody;
  /** The sixty digits both people compare, or null before any reconcile. */
  safetyNumberFor: (userId: string) => Promise<string | null>;
  /** Exit 1: the humans compared out of band and the numbers matched. */
  verifyPeer: (userId: string) => Promise<void>;
  /** Exit 2: "I checked" — a pin, which downgrades a verified badge. */
  acceptPeer: (userId: string) => Promise<void>;
  /** The own-account prompt's yes: that new device under your id is yours. */
  acceptOwnDevices: () => Promise<void>;
  /** The enrolment ceremony: returns the recovery key to show exactly once. */
  enableBackup: () => Promise<string | null>;
  /** The offer's no, recorded and respected (ADR 010). */
  declineBackup: () => Promise<void>;
  /** Step one of a restore. Nothing local changes until `applyRestore`. */
  openBackup: (recoveryKey: string) => Promise<OpenBackupOutcome>;
  /** Step two: the person confirmed the sealed date. */
  applyRestore: () => Promise<boolean>;
  /** The person backed out of a restore. */
  discardRestore: () => void;
}

/**
 * The MLS service, bound to a React tree.
 *
 * The service owns all the state and all the ordering; this hook is the
 * adapter that turns its `onChange` into a render and its methods into stable
 * callbacks. Nothing about MLS lives in the chat reducer — an encrypted
 * conversation is the ordinary one with a decryption step in front of it.
 */
export function useMls(currentUserId: string): MlsController {
  const [state, setState] = useState<MlsState>(initialMlsState);

  // Built once per user: constructing the service does nothing on its own —
  // no wasm, no IndexedDB — until an encrypted channel is opened. A different
  // user means a different signed-in app and therefore a fresh tree, so there
  // is no state to reset here when the id changes.
  const service = useMemo(
    () => new MlsService({ currentUserId, onChange: setState }),
    [currentUserId],
  );

  const openChannel = useCallback(
    (channelId: string) => {
      void service.openChannel(channelId);
    },
    [service],
  );

  const syncChannel = useCallback(
    (channelId: string) => {
      void service.syncChannel(channelId);
    },
    [service],
  );

  const syncWelcomes = useCallback(() => {
    void service.syncWelcomes();
  }, [service]);

  const memberAdded = useCallback(
    (channelId: string) => {
      void service.memberAdded(channelId);
    },
    [service],
  );

  const memberRemoved = useCallback(
    (channelId: string) => {
      void service.memberRemoved(channelId);
    },
    [service],
  );

  const encrypt = useCallback(
    (channelId: string, text: string, attachmentIds?: readonly string[]) =>
      service.encrypt(channelId, text, attachmentIds),
    [service],
  );

  const attachmentIdsOf = useCallback(
    (messageId: string) => service.attachmentIdsOf(messageId),
    [service],
  );

  const rememberSent = useCallback(
    (messageId: string, text: string) => service.rememberSent(messageId, text),
    [service],
  );

  const mediaKey = useCallback(
    (channelId: string) => service.mediaKey(channelId),
    [service],
  );

  const decryptAll = useCallback(
    (channelId: string, messages: readonly Message[]) => {
      for (const message of messages) {
        if (message.mls !== undefined) {
          void service.decrypt(channelId, message.id, message.mls.ciphertext);
        }
      }
    },
    [service],
  );

  const safetyNumberFor = useCallback(
    (userId: string) => service.safetyNumberFor(userId),
    [service],
  );

  const verifyPeer = useCallback((userId: string) => service.verifyPeer(userId), [service]);

  const acceptPeer = useCallback((userId: string) => service.acceptPeer(userId), [service]);

  const acceptOwnDevices = useCallback(() => service.acceptOwnDevices(), [service]);

  const enableBackup = useCallback(() => service.enableBackup(), [service]);
  const declineBackup = useCallback(() => service.declineBackup(), [service]);
  const openBackup = useCallback(
    (recoveryKey: string) => service.openBackup(recoveryKey),
    [service],
  );
  const applyRestore = useCallback(() => service.applyRestore(), [service]);
  const discardRestore = useCallback(() => {
    service.discardRestore();
  }, [service]);

  const decrypted = state.decrypted;
  const bodyOf = useCallback(
    (message: Message): MessageBody => {
      if (message.mls === undefined) {
        return { kind: "plaintext", text: message.content };
      }
      const body = decrypted[message.id];
      if (body === undefined) {
        return { kind: "pending" };
      }
      if (body === null) {
        return { kind: "undecryptable" };
      }
      // A body that claims the envelope sentinel and does not parse is not
      // shown as text: it decodes to null, and the bubble says it cannot be
      // displayed (ADR 013).
      const decoded = decodeBody(body);
      return decoded === null
        ? { kind: "undecryptable" }
        : { kind: "decrypted", text: decoded.text, attachments: decoded.attachments };
    },
    [decrypted],
  );

  /*
   * Memoized, and that is load-bearing rather than tidy: every callback below
   * is already stable, so a fresh object literal here would change identity on
   * every render and re-fire each effect that depends on the controller. It
   * did — the reconnect effect refetched the Welcome list once per render.
   */
  return useMemo(
    () => ({
      state,
      openChannel,
      syncChannel,
      syncWelcomes,
      memberAdded,
      memberRemoved,
      encrypt,
      attachmentIdsOf,
      rememberSent,
      mediaKey,
      decryptAll,
      bodyOf,
      safetyNumberFor,
      verifyPeer,
      acceptPeer,
      acceptOwnDevices,
      enableBackup,
      declineBackup,
      openBackup,
      applyRestore,
      discardRestore,
    }),
    [
      state,
      openChannel,
      syncChannel,
      syncWelcomes,
      memberAdded,
      memberRemoved,
      encrypt,
      attachmentIdsOf,
      rememberSent,
      mediaKey,
      decryptAll,
      bodyOf,
      safetyNumberFor,
      verifyPeer,
      acceptPeer,
      acceptOwnDevices,
      enableBackup,
      declineBackup,
      openBackup,
      applyRestore,
      discardRestore,
    ],
  );
}
