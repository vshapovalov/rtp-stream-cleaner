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
