package session

import (
	"net"
	"testing"
	"time"
)

type noopProxy struct{}

func (p *noopProxy) start() {}
func (p *noopProxy) stop()  {}

func newTestManager(t *testing.T, idleTimeout time.Duration) *Manager {
	t.Helper()
	allocator, err := NewPortAllocator(14000, 14010)
	if err != nil {
		t.Fatalf("unexpected allocator error: %v", err)
	}
	return newManagerWithDeps(
		allocator,
		0,
		0,
		idleTimeout,
		false,
		ProxyLogConfig{},
		managerDeps{
			startReaper: false,
			now:         func() time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) },
			listenUDP:   func(string, *net.UDPAddr) (*net.UDPConn, error) { return nil, nil },
			newAudioProxy: func(*Session, *net.UDPConn, *net.UDPConn, time.Duration, ProxyLogConfig) sessionProxy {
				return &noopProxy{}
			},
			newVideoProxy: func(*Session, *net.UDPConn, *net.UDPConn, time.Duration, time.Duration, bool, bool, ProxyLogConfig) sessionProxy {
				return &noopProxy{}
			},
		},
	)
}

// TestManager_CreateStoresSessionAndReturnsID verifies that Create generates a
// non-empty session ID and that the created session is stored in the manager
// map. This matters because the API relies on stable IDs to address sessions
// later. Preconditions: a deterministic manager instance with UDP networking
// disabled and a port allocator range large enough for one session. Inputs: a
// call ID and tags with videoFix enabled. Edge case: none, as Create should
// always set an ID. The expected output is a session with a non-empty ID, and a
// subsequent Get using that ID returns the same session data. Assertions are
// stable because ID creation is deterministic for uniqueness and storage is
// guarded by the manager mutex. Flakiness is avoided by removing UDP sockets and
// goroutines. A regression would show an empty ID, a missing session on Get, or
// a mismatch between created and stored data.
func TestManager_CreateStoresSessionAndReturnsID(t *testing.T) {
	manager := newTestManager(t, 0)
	created, err := manager.Create("call-1", "from-1", "to-1", true)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("expected non-empty session ID")
	}
	stored, ok := manager.Get(created.ID)
	if !ok {
		t.Fatalf("expected session to be stored")
	}
	if stored.ID != created.ID {
		t.Fatalf("expected stored ID %q, got %q", created.ID, stored.ID)
	}
}

// TestManager_UpdateSetsDestIndependentlyAudioVideo verifies that audio and
// video RTP destinations are updated independently, preventing one media update
// from overwriting the other. This matters because callers may update only one
// leg at a time. Preconditions: a created session with no destinations set.
// Inputs: first update audio destination only, then update video destination
// only. Edge case: nil destinations must leave the opposite media unchanged.
// The expected output is that audio destination remains set after the video-only
// update, and video destination is set after the audio-only update. Assertions
// are stable because UpdateRTPDest only assigns when non-nil. Flakiness is
// avoided by using deterministic addresses and no concurrency. A regression
// would show a nil audio destination after a video update or vice versa.
func TestManager_UpdateSetsDestIndependentlyAudioVideo(t *testing.T) {
	manager := newTestManager(t, 0)
	created, err := manager.Create("call-2", "from-2", "to-2", false)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	audioDest := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 9000}
	if _, ok := manager.UpdateRTPDest(created.ID, audioDest, nil); !ok {
		t.Fatalf("expected update to succeed")
	}
	videoDest := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9002}
	if _, ok := manager.UpdateRTPDest(created.ID, nil, videoDest); !ok {
		t.Fatalf("expected update to succeed")
	}
	updated, ok := manager.Get(created.ID)
	if !ok {
		t.Fatalf("expected session to be present")
	}
	if updated.Audio.RTPEngineDest == nil || updated.Audio.RTPEngineDest.String() != audioDest.String() {
		t.Fatalf("expected audio dest to remain %s", audioDest.String())
	}
	if updated.Video.RTPEngineDest == nil || updated.Video.RTPEngineDest.String() != videoDest.String() {
		t.Fatalf("expected video dest to be %s", videoDest.String())
	}
}

// TestManager_UpdateRTPDest_DisablesMediaOnPortZero verifies that a port 0
// destination disables the media, clears the destination, and sets a disabled
// reason. This matters because SDP can signal disabled media via port 0 and the
// manager must reflect that state. Preconditions: a created session. Inputs:
// update with a valid video destination, then update with port 0. Edge case:
// switching from enabled to disabled should clear the stored destination. The
// expected output is enabled true with empty reason after the first update, and
// enabled false with nil destination and reason "rtpengine_port_0" after the
// second update. Assertions are stable because updates are synchronous under
// the manager lock. Flakiness is avoided by removing background goroutines and
// network usage. A regression would keep the destination set or fail to update
// enabled/disabled flags.
func TestManager_UpdateRTPDest_DisablesMediaOnPortZero(t *testing.T) {
	manager := newTestManager(t, 0)
	created, err := manager.Create("call-6", "from-6", "to-6", false)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	videoDest := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 9002}
	if _, ok := manager.UpdateRTPDest(created.ID, nil, videoDest); !ok {
		t.Fatalf("expected update to succeed")
	}
	enabledSession, ok := manager.Get(created.ID)
	if !ok {
		t.Fatalf("expected session to be present")
	}
	if !enabledSession.Video.Enabled {
		t.Fatalf("expected video to be enabled")
	}
	if enabledSession.Video.DisabledReason != "" {
		t.Fatalf("expected empty disabled reason, got %q", enabledSession.Video.DisabledReason)
	}
	if enabledSession.Video.RTPEngineDest == nil {
		t.Fatalf("expected video dest to be set")
	}

	disableDest := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 0}
	if _, ok := manager.UpdateRTPDest(created.ID, nil, disableDest); !ok {
		t.Fatalf("expected update to succeed")
	}
	disabledSession, ok := manager.Get(created.ID)
	if !ok {
		t.Fatalf("expected session to be present")
	}
	if disabledSession.Video.Enabled {
		t.Fatalf("expected video to be disabled")
	}
	if disabledSession.Video.DisabledReason != "rtpengine_port_0" {
		t.Fatalf("expected disabled reason %q, got %q", "rtpengine_port_0", disabledSession.Video.DisabledReason)
	}
	if disabledSession.Video.RTPEngineDest != nil {
		t.Fatalf("expected video dest to be nil when disabled")
	}
}

// TestManager_CreateWithInitialDest_AppliesDestinations verifies that initial
// RTP destinations are applied at creation time, including disabling media when
// port 0 is provided. This matters because callers must be able to set initial
// media state without an extra update call. Preconditions: a deterministic
// manager instance with UDP networking disabled. Inputs: create with audio
// destination port 40100 and video destination port 0. Edge case: port 0
// disables media and clears destination. The expected output is enabled audio
// with a non-nil destination and disabled video with nil destination and reason
// "rtpengine_port_0". Assertions are stable because the manager uses
// deterministic inputs and no concurrency. Flakiness is avoided by using stub
// proxies and no timers. A regression would leave video enabled or set a
// non-nil destination for port 0.
func TestManager_CreateWithInitialDest_AppliesDestinations(t *testing.T) {
	manager := newTestManager(t, 0)
	audioDest := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 10), Port: 40100}
	videoDest := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 20), Port: 0}

	created, err := manager.CreateWithInitialDest("call-7", "from-7", "to-7", false, audioDest, videoDest)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}

	if !created.Audio.Enabled {
		t.Fatalf("expected audio to be enabled")
	}
	if created.Audio.RTPEngineDest == nil {
		t.Fatalf("expected audio dest to be set")
	}
	if created.Video.Enabled {
		t.Fatalf("expected video to be disabled")
	}
	if created.Video.RTPEngineDest != nil {
		t.Fatalf("expected video dest to be nil when disabled")
	}
	if created.Video.DisabledReason != "rtpengine_port_0" {
		t.Fatalf("expected disabled reason %q, got %q", "rtpengine_port_0", created.Video.DisabledReason)
	}
}

// TestManager_DeleteRemovesSession ensures that Delete removes a session from
// the manager and returns true for existing IDs. This matters because cleanup
// must release resources and prevent future lookups. Preconditions: a manager
// with one created session. Inputs: delete using the session ID. Edge case: the
// session should be missing immediately after deletion. The expected output is
// a true delete result and a subsequent Get that returns false. Assertions are
// stable because Delete locks the map and removes the entry synchronously.
// Flakiness is avoided by disabling background goroutines and network usage. A
// regression would show Delete returning false for an existing session or the
// session still being returned by Get after deletion.
func TestManager_DeleteRemovesSession(t *testing.T) {
	manager := newTestManager(t, 0)
	created, err := manager.Create("call-3", "from-3", "to-3", false)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	if !manager.Delete(created.ID) {
		t.Fatalf("expected delete to succeed")
	}
	if _, ok := manager.Get(created.ID); ok {
		t.Fatalf("expected session to be removed")
	}
}

// TestManager_IdleCleanup_RemovesOnlyIdleSessions validates deterministic idle
// cleanup by invoking Cleanup with a controlled timestamp and verifying that
// only sessions exceeding the idle timeout are removed. This matters because
// production uses timers and we need deterministic unit tests that avoid time
// races. Preconditions: idle timeout configured and reaper goroutine disabled.
// Inputs: two sessions with manual LastActivity values, one older than the
// timeout and one newer, then Cleanup with a fixed "now". Edge case: last
// activity exactly on the threshold is treated as idle when now-sub-last >=
// timeout. The expected output is that only the idle session is removed from the
// manager map. Assertions are stable because Cleanup uses explicit timestamps
// without sleeps. Flakiness is avoided by controlling time and avoiding UDP or
// goroutines. A regression would remove the active session or fail to remove the
// idle session.
func TestManager_IdleCleanup_RemovesOnlyIdleSessions(t *testing.T) {
	idleTimeout := 5 * time.Minute
	manager := newTestManager(t, idleTimeout)
	createdIdle, err := manager.Create("call-4", "from-4", "to-4", false)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	createdActive, err := manager.Create("call-5", "from-5", "to-5", false)
	if err != nil {
		t.Fatalf("unexpected create error: %v", err)
	}
	now := time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC)
	manager.mu.Lock()
	manager.sessions[createdIdle.ID].setLastActivity(now.Add(-idleTimeout - time.Second))
	manager.sessions[createdActive.ID].setLastActivity(now.Add(-idleTimeout + time.Second))
	manager.mu.Unlock()

	manager.Cleanup(now)

	if _, ok := manager.Get(createdIdle.ID); ok {
		t.Fatalf("expected idle session to be removed")
	}
	if _, ok := manager.Get(createdActive.ID); !ok {
		t.Fatalf("expected active session to remain")
	}
}
