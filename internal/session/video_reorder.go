package session

import (
	"net"
	"time"

	"rtp-stream-cleaner/internal/rtpfix"
)

type reorderEntry struct {
	packet   []byte
	src      *net.UDPAddr
	inserted time.Time
}

type videoReorderBuffer struct {
	enabled     bool
	initialized bool
	expectedSeq uint16
	maxPackets  uint16
	maxWait     time.Duration
	buffer      map[uint16]reorderEntry
	counters    *videoDirectionCounters
}

func newVideoReorderBuffer(maxPackets int, maxWait time.Duration, counters *videoDirectionCounters) *videoReorderBuffer {
	if maxPackets < 1 {
		maxPackets = 1
	}
	return &videoReorderBuffer{
		enabled:    true,
		maxPackets: uint16(maxPackets),
		maxWait:    maxWait,
		buffer:     make(map[uint16]reorderEntry),
		counters:   counters,
	}
}

func (r *videoReorderBuffer) readPollInterval() time.Duration {
	if r == nil || r.maxWait <= 0 {
		return 50 * time.Millisecond
	}
	if r.maxWait < 2*time.Millisecond {
		return r.maxWait
	}
	return r.maxWait / 2
}

func (r *videoReorderBuffer) push(packet []byte, src *net.UDPAddr, now time.Time, release func([]byte, *net.UDPAddr, bool)) {
	if r == nil {
		release(packet, src, false)
		return
	}
	header, ok := rtpfix.ParseRTPHeader(packet)
	if !ok {
		release(packet, src, false)
		return
	}
	if !r.initialized {
		r.initialized = true
		r.expectedSeq = header.Seq + 1
		release(packet, src, false)
		return
	}

	if header.Seq == r.expectedSeq {
		r.releaseExpected(packet, src, release)
		r.releaseBufferedChain(release)
		r.releaseExpired(now, release)
		return
	}

	if _, exists := r.buffer[header.Seq]; exists {
		r.counters.videoReorderDuplicates.Add(1)
		return
	}

	forward := seqDistanceForward(r.expectedSeq, header.Seq)
	if forward == 0 || forward >= 0x8000 {
		backward := seqDistanceForward(header.Seq, r.expectedSeq)
		if backward > 0 && backward <= r.maxPackets {
			r.counters.videoReorderDuplicates.Add(1)
		} else {
			r.counters.videoReorderLateDrops.Add(1)
		}
		return
	}

	if forward <= r.maxPackets {
		clone := append([]byte(nil), packet...)
		r.buffer[header.Seq] = reorderEntry{packet: clone, src: cloneUDPAddr(src), inserted: now}
		r.counters.videoReorderBuffered.Add(1)
		r.observeDepth()
		r.enforceWindowAndRelease(release)
		r.releaseExpired(now, release)
		return
	}

	// Too far ahead: force-skip missing sequence(s) without renumbering.
	r.counters.videoReorderOutOfWindow.Add(1)
	r.forceSkipUntilInWindow(header.Seq)
	if header.Seq == r.expectedSeq {
		r.releaseExpected(packet, src, release)
		r.releaseBufferedChain(release)
		return
	}
	clone := append([]byte(nil), packet...)
	r.buffer[header.Seq] = reorderEntry{packet: clone, src: cloneUDPAddr(src), inserted: now}
	r.counters.videoReorderBuffered.Add(1)
	r.observeDepth()
	r.enforceWindowAndRelease(release)
}

func (r *videoReorderBuffer) releaseExpired(now time.Time, release func([]byte, *net.UDPAddr, bool)) {
	if r == nil || r.maxWait <= 0 || len(r.buffer) == 0 {
		return
	}
	for len(r.buffer) > 0 {
		oldest := now
		for _, entry := range r.buffer {
			if entry.inserted.Before(oldest) {
				oldest = entry.inserted
			}
		}
		if now.Sub(oldest) <= r.maxWait {
			return
		}
		r.counters.videoReorderHeldTooLong.Add(1)
		r.forceSkipOne()
		r.releaseBufferedChain(release)
	}
}

func (r *videoReorderBuffer) enforceWindowAndRelease(release func([]byte, *net.UDPAddr, bool)) {
	for len(r.buffer) >= int(r.maxPackets) {
		r.forceSkipOne()
		r.releaseBufferedChain(release)
		if len(r.buffer) == 0 {
			return
		}
	}
}

func (r *videoReorderBuffer) forceSkipUntilInWindow(seq uint16) {
	for {
		forward := seqDistanceForward(r.expectedSeq, seq)
		if forward <= r.maxPackets {
			return
		}
		r.forceSkipOne()
	}
}

func (r *videoReorderBuffer) forceSkipOne() {
	r.expectedSeq++
	r.counters.videoReorderForcedSkips.Add(1)
}

func (r *videoReorderBuffer) releaseExpected(packet []byte, src *net.UDPAddr, release func([]byte, *net.UDPAddr, bool)) {
	release(packet, src, false)
	r.expectedSeq++
}

func (r *videoReorderBuffer) releaseBufferedChain(release func([]byte, *net.UDPAddr, bool)) {
	for {
		entry, ok := r.buffer[r.expectedSeq]
		if !ok {
			return
		}
		delete(r.buffer, r.expectedSeq)
		release(entry.packet, entry.src, true)
		r.expectedSeq++
	}
}

func (r *videoReorderBuffer) observeDepth() {
	if r == nil || r.counters == nil {
		return
	}
	depth := uint64(len(r.buffer))
	for {
		cur := r.counters.videoReorderMaxDepth.Load()
		if depth <= cur {
			return
		}
		if r.counters.videoReorderMaxDepth.CompareAndSwap(cur, depth) {
			return
		}
	}
}

// seqDistanceForward returns modulo-65536 forward distance from from->to.
func seqDistanceForward(from, to uint16) uint16 {
	return to - from
}
