import { useCallback, useMemo, useState } from "react";

import type { Message } from "../chat/types";
import { MlsService } from "./service";
import type { MessageBody, MlsState } from "./types";
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
  memberRemoved: (channelId: string, userId: string) => void;
  /** Null when nothing can be sent — the caller must not fall back to text. */
  encrypt: (
    channelId: string,
    text: string,
  ) => Promise<{ epoch: number; ciphertext: string } | null>;
  /** Keeps the plaintext of a message this device sent (see the service). */
  rememberSent: (messageId: string, text: string) => void;
  /** Queues the decryption of whatever in this page is still encrypted. */
  decryptAll: (channelId: string, messages: readonly Message[]) => void;
  /** What a bubble should draw for this message. */
  bodyOf: (message: Message) => MessageBody;
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
    (channelId: string, userId: string) => {
      void service.memberRemoved(channelId, userId);
    },
    [service],
  );

  const encrypt = useCallback(
    (channelId: string, text: string) => service.encrypt(channelId, text),
    [service],
  );

  const rememberSent = useCallback(
    (messageId: string, text: string) => {
      service.rememberSent(messageId, text);
    },
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

  const decrypted = state.decrypted;
  const bodyOf = useCallback(
    (message: Message): MessageBody => {
      if (message.mls === undefined) {
        return { kind: "plaintext", text: message.content };
      }
      const text = decrypted[message.id];
      if (text === undefined) {
        return { kind: "pending" };
      }
      return text === null ? { kind: "undecryptable" } : { kind: "decrypted", text };
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
      rememberSent,
      decryptAll,
      bodyOf,
    }),
    [
      state,
      openChannel,
      syncChannel,
      syncWelcomes,
      memberAdded,
      memberRemoved,
      encrypt,
      rememberSent,
      decryptAll,
      bodyOf,
    ],
  );
}
