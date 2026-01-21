package integration_test

import (
	"net/http"
	"testing"
	"time"
)

// TestRtpCleanerSmokeSessionLifecycle validates the session-management API workflow
// for the rtp-cleaner control plane, focusing on CRUD correctness rather than RTP
// proxying behavior (no media packets are exchanged). The topology concept is still
// the standard A-leg (doorphone -> cleaner) and B-leg (cleaner -> rtpengine_dest),
// but this smoke test only exercises the HTTP API that provisions those legs.
// No external helper process is required here; the rtppeer helper is intentionally
// unused because this test only proves the control plane responds and returns stable
// session metadata. The inputs are simple JSON bodies (no PCAPs or SSRCs) so that
// the assertions remain deterministic: we assert a 200 OK health response, creation
// returns a non-empty session ID plus allocated ports, GET returns the same ID, and
// DELETE removes the session so a subsequent GET returns 404. Stability is ensured
// by running the service on a dynamically chosen localhost port and polling the
// /v1/health endpoint with bounded retries before issuing requests. Any non-200
// health response, missing session ID/ports, or failure to return 404 after delete
// would indicate a regression in session lifecycle handling or API routing.
func TestRtpCleanerSmokeSessionLifecycle(t *testing.T) {
	instance, cleanup := startRtpCleaner(t, nil)
	t.Cleanup(cleanup)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHealth(instance.BaseURL, 2*time.Second); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	var createReq createSessionRequest
	createReq.CallID = "smoke-call"
	createReq.FromTag = "from-tag"
	createReq.ToTag = "to-tag"
	createReq.Audio.Enable = true
	createReq.Video.Enable = true
	createResp, err := createSession(t, client, instance.BaseURL, createReq)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if createResp.ID == "" {
		t.Fatalf("create session: empty id")
	}
	if createResp.Audio.APort == 0 || createResp.Audio.BPort == 0 {
		t.Fatalf("create session: missing audio ports")
	}
	if createResp.Video.APort == 0 || createResp.Video.BPort == 0 {
		t.Fatalf("create session: missing video ports")
	}

	gotSession, status, err := getSession(t, client, instance.BaseURL, createResp.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("get session: expected 200, got %d", status)
	}
	if gotSession.ID != createResp.ID {
		t.Fatalf("get session: expected id %s, got %s", createResp.ID, gotSession.ID)
	}

	status, err = deleteSession(t, client, instance.BaseURL, createResp.ID)
	if err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("delete session: expected 200, got %d", status)
	}

	assertNotFound(t, client, instance.BaseURL, createResp.ID)
}
