import { Room, RoomEvent, Track } from "livekit-client";
import type { Participant } from "livekit-client";

import type {
  MediaConnect,
  MediaEvent,
  MediaParticipant,
  MediaTrack,
} from "./media";

/**
 * The one module that touches `livekit-client`. Everything the call UI knows
 * about media goes through `MediaSession` (media.ts), so this file is the
 * whole of the WebRTC surface and the whole of what a test replaces.
 */

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

export const connectLiveKit: MediaConnect = async (url, token) => {
  const room = new Room();
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
    disconnect: async () => {
      // Dropped before the disconnect so the resulting Disconnected event does
      // not tell the UI its own leave was the server ending the call.
      listeners.clear();
      await room.disconnect();
      room.removeAllListeners();
    },
  };
};
