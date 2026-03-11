package session

import (
	"net"
	"testing"
	"time"

	"rtp-stream-cleaner/internal/rtpfix"
)

func TestVideoReorderSimpleAndGapScenarios(t *testing.T) {
	t.Run("in-order", func(t *testing.T) {
		released, counters := runReorderSequence(t, 8, 10*time.Millisecond, []uint16{100, 101, 102, 103}, nil)
		assertSeqs(t, released, []uint16{100, 101, 102, 103})
		if counters.videoReorderBuffered.Load() != 0 {
			t.Fatalf("unexpected buffered packets")
		}
	})

	t.Run("simple reorder", func(t *testing.T) {
		released, _ := runReorderSequence(t, 8, 10*time.Millisecond, []uint16{100, 102, 101, 103}, nil)
		assertSeqs(t, released, []uint16{100, 101, 102, 103})
	})

	t.Run("small gap resolved", func(t *testing.T) {
		released, _ := runReorderSequence(t, 8, 10*time.Millisecond, []uint16{100, 103, 101, 102}, nil)
		assertSeqs(t, released, []uint16{100, 101, 102, 103})
	})

	t.Run("duplicate", func(t *testing.T) {
		released, counters := runReorderSequence(t, 8, 10*time.Millisecond, []uint16{100, 101, 101, 102}, nil)
		assertSeqs(t, released, []uint16{100, 101, 102})
		if counters.videoReorderDuplicates.Load() != 1 {
			t.Fatalf("expected one duplicate")
		}
	})
}

func TestVideoReorderLateOverflowTimeoutAndWrap(t *testing.T) {
	t.Run("late after forced skip", func(t *testing.T) {
		now := time.Now()
		released, counters := runReorderSequence(t, 2, 10*time.Millisecond, []uint16{100, 102, 103, 101}, []time.Time{now, now, now, now})
		assertSeqs(t, released, []uint16{100, 102, 103})
		if counters.videoReorderLateDrops.Load() == 0 {
			t.Fatalf("expected late drop")
		}
	})

	t.Run("window overflow", func(t *testing.T) {
		released, counters := runReorderSequence(t, 2, time.Second, []uint16{100, 103, 104}, nil)
		assertSeqs(t, released, []uint16{100, 103, 104})
		if counters.videoReorderForcedSkips.Load() == 0 {
			t.Fatalf("expected forced skip")
		}
	})

	t.Run("timeout skip", func(t *testing.T) {
		counters := &videoDirectionCounters{}
		r := newVideoReorderBuffer(8, 5*time.Millisecond, counters)
		base := time.Now()
		var out []uint16
		release := func(packet []byte, _ *net.UDPAddr, _ bool) {
			h, _ := rtpfix.ParseRTPHeader(packet)
			out = append(out, h.Seq)
		}
		r.push(makeRTPPacket(100, 1, []byte{0x65}), localAddr(), base, release)
		r.push(makeRTPPacket(102, 1, []byte{0x65}), localAddr(), base, release)
		r.releaseExpired(base.Add(20*time.Millisecond), release)
		assertSeqs(t, out, []uint16{100, 102})
		if counters.videoReorderHeldTooLong.Load() == 0 {
			t.Fatalf("expected held-too-long metric")
		}
	})

	t.Run("wrap-around ordered", func(t *testing.T) {
		released, _ := runReorderSequence(t, 8, 10*time.Millisecond, []uint16{65534, 65535, 0, 1, 2}, nil)
		assertSeqs(t, released, []uint16{65534, 65535, 0, 1, 2})
	})

	t.Run("wrap-around reorder", func(t *testing.T) {
		released, _ := runReorderSequence(t, 8, 10*time.Millisecond, []uint16{65534, 0, 65535, 1}, nil)
		assertSeqs(t, released, []uint16{65534, 65535, 0, 1})
	})
}

func runReorderSequence(t *testing.T, maxPackets int, maxWait time.Duration, seqs []uint16, times []time.Time) ([]uint16, *videoDirectionCounters) {
	t.Helper()
	counters := &videoDirectionCounters{}
	r := newVideoReorderBuffer(maxPackets, maxWait, counters)
	out := make([]uint16, 0, len(seqs))
	release := func(packet []byte, _ *net.UDPAddr, _ bool) {
		h, ok := rtpfix.ParseRTPHeader(packet)
		if !ok {
			t.Fatalf("header parse failed")
		}
		out = append(out, h.Seq)
	}
	for i, seq := range seqs {
		now := time.Now()
		if i < len(times) && !times[i].IsZero() {
			now = times[i]
		}
		r.push(makeRTPPacket(seq, 9000, []byte{0x65}), localAddr(), now, release)
	}
	return out, counters
}

func localAddr() *net.UDPAddr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10000}
}

func assertSeqs(t *testing.T, got, want []uint16) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("seq mismatch at %d: got=%v want=%v", i, got, want)
		}
	}
}
