package session

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSnapshotAudioDirectionalStatsSplitAndDropReasons(t *testing.T) {
	var counters audioCounters
	counters.aToB.pktsIn.Add(10)
	counters.aToB.pktsOut.Add(8)
	counters.aToB.bytesIn.Add(1000)
	counters.aToB.bytesOut.Add(800)
	counters.aToB.ignoredDisabled.Add(2)
	counters.aToB.dropDestNotSet.Add(1)
	counters.aToB.dropPeerUpdateRejected.Add(2)
	counters.aToB.dropWriteError.Add(3)

	counters.bToA.pktsIn.Add(7)
	counters.bToA.pktsOut.Add(6)
	counters.bToA.bytesIn.Add(700)
	counters.bToA.bytesOut.Add(600)
	counters.bToA.dropDestIPMismatch.Add(4)
	counters.bToA.dropPeerNotLearned.Add(5)

	snapshots := snapshotAudioDirectionalStats(&counters)
	if len(snapshots) != 2 {
		t.Fatalf("unexpected snapshots len: %d", len(snapshots))
	}

	aToB := snapshots[0]
	if aToB.Direction != proxyDirectionAToB {
		t.Fatalf("unexpected direction: %s", aToB.Direction)
	}
	if aToB.PktsIn != 10 || aToB.PktsOut != 8 {
		t.Fatalf("unexpected a->b packet split: in=%d out=%d", aToB.PktsIn, aToB.PktsOut)
	}
	if aToB.DropsTotal != aToB.DropDestNotSet+aToB.DropDestIPMismatch+aToB.DropPeerNotLearned+aToB.DropPeerUpdateRejected+aToB.DropWriteError {
		t.Fatalf("a->b drops total mismatch")
	}

	bToA := snapshots[1]
	if bToA.Direction != proxyDirectionBToA {
		t.Fatalf("unexpected direction: %s", bToA.Direction)
	}
	if bToA.PktsIn != 7 || bToA.PktsOut != 6 {
		t.Fatalf("unexpected b->a packet split: in=%d out=%d", bToA.PktsIn, bToA.PktsOut)
	}
	if bToA.DropsTotal != bToA.DropDestNotSet+bToA.DropDestIPMismatch+bToA.DropPeerNotLearned+bToA.DropPeerUpdateRejected+bToA.DropWriteError {
		t.Fatalf("b->a drops total mismatch")
	}
}

func TestSnapshotVideoDirectionalStatsSplitAndDropReasons(t *testing.T) {
	var counters videoCounters
	counters.aToB.pktsIn.Add(11)
	counters.aToB.pktsOut.Add(9)
	counters.aToB.videoFramesStarted.Add(3)
	counters.aToB.videoKeyframes.Add(1)
	counters.aToB.videoInjectedSPS.Add(1)
	counters.aToB.videoInjectedPPS.Add(1)
	counters.aToB.videoForcedFlushes.Add(2)
	counters.aToB.videoNalParseErrors.Add(4)
	counters.aToB.videoSeqGaps.Add(5)
	counters.aToB.dropDestNotSet.Add(1)
	counters.aToB.dropWriteError.Add(2)

	counters.bToA.pktsIn.Add(6)
	counters.bToA.pktsOut.Add(6)
	counters.bToA.dropDestIPMismatch.Add(7)
	counters.bToA.dropPeerNotLearned.Add(8)

	snapshots := snapshotVideoDirectionalStats(&counters)
	if len(snapshots) != 2 {
		t.Fatalf("unexpected snapshots len: %d", len(snapshots))
	}

	aToB := snapshots[0]
	if aToB.Direction != proxyDirectionAToB {
		t.Fatalf("unexpected direction: %s", aToB.Direction)
	}
	if aToB.VideoFramesStarted != 3 || aToB.VideoKeyframes != 1 {
		t.Fatalf("unexpected a->b video metrics: frames=%d keyframes=%d", aToB.VideoFramesStarted, aToB.VideoKeyframes)
	}
	if aToB.DropsTotal != aToB.DropDestNotSet+aToB.DropDestIPMismatch+aToB.DropPeerNotLearned+aToB.DropPeerUpdateRejected+aToB.DropWriteError {
		t.Fatalf("a->b drops total mismatch")
	}

	bToA := snapshots[1]
	if bToA.Direction != proxyDirectionBToA {
		t.Fatalf("unexpected direction: %s", bToA.Direction)
	}
	if bToA.VideoFramesStarted != 0 || bToA.VideoNalParseErrors != 0 {
		t.Fatalf("expected b->a fix counters to stay zero")
	}
	if bToA.DropsTotal != bToA.DropDestNotSet+bToA.DropDestIPMismatch+bToA.DropPeerNotLearned+bToA.DropPeerUpdateRejected+bToA.DropWriteError {
		t.Fatalf("b->a drops total mismatch")
	}
}

func TestProxyStatsLogsUseDropsTotalInsteadOfAggregateDrops(t *testing.T) {
	t.Run("audio", func(t *testing.T) {
		session := &Session{ID: "log-audio"}
		session.audioEnabled.Store(true)
		session.audioCounters.aToB.pktsIn.Add(1)
		session.audioCounters.aToB.dropDestNotSet.Add(1)
		proxy := &audioProxy{session: session, peerLearningTracker: newPeerLearningTracker(1, time.Second, time.Second, nil, "log-audio", proxyDirectionAToB, "audio")}

		buf := bytes.Buffer{}
		proxy.logger = slog.New(slog.NewJSONHandler(&buf, nil))
		proxy.logStats(false)
		logLine := buf.String()
		if !strings.Contains(logLine, "\"drops_total\"") {
			t.Fatalf("expected drops_total in audio log: %s", logLine)
		}
		if strings.Contains(logLine, "\"drops\":") {
			t.Fatalf("unexpected legacy aggregate drops field in audio log: %s", logLine)
		}
		if !strings.Contains(logLine, "\"learned_peer\":\"none\"") {
			t.Fatalf("expected learned_peer=none in audio log: %s", logLine)
		}

		buf.Reset()
		_ = proxy.peerLearningTracker.observe(&net.UDPAddr{IP: net.ParseIP("10.0.0.5"), Port: 34567}, true, time.Now())
		proxy.logStats(false)
		if !strings.Contains(buf.String(), "\"learned_peer\":\"10.0.0.5:34567\"") {
			t.Fatalf("expected learned_peer with learned address in audio log: %s", buf.String())
		}
	})

	t.Run("video", func(t *testing.T) {
		session := &Session{ID: "log-video"}
		session.videoEnabled.Store(true)
		session.videoCounters.aToB.pktsIn.Add(1)
		session.videoCounters.aToB.dropDestNotSet.Add(1)
		proxy := &videoProxy{session: session, peerLearningTracker: newPeerLearningTracker(1, time.Second, time.Second, nil, "log-video", proxyDirectionAToB, "video")}

		buf := bytes.Buffer{}
		proxy.logger = slog.New(slog.NewJSONHandler(&buf, nil))
		proxy.logStats(false)
		logLine := buf.String()
		if !strings.Contains(logLine, "\"drops_total\"") {
			t.Fatalf("expected drops_total in video log: %s", logLine)
		}
		if strings.Contains(logLine, "\"drops\":") {
			t.Fatalf("unexpected legacy aggregate drops field in video log: %s", logLine)
		}
		if !strings.Contains(logLine, "\"learned_peer\":\"none\"") {
			t.Fatalf("expected learned_peer=none in video log: %s", logLine)
		}

		buf.Reset()
		_ = proxy.peerLearningTracker.observe(&net.UDPAddr{IP: net.ParseIP("10.0.0.6"), Port: 45678}, true, time.Now())
		proxy.logStats(false)
		if !strings.Contains(buf.String(), "\"learned_peer\":\"10.0.0.6:45678\"") {
			t.Fatalf("expected learned_peer with learned address in video log: %s", buf.String())
		}
	})
}

func TestProxySourcesLoggedOnlyOnFinal(t *testing.T) {
	t.Run("audio", func(t *testing.T) {
		session := &Session{ID: "audio-final-sources"}
		session.audioEnabled.Store(true)
		proxy := &audioProxy{session: session, peerLearningTracker: newPeerLearningTracker(1, time.Second, time.Second, nil, "log-audio", proxyDirectionAToB, "audio")}

		proxy.aToBSources.observe(&net.UDPAddr{IP: net.ParseIP("10.0.0.10"), Port: 1000})
		proxy.bToASources.observe(&net.UDPAddr{IP: net.ParseIP("10.0.0.11"), Port: 1001})

		buf := bytes.Buffer{}
		proxy.logger = slog.New(slog.NewJSONHandler(&buf, nil))
		proxy.logStats(false)
		if strings.Contains(buf.String(), "audio.proxy.sources") {
			t.Fatalf("audio.proxy.sources should not be logged for non-final stats: %s", buf.String())
		}

		buf.Reset()
		proxy.logStats(true)
		logLine := buf.String()
		if !strings.Contains(logLine, "audio.proxy.sources") {
			t.Fatalf("expected audio.proxy.sources for final stats: %s", logLine)
		}
		if !strings.Contains(logLine, "10.0.0.10:1000=1") || !strings.Contains(logLine, "10.0.0.11:1001=1") {
			t.Fatalf("expected source counts in audio final log: %s", logLine)
		}
	})

	t.Run("video", func(t *testing.T) {
		session := &Session{ID: "video-final-sources"}
		session.videoEnabled.Store(true)
		proxy := &videoProxy{session: session, peerLearningTracker: newPeerLearningTracker(1, time.Second, time.Second, nil, "log-video", proxyDirectionAToB, "video")}

		proxy.aToBSources.observe(&net.UDPAddr{IP: net.ParseIP("10.0.0.20"), Port: 2000})
		proxy.bToASources.observe(&net.UDPAddr{IP: net.ParseIP("10.0.0.21"), Port: 2001})

		buf := bytes.Buffer{}
		proxy.logger = slog.New(slog.NewJSONHandler(&buf, nil))
		proxy.logStats(false)
		if strings.Contains(buf.String(), "video.proxy.sources") {
			t.Fatalf("video.proxy.sources should not be logged for non-final stats: %s", buf.String())
		}

		buf.Reset()
		proxy.logStats(true)
		logLine := buf.String()
		if !strings.Contains(logLine, "video.proxy.sources") {
			t.Fatalf("expected video.proxy.sources for final stats: %s", logLine)
		}
		if !strings.Contains(logLine, "10.0.0.20:2000=1") || !strings.Contains(logLine, "10.0.0.21:2001=1") {
			t.Fatalf("expected source counts in video final log: %s", logLine)
		}
	})
}
