package wsgateway

import "time"

// The §5 replay buffer bounds: the more recent of the last 256 events or the
// last 5 minutes. It is memory only. It is not durable, not a queue, and not
// a source of truth — Postgres is.
const (
	replayMaxEvents = 256
	replayMaxAge    = 5 * time.Minute
)

// replayEntry is one ordered event, rendered once and shared by every
// recipient. The bytes are never modified after they go in.
type replayEntry struct {
	seq   uint64
	at    time.Time
	frame []byte
}

// replayBuffer is one channel's sequence counter and its bounded window of
// recent ordered events, ascending by seq.
//
// The counter outlives the entries on purpose. Pruning drops what nobody can
// resume from any more, but a counter that restarted at 1 would hand a
// reconnecting client sequence numbers below ones it already holds, and
// every later resume on that channel would be judged against the wrong
// scale. Keeping it costs one integer per channel that has ever carried an
// ordered event.
//
// ponytail: unbounded in channel count (a few dozen bytes each, never freed
// while the process lives). If an instance ever has enough channels for that
// to matter, expire the whole entry on an idle timer far longer than
// replayMaxAge and accept the resync it causes.
type replayBuffer struct {
	lastSeq uint64
	entries []replayEntry
}

// nextSeq assigns the next sequence number. The caller renders the frame
// with it and hands both back to store; the two steps are separate because
// the sequence number is part of the bytes.
func (b *replayBuffer) nextSeq() uint64 {
	b.lastSeq++
	return b.lastSeq
}

// store buffers a rendered ordered event.
func (b *replayBuffer) store(seq uint64, frame []byte, at time.Time) {
	b.entries = append(b.entries, replayEntry{seq: seq, at: at, frame: frame})
	if len(b.entries) > replayMaxEvents {
		// Copy rather than reslice: reslicing keeps the dropped entries'
		// frames alive behind the array for as long as the buffer lives.
		b.entries = append([]replayEntry(nil), b.entries[len(b.entries)-replayMaxEvents:]...)
	}
}

// canResume reports whether the buffer can carry a client from seq to the
// present. A client further behind than the buffer reaches, or one claiming
// a sequence number this buffer never issued, cannot be resumed and is told
// to resync.
func (b *replayBuffer) canResume(seq uint64) bool {
	switch {
	case seq > b.lastSeq:
		// A sequence number we never issued: a different server lifetime, or
		// a client making something up. Either way there is nothing to
		// replay from.
		return false
	case seq == b.lastSeq:
		// Caught up. Nothing to replay, and nothing missing.
		return true
	case len(b.entries) == 0:
		return false
	default:
		// The next event the client is missing must still be in the window.
		return b.entries[0].seq <= seq+1
	}
}

// after returns the buffered frames a client at seq has not seen, in order.
func (b *replayBuffer) after(seq uint64) [][]byte {
	var out [][]byte
	for _, e := range b.entries {
		if e.seq > seq {
			out = append(out, e.frame)
		}
	}
	return out
}

// prune drops entries older than cutoff.
func (b *replayBuffer) prune(cutoff time.Time) {
	drop := 0
	for drop < len(b.entries) && b.entries[drop].at.Before(cutoff) {
		drop++
	}
	if drop == 0 {
		return
	}
	b.entries = append([]replayEntry(nil), b.entries[drop:]...)
}
