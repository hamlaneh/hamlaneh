import {
  BaseKeyProvider,
  Room,
  RoomEvent,
  Track,
  createKeyMaterialFromBuffer,
  isE2EESupported,
} from "livekit-client";
import type { Participant } from "livekit-client";

import type { MediaKey } from "../mls/types";
import type {
  MediaConnect,
  MediaEvent,
  MediaParticipant,
  MediaTrack,
} from "./media";
import { CallEncryptionUnsupportedError } from "./media";

/**
 * The one module that touches `livekit-client`. Everything the call UI knows
 * about media goes through `MediaSession` (media.ts), so this file is the
 * whole of the WebRTC surface and the whole of what a test replaces.
 */

/**
 * How many slots the media keyring has, and therefore what an epoch is
 * reduced modulo to name one.
 *
 * **This is a wire constant, not a local setting.** Every participant computes
 * its slot from its own copy of this number, and nothing is ever signalled, so
 * two clients that disagree about it seal and open different slots. That
 * failure is silent by design — `failureTolerance: -1` never throws, and the
 * symptom is a tile that stays frozen, which ADR 009 case (a) teaches people
 * to read as "they have not merged the commit yet". So it is pinned here
 * rather than inherited from `livekit-client` (whose own default is 16 in the
 * pinned 2.22.1, and is theirs to change), and **changing it breaks every
 * open tab on the old value**: it is a release-boundary change, not an edit.
 *
 * ADR 009, decision 3 schedules a rise to 256 for the day sixteen epochs can
 * pass inside one frame's flight time. That day still needs the paragraph
 * above answered before the number moves.
 */
export const MEDIA_KEYRING_SIZE = 16;

/** The keyring slot an epoch names — the whole of the rotation protocol. */
export function mediaKeySlot(epoch: number): number {
  return epoch % MEDIA_KEYRING_SIZE;
}

/**
 * The keyring, driven by MLS epochs (ADR 009, decision 4).
 *
 * Everything cryptographic here is the library's: its worker, its AES-GCM
 * frame encryption, its HKDF key-material derivation, its keyring and frame
 * format. This class routes keys and makes none.
 *
 * It subclasses `BaseKeyProvider` rather than using the stock
 * `ExternalE2EEKeyProvider` for one reason, verified against the pinned
 * 2.22.1 source: that class's `setKey` calls `onSetEncryptionKey(key)` with
 * **no key index**, so it holds a single static passphrase and rotating under
 * it would overwrite the live key while frames sealed with it are still in
 * flight. The protected call one layer down takes the index, which is the
 * whole of what rotation needs — so this is the same three options
 * `ExternalE2EEKeyProvider` hard-codes, plus one method.
 *
 * LiveKit's own key ratchet stays unused on purpose: **MLS is the only
 * ratchet.** Epoch-derived slots agree only because both ends compute them
 * from group state, and a local ratchet advancing keys out of band would
 * desynchronize exactly that. `ratchetWindowSize: 0` and shared-key mode are
 * the library's own way of saying so.
 */
export class MlsKeyProvider extends BaseKeyProvider {
  constructor() {
    super({
      sharedKey: true,
      // A shared key that fails to decrypt for one participant must not be
      // marked invalid: a peer who has not merged the commit yet sends under
      // a slot we cannot open, and the keyring has to survive that so
      // decoding resumes the instant their catch-up lands.
      ratchetWindowSize: 0,
      failureTolerance: -1,
      // Matches the MLS ciphersuite's AES-128-GCM strength.
      keySize: 128,
      // Stated, not inherited. Both ends of a call divide by this number and
      // nobody tells anybody which one they used — see MEDIA_KEYRING_SIZE.
      keyringSize: MEDIA_KEYRING_SIZE,
    });
  }

  /**
   * Fills the slot this epoch names, and makes it the one we send under.
   *
   * `mediaKeySlot` is the whole of the rotation protocol: every member
   * computes the same slot from the same epoch, so nothing is ever signalled
   * and no member is a keying authority. The old slot keeps its key until the
   * ring index comes round again, which is what lets frames already in flight
   * decode.
   */
  async useEpoch(key: MediaKey): Promise<void> {
    // Copied into an exact-size buffer: the bytes arrive as a view from wasm,
    // and `crypto.subtle.importKey` takes the whole buffer, not the view.
    const bytes = new Uint8Array(key.secret);
    const material = await createKeyMaterialFromBuffer(bytes.buffer);
    this.onSetEncryptionKey(material, undefined, mediaKeySlot(key.epoch));
  }
}

function trackFor(participant: Participant, source: Track.Source): MediaTrack | null {
  const track = participant.getTrackPublication(source)?.track;
  if (track === undefined) {
    return null;
  }
  return {
    attach: (element) => {
      track.attach(element);
    },
    detach: (element) => {
      track.detach(element);
    },
  };
}

function describe(participant: Participant): MediaParticipant {
  return {
    identity: participant.identity,
    // A participant always has an identity; the display name is what the
    // token carried, and falling back to the identity beats an empty tile.
    name: participant.name ?? participant.identity,
    isLocal: participant.isLocal,
    speaking: participant.isSpeaking,
    micEnabled: participant.isMicrophoneEnabled,
    cameraEnabled: participant.isCameraEnabled,
    screenSharing: participant.isScreenShareEnabled,
    camera: trackFor(participant, Track.Source.Camera),
    screen: trackFor(participant, Track.Source.ScreenShare),
    microphone: trackFor(participant, Track.Source.Microphone),
  };
}

export const connectLiveKit: MediaConnect = async (url, token, key) => {
  // Gate 2 of ADR 009, decision 2, and the reason it lives here: refusing an
  // encrypted call this browser cannot encrypt is the honest answer, and a
  // plaintext fallback would be the silent downgrade the whole phase exists
  // to prevent. Nothing below runs — no room, no ticket spent, no worker.
  if (key !== undefined && !isE2EESupported()) {
    throw new CallEncryptionUnsupportedError();
  }

  const keyProvider = key === undefined ? null : new MlsKeyProvider();
  // Held so `disconnect` can end it. `E2EEManager` never terminates the worker
  // it was handed — verified in the pinned 2.22.1 — so without this each
  // encrypted join would leave one behind for the life of the page, holding
  // that call's media keys in its memory long after the call ended. ADR 009
  // says the provider is created at join and discarded at leave, and the
  // worker is the half of it that does not go on its own.
  const worker =
    keyProvider === null
      ? null
      : // Bundled same-origin by Vite, so no CSP change and no CDN.
        new Worker(new URL("livekit-client/e2ee-worker", import.meta.url), { type: "module" });
  const room =
    keyProvider === null || worker === null
      ? new Room()
      : new Room({ e2ee: { keyProvider, worker } });

  // Keyed before connecting, so this room has never existed unencrypted.
  if (keyProvider !== null && key !== undefined) {
    await keyProvider.useEpoch(key);
    await room.setE2EEEnabled(true);
  }

  const listeners = new Set<(event: MediaEvent) => void>();

  const emit = (event: MediaEvent) => {
    for (const listener of listeners) {
      listener(event);
    }
  };
  const onChange = () => {
    emit("changed");
  };
  const onClosed = () => {
    emit("closed");
  };

  // Every event that moves something a tile draws. Listed rather than looped
  // because the emitter types each event's callback separately.
  room.on(RoomEvent.ParticipantConnected, onChange);
  room.on(RoomEvent.ParticipantDisconnected, onChange);
  room.on(RoomEvent.TrackSubscribed, onChange);
  room.on(RoomEvent.TrackUnsubscribed, onChange);
  room.on(RoomEvent.TrackMuted, onChange);
  room.on(RoomEvent.TrackUnmuted, onChange);
  room.on(RoomEvent.LocalTrackPublished, onChange);
  room.on(RoomEvent.LocalTrackUnpublished, onChange);
  room.on(RoomEvent.ActiveSpeakersChanged, onChange);
  room.on(RoomEvent.ConnectionStateChanged, onChange);
  room.on(RoomEvent.Disconnected, onClosed);

  await room.connect(url, token);

  return {
    participants: () =>
      [room.localParticipant, ...room.remoteParticipants.values()].map(describe),
    subscribe: (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    setMicrophoneEnabled: async (enabled) => {
      await room.localParticipant.setMicrophoneEnabled(enabled);
    },
    setCameraEnabled: async (enabled) => {
      await room.localParticipant.setCameraEnabled(enabled);
    },
    setScreenShareEnabled: async (enabled) => {
      await room.localParticipant.setScreenShareEnabled(enabled);
    },
    setKey: async (next) => {
      // A plaintext room never gains a key: the room kind is fixed at birth
      // (ADR 006, decision 3), so there is nothing here that could turn one
      // into the other in either direction.
      await keyProvider?.useEpoch(next);
    },
    disconnect: async () => {
      // Dropped before the disconnect so the resulting Disconnected event does
      // not tell the UI its own leave was the server ending the call.
      listeners.clear();
      await room.disconnect();
      room.removeAllListeners();
      // After the disconnect, so nothing is torn down mid-teardown. This is
      // where the epoch keys this call collected stop existing.
      worker?.terminate();
    },
  };
};
