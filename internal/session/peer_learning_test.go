package session

import (
	"net"
	"testing"
	"time"
)

func TestPeerLearningInitialLockAfterMinPackets(t *testing.T) {
	tracker := newPeerLearningTracker(3, 4*time.Second, time.Second, nil, "s", "a->b", "audio")
	src := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1000}
	now := time.Now()

	if tracker.observe(src, true, now) {
		t.Fatalf("must not accept before lock")
	}
	if tracker.observe(src, true, now.Add(10*time.Millisecond)) {
		t.Fatalf("must not accept before lock")
	}
	if !tracker.observe(src, true, now.Add(20*time.Millisecond)) {
		t.Fatalf("expected lock after min packets")
	}
	if learned := tracker.learned(); learned == nil || learned.String() != src.String() {
		t.Fatalf("unexpected learned peer: %v", learned)
	}
}

func TestPeerLearningDoesNotLockBeforeMinPackets(t *testing.T) {
	tracker := newPeerLearningTracker(4, 4*time.Second, time.Second, nil, "s", "a->b", "audio")
	src := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1000}
	now := time.Now()
	for i := 0; i < 3; i++ {
		if tracker.observe(src, true, now.Add(time.Duration(i)*10*time.Millisecond)) {
			t.Fatalf("unexpected accept before lock")
		}
	}
	if tracker.learned() != nil {
		t.Fatalf("unexpected learned peer")
	}
}

func TestPeerLearningMultipleCandidates(t *testing.T) {
	tracker := newPeerLearningTracker(3, 4*time.Second, time.Second, nil, "s", "a->b", "audio")
	src1 := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1000}
	src2 := &net.UDPAddr{IP: net.ParseIP("10.0.0.2"), Port: 1001}
	now := time.Now()

	_ = tracker.observe(src1, true, now)
	_ = tracker.observe(src2, true, now.Add(10*time.Millisecond))
	_ = tracker.observe(src1, true, now.Add(20*time.Millisecond))
	if tracker.observe(src2, true, now.Add(30*time.Millisecond)) {
		t.Fatalf("must not lock before src2 reaches min packets")
	}
	if !tracker.observe(src2, true, now.Add(40*time.Millisecond)) {
		t.Fatalf("expected lock on src2")
	}
	if got := tracker.learned(); got == nil || got.String() != src2.String() {
		t.Fatalf("unexpected learned peer: %v", got)
	}
}

func TestPeerLearningCandidateTTL(t *testing.T) {
	tracker := newPeerLearningTracker(3, 100*time.Millisecond, time.Second, nil, "s", "a->b", "audio")
	src := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1000}
	now := time.Now()

	_ = tracker.observe(src, true, now)
	_ = tracker.observe(src, true, now.Add(20*time.Millisecond))
	if tracker.observe(src, true, now.Add(200*time.Millisecond)) {
		t.Fatalf("must not lock with expired candidate history")
	}
	if tracker.observe(src, true, now.Add(210*time.Millisecond)) {
		t.Fatalf("must not lock at second packet after reset")
	}
	if !tracker.observe(src, true, now.Add(220*time.Millisecond)) {
		t.Fatalf("expected lock after re-counting from zero")
	}
}

func TestPeerLearningLockedPeerAcceptedOtherRejected(t *testing.T) {
	tracker := newPeerLearningTracker(2, time.Second, time.Second, nil, "s", "a->b", "audio")
	src1 := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1000}
	src2 := &net.UDPAddr{IP: net.ParseIP("10.0.0.2"), Port: 1001}
	now := time.Now()
	_ = tracker.observe(src1, true, now)
	if !tracker.observe(src1, true, now.Add(10*time.Millisecond)) {
		t.Fatalf("expected lock")
	}
	if !tracker.observe(src1, true, now.Add(20*time.Millisecond)) {
		t.Fatalf("expected learned peer acceptance")
	}
	if tracker.observe(src2, true, now.Add(30*time.Millisecond)) {
		t.Fatalf("unexpected acceptance from foreign peer while locked")
	}
}

func TestPeerLearningReenterAndRelockAfterIdle(t *testing.T) {
	tracker := newPeerLearningTracker(2, time.Second, 100*time.Millisecond, nil, "s", "a->b", "audio")
	src1 := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1000}
	src2 := &net.UDPAddr{IP: net.ParseIP("10.0.0.2"), Port: 1001}
	now := time.Now()
	_ = tracker.observe(src1, true, now)
	if !tracker.observe(src1, true, now.Add(10*time.Millisecond)) {
		t.Fatalf("expected initial lock")
	}
	if tracker.observe(src2, true, now.Add(300*time.Millisecond)) {
		t.Fatalf("first packet from new source should only start learning")
	}
	if !tracker.observe(src2, true, now.Add(320*time.Millisecond)) {
		t.Fatalf("expected re-lock on new source")
	}
	if got := tracker.learned(); got == nil || got.String() != src2.String() {
		t.Fatalf("unexpected re-learned peer: %v", got)
	}
}

func TestVideoLearningUsesMediaLikePacketsOnly(t *testing.T) {
	rtpButNotMedia := makeRTPPacket(1, 100, []byte{})
	if isSuitableVideoMediaPacket(rtpButNotMedia) {
		t.Fatalf("empty payload must not be suitable video media")
	}
	media := makeRTPPacket(2, 100, []byte{0x67}) // SPS
	if !isSuitableVideoMediaPacket(media) {
		t.Fatalf("SPS packet must be suitable video media")
	}
}

func TestVideoIdleBasedOnMediaPackets(t *testing.T) {
	tracker := newPeerLearningTracker(2, time.Second, 100*time.Millisecond, nil, "s", "a->b", "video")
	src1 := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1000}
	now := time.Now()
	_ = tracker.observe(src1, true, now)
	if !tracker.observe(src1, true, now.Add(10*time.Millisecond)) {
		t.Fatalf("expected lock")
	}
	if !tracker.observe(src1, false, now.Add(80*time.Millisecond)) {
		t.Fatalf("non-media from learned peer should pass while locked")
	}
	if tracker.observe(src1, false, now.Add(220*time.Millisecond)) {
		t.Fatalf("after idle timeout first non-media should not re-lock/accept")
	}
	if tracker.observe(src1, true, now.Add(230*time.Millisecond)) {
		t.Fatalf("first media packet after idle should not lock yet")
	}
	if !tracker.observe(src1, true, now.Add(240*time.Millisecond)) {
		t.Fatalf("expected re-lock after suitable packet series")
	}
}
