package integration_test

import (
	"bytes"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func baseEnv(idleTimeoutSec string) map[string]string {
	env := map[string]string{
		"PUBLIC_IP":                "127.0.0.1",
		"INTERNAL_IP":              "127.0.0.1",
		"PEER_LEARNING_WINDOW_SEC": "1",
		"MAX_FRAME_WAIT_MS":        "150",
		"RTP_PORT_MIN":             "35000",
		"RTP_PORT_MAX":             "35020",
	}
	if idleTimeoutSec != "" {
		env["IDLE_TIMEOUT_SEC"] = idleTimeoutSec
	}
	return env
}

func waitForSessionCondition(t *testing.T, client *http.Client, baseURL, id string, timeout time.Duration, cond func(sessionStateResponse) bool) (sessionStateResponse, error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, status, err := getSession(t, client, baseURL, id)
		if err != nil {
			return resp, err
		}
		if status == http.StatusOK && cond(resp) {
			return resp, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return sessionStateResponse{}, fmt.Errorf("timeout waiting for session condition")
}

func waitForSessionNotFound(t *testing.T, client *http.Client, baseURL, id string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, status, err := getSession(t, client, baseURL, id)
		if err != nil {
			return err
		}
		if status == http.StatusNotFound {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for 404")
}

func packetsForSSRC(stats []rtpPeerSourceStats, ssrc uint32) int {
	for _, entry := range stats {
		if entry.SSRC == ssrc {
			return entry.Packets
		}
	}
	return 0
}

// TestIntegrationA1CreateGetDelete validates the happy-path CRUD flow for a full
// audio+video session. Topology: A-leg (doorphone/rtppeer sender) would target the
// returned audio/video A ports, while B-leg (rtp-cleaner -> rtpengine_dest) would be
// pointed at an external receiver, but this test stays control-plane only so no
// PCAP is sent and no SSRCs are exercised. Ports are still allocated deterministically
// from RTP_PORT_MIN=35000..RTP_PORT_MAX=35020, which makes the responses stable. We
// assert that POST returns a session ID and ports, GET echoes the same ID, DELETE
// returns 200, and a follow-up GET yields 404. The counters are stable because we
// do not send media: audio/video packet counters should remain at zero implicitly.
// Env used: PUBLIC_IP/INTERNAL_IP=127.0.0.1, PEER_LEARNING_WINDOW_SEC=1,
// IDLE_TIMEOUT_SEC=10, MAX_FRAME_WAIT_MS=150, RTP_PORT_MIN/MAX. We avoid flakes by
// polling /v1/health before requests and by polling API responses instead of using
// fixed sleeps.
func TestIntegrationA1CreateGetDelete(t *testing.T) {
	instance, cleanup := startRtpCleaner(t, baseEnv("10"))
	t.Cleanup(cleanup)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHealth(instance.BaseURL, 2*time.Second); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	var createReq createSessionRequest
	createReq.CallID = "call-a1"
	createReq.FromTag = "from-a1"
	createReq.ToTag = "to-a1"
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

// TestIntegrationA2CreateValidation exercises negative validation on session create.
// Topology is still A-leg/B-leg, but we never allocate ports because the request
// fails before provisioning. No PCAP/SSRCs are involved. The counters are irrelevant
// here; we assert the API returns 400 for malformed input (wrong field type) to
// ensure schema validation is enforced. Env used: PUBLIC_IP/INTERNAL_IP=127.0.0.1,
// PEER_LEARNING_WINDOW_SEC=1, IDLE_TIMEOUT_SEC=10, MAX_FRAME_WAIT_MS=150,
// RTP_PORT_MIN/MAX. We avoid flakes by polling /v1/health before the request and
// by checking the HTTP response directly (no sleeps, no timing assumptions).
func TestIntegrationA2CreateValidation(t *testing.T) {
	instance, cleanup := startRtpCleaner(t, baseEnv("10"))
	t.Cleanup(cleanup)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHealth(instance.BaseURL, 2*time.Second); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	payload := []byte(`{"call_id":123,"from_tag":"from-a2","to_tag":"to-a2","audio":{"enable":true}}`)
	req, err := http.NewRequest(http.MethodPost, withAccessToken(instance.BaseURL+"/v1/session"), bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid create, got %d", resp.StatusCode)
	}
}

// TestIntegrationA3UpdateUnknown checks that updating a nonexistent session is
// rejected with 404. Topology is still conceptual A-leg/B-leg, but no session is
// allocated, so no ports, PCAPs, or SSRCs are used. Counters remain unused. Env
// used: PUBLIC_IP/INTERNAL_IP=127.0.0.1, PEER_LEARNING_WINDOW_SEC=1,
// IDLE_TIMEOUT_SEC=10, MAX_FRAME_WAIT_MS=150, RTP_PORT_MIN/MAX. We avoid flakes
// by polling /v1/health and issuing a single HTTP request without sleeps.
func TestIntegrationA3UpdateUnknown(t *testing.T) {
	instance, cleanup := startRtpCleaner(t, baseEnv("10"))
	t.Cleanup(cleanup)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHealth(instance.BaseURL, 2*time.Second); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	updateReq := updateSessionRequest{Audio: &updateMediaRequest{RTPEngineDest: stringPtr("127.0.0.1:35000")}}
	_, status, err := updateSession(t, client, instance.BaseURL, "nonexistent", updateReq)
	if err != nil {
		t.Fatalf("update session: %v", err)
	}
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 for update of unknown session, got %d", status)
	}
}

// TestIntegrationA4PartialUpdate verifies that partial updates only modify the
// targeted media leg. Topology: A-leg ports accept inbound RTP (unused here) and
// B-leg destinations are set via updates. No PCAP is exchanged, so SSRCs are not
// involved. Counters are stable (zero) because we never send media; we assert the
// API state instead: first update sets audio rtpengine_dest only, then video update
// sets video dest while preserving the earlier audio dest. Env used:
// PUBLIC_IP/INTERNAL_IP=127.0.0.1, PEER_LEARNING_WINDOW_SEC=1, IDLE_TIMEOUT_SEC=10,
// MAX_FRAME_WAIT_MS=150, RTP_PORT_MIN/MAX. We avoid flakes by polling /v1/health
// and relying on deterministic GET responses rather than sleeps.
func TestIntegrationA4PartialUpdate(t *testing.T) {
	instance, cleanup := startRtpCleaner(t, baseEnv("10"))
	t.Cleanup(cleanup)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHealth(instance.BaseURL, 2*time.Second); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	var createReq createSessionRequest
	createReq.CallID = "call-a4"
	createReq.FromTag = "from-a4"
	createReq.ToTag = "to-a4"
	createReq.Audio.Enable = true
	createReq.Video.Enable = true
	createResp, err := createSession(t, client, instance.BaseURL, createReq)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	audioDest := fmt.Sprintf("127.0.0.1:%d", freeUDPPort(t))
	updateReq := updateSessionRequest{Audio: &updateMediaRequest{RTPEngineDest: &audioDest}}
	updateResp, status, err := updateSession(t, client, instance.BaseURL, createResp.ID, updateReq)
	if err != nil {
		t.Fatalf("update session audio: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("update session audio: expected 200, got %d", status)
	}
	if updateResp.Audio.RTPEngineDest != audioDest {
		t.Fatalf("update session audio: expected %s, got %s", audioDest, updateResp.Audio.RTPEngineDest)
	}
	if updateResp.Video.RTPEngineDest != "" {
		t.Fatalf("update session audio: expected empty video dest, got %s", updateResp.Video.RTPEngineDest)
	}

	videoDest := fmt.Sprintf("127.0.0.1:%d", freeUDPPort(t))
	updateReq = updateSessionRequest{Video: &updateMediaRequest{RTPEngineDest: &videoDest}}
	updateResp, status, err = updateSession(t, client, instance.BaseURL, createResp.ID, updateReq)
	if err != nil {
		t.Fatalf("update session video: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("update session video: expected 200, got %d", status)
	}
	if updateResp.Audio.RTPEngineDest != audioDest {
		t.Fatalf("update session video: expected audio dest %s, got %s", audioDest, updateResp.Audio.RTPEngineDest)
	}
	if updateResp.Video.RTPEngineDest != videoDest {
		t.Fatalf("update session video: expected %s, got %s", videoDest, updateResp.Video.RTPEngineDest)
	}
}

// TestIntegrationA5DeleteActiveStopsTraffic validates that deleting an active
// session halts forwarding. Topology: rtppeer sender injects audio RTP (SSRC
// 0xedcc15a7 from testdata/normal.pcap) into the A-leg audio port; rtp-cleaner
// forwards to the B-leg destination where rtppeer receiver listens and writes
// recv.pcap. We create an audio-only session so video is disabled and use a
// dummy video port for rtppeer. Counters: audio_a_in_pkts reflects packets
// received on the A-leg; audio_b_out_pkts reflects forwarded packets to the
// B-leg. We poll GET until audio_b_out_pkts > 0 (no sleeps) to prove forwarding,
// then DELETE the session and verify the receiver PCAP packet count stops
// increasing across two list-sources reads. Env used: PUBLIC_IP/INTERNAL_IP=127.0.0.1,
// PEER_LEARNING_WINDOW_SEC=1, IDLE_TIMEOUT_SEC=10, MAX_FRAME_WAIT_MS=150,
// RTP_PORT_MIN/MAX. Flake avoidance: API polling for counters, bounded receiver
// duration, and checking for stable packet counts rather than timing assumptions.
func TestIntegrationA5DeleteActiveStopsTraffic(t *testing.T) {
	instance, cleanup := startRtpCleaner(t, baseEnv("10"))
	t.Cleanup(cleanup)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHealth(instance.BaseURL, 2*time.Second); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	var createReq createSessionRequest
	createReq.CallID = "call-a5"
	createReq.FromTag = "from-a5"
	createReq.ToTag = "to-a5"
	createReq.Audio.Enable = true
	createReq.Video.Enable = false
	createResp, err := createSession(t, client, instance.BaseURL, createReq)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	recvPort := freeUDPPort(t)
	recvVideoPort := freeUDPPort(t)
	recvPCAP := filepath.Join(t.TempDir(), "recv.pcap")
	recvErr := make(chan error, 1)
	go func() {
		recvErr <- rtpPeerRecvPCAP(t, rtpPeerRecvConfig{
			AudioPort: recvPort,
			VideoPort: recvVideoPort,
			RecvPCAP:  recvPCAP,
			Duration:  4 * time.Second,
			Timeout:   10 * time.Second,
		})
	}()

	audioDest := fmt.Sprintf("127.0.0.1:%d", recvPort)
	_, status, err := updateSession(t, client, instance.BaseURL, createResp.ID, updateSessionRequest{
		Audio: &updateMediaRequest{RTPEngineDest: &audioDest},
	})
	if err != nil {
		t.Fatalf("update session audio: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("update session audio: expected 200, got %d", status)
	}

	sendErr := rtpPeerSendPCAP(t, rtpPeerSendConfig{
		AudioPort: freeUDPPort(t),
		VideoPort: freeUDPPort(t),
		AudioTo:   fmt.Sprintf("127.0.0.1:%d", createResp.Audio.APort),
		VideoTo:   fmt.Sprintf("127.0.0.1:%d", createResp.Video.APort),
		AudioSSRC: normalAudioSSRC,
		VideoSSRC: normalVideoSSRC,
		SendPCAP:  filepath.Join(repoRoot(t), "testdata", "normal.pcap"),
		Duration:  2 * time.Second,
		Timeout:   10 * time.Second,
	})
	if sendErr != nil {
		t.Fatalf("rtppeer send: %v", sendErr)
	}

	if _, err := waitForSessionCondition(t, client, instance.BaseURL, createResp.ID, 3*time.Second, func(resp sessionStateResponse) bool {
		return resp.AudioBOutPkts > 0
	}); err != nil {
		t.Fatalf("wait for audio forwarding: %v", err)
	}

	status, err = deleteSession(t, client, instance.BaseURL, createResp.ID)
	if err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("delete session: expected 200, got %d", status)
	}

	if err := waitForSessionNotFound(t, client, instance.BaseURL, createResp.ID, 2*time.Second); err != nil {
		t.Fatalf("wait for delete: %v", err)
	}

	firstStats, err := rtpPeerListSources(t, recvPCAP)
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	firstPackets := packetsForSSRC(firstStats, normalAudioSSRC)
	if firstPackets == 0 {
		t.Fatalf("expected captured audio packets, got 0")
	}

	time.Sleep(300 * time.Millisecond)

	secondStats, err := rtpPeerListSources(t, recvPCAP)
	if err != nil {
		t.Fatalf("list sources second: %v", err)
	}
	secondPackets := packetsForSSRC(secondStats, normalAudioSSRC)
	if secondPackets != firstPackets {
		t.Fatalf("expected packet count to stop after delete: %d -> %d", firstPackets, secondPackets)
	}

	if err := <-recvErr; err != nil {
		t.Fatalf("rtppeer recv: %v", err)
	}
}

// TestIntegrationB1IdleAutoDelete validates idle cleanup by ensuring a session
// with no traffic is removed after IDLE_TIMEOUT_SEC. Topology would normally use
// A-leg/B-leg ports, but we intentionally send no PCAP/SSRCs to keep counters at
// zero. We poll GET until a 404 is returned (no sleeps), which is stable because
// the idle reaper is time-driven and uses the environment configuration. Env used:
// PUBLIC_IP/INTERNAL_IP=127.0.0.1, PEER_LEARNING_WINDOW_SEC=1, IDLE_TIMEOUT_SEC=2,
// MAX_FRAME_WAIT_MS=150, RTP_PORT_MIN/MAX. Flake avoidance: bounded polling loop
// rather than a single long sleep, so the assertion tolerates scheduler jitter.
func TestIntegrationB1IdleAutoDelete(t *testing.T) {
	instance, cleanup := startRtpCleaner(t, baseEnv("2"))
	t.Cleanup(cleanup)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHealth(instance.BaseURL, 2*time.Second); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	var createReq createSessionRequest
	createReq.CallID = "call-b1"
	createReq.FromTag = "from-b1"
	createReq.ToTag = "to-b1"
	createReq.Audio.Enable = true
	createReq.Video.Enable = false
	createResp, err := createSession(t, client, instance.BaseURL, createReq)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := waitForSessionNotFound(t, client, instance.BaseURL, createResp.ID, 4*time.Second); err != nil {
		t.Fatalf("expected idle session to be deleted: %v", err)
	}
}

// TestIntegrationB2ActiveSessionNotDeleted ensures active traffic prevents idle
// cleanup. Topology: rtppeer sender injects audio RTP (SSRC 0xedcc15a7 from
// testdata/normal.pcap) into the A-leg audio port; rtp-cleaner forwards to a
// receiver on the B-leg. Counters: audio_a_in_pkts and audio_b_out_pkts should
// advance while sending, and we expect GET to remain 200 during the send even
// though IDLE_TIMEOUT_SEC=2. Env used: PUBLIC_IP/INTERNAL_IP=127.0.0.1,
// PEER_LEARNING_WINDOW_SEC=1, IDLE_TIMEOUT_SEC=2, MAX_FRAME_WAIT_MS=150,
// RTP_PORT_MIN/MAX. Flake avoidance: we poll API counters instead of sleeping
// to detect active traffic and check for 200 responses during the send window.
func TestIntegrationB2ActiveSessionNotDeleted(t *testing.T) {
	instance, cleanup := startRtpCleaner(t, baseEnv("2"))
	t.Cleanup(cleanup)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHealth(instance.BaseURL, 2*time.Second); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	var createReq createSessionRequest
	createReq.CallID = "call-b2"
	createReq.FromTag = "from-b2"
	createReq.ToTag = "to-b2"
	createReq.Audio.Enable = true
	createReq.Video.Enable = false
	createResp, err := createSession(t, client, instance.BaseURL, createReq)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	recvPort := freeUDPPort(t)
	recvVideoPort := freeUDPPort(t)
	recvPCAP := filepath.Join(t.TempDir(), "recv.pcap")
	recvErr := make(chan error, 1)
	go func() {
		recvErr <- rtpPeerRecvPCAP(t, rtpPeerRecvConfig{
			AudioPort: recvPort,
			VideoPort: recvVideoPort,
			RecvPCAP:  recvPCAP,
			Duration:  4 * time.Second,
			Timeout:   10 * time.Second,
		})
	}()

	audioDest := fmt.Sprintf("127.0.0.1:%d", recvPort)
	_, status, err := updateSession(t, client, instance.BaseURL, createResp.ID, updateSessionRequest{
		Audio: &updateMediaRequest{RTPEngineDest: &audioDest},
	})
	if err != nil {
		t.Fatalf("update session audio: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("update session audio: expected 200, got %d", status)
	}

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- rtpPeerSendPCAP(t, rtpPeerSendConfig{
			AudioPort: freeUDPPort(t),
			VideoPort: freeUDPPort(t),
			AudioTo:   fmt.Sprintf("127.0.0.1:%d", createResp.Audio.APort),
			VideoTo:   fmt.Sprintf("127.0.0.1:%d", createResp.Video.APort),
			AudioSSRC: normalAudioSSRC,
			VideoSSRC: normalVideoSSRC,
			SendPCAP:  filepath.Join(repoRoot(t), "testdata", "normal.pcap"),
			Duration:  3 * time.Second,
			Timeout:   10 * time.Second,
		})
	}()

	if _, err := waitForSessionCondition(t, client, instance.BaseURL, createResp.ID, 3*time.Second, func(resp sessionStateResponse) bool {
		return resp.AudioAInPkts > 0 && resp.AudioBOutPkts > 0
	}); err != nil {
		t.Fatalf("wait for active traffic: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, status, err := getSession(t, client, instance.BaseURL, createResp.ID)
		if err != nil {
			t.Fatalf("get session during send: %v", err)
		}
		if status == http.StatusNotFound {
			t.Fatalf("session was deleted while active")
		}
		time.Sleep(150 * time.Millisecond)
	}

	if err := <-sendErr; err != nil {
		t.Fatalf("rtppeer send: %v", err)
	}
	if err := <-recvErr; err != nil {
		t.Fatalf("rtppeer recv: %v", err)
	}
}

// TestIntegrationC1AudioOnlyProxy validates audio-only proxying. Topology: rtppeer
// sender replays testdata/normal.pcap (audio SSRC 0xedcc15a7 and video SSRC
// 0x259989ef), but we route the video leg to an unused UDP sink so rtp-cleaner
// only sees audio on its A-leg. rtp-cleaner is configured with audio enabled and
// video disabled (no video dest update), so only audio should be forwarded to the
// B-leg receiver. We capture recv.pcap and list sources to ensure only the audio
// SSRC appears. Counters: audio_a_in_pkts/audio_b_out_pkts should increase, while
// all video counters remain zero because no video packets reach the cleaner. Env
// used: PUBLIC_IP/INTERNAL_IP=127.0.0.1, PEER_LEARNING_WINDOW_SEC=1,
// IDLE_TIMEOUT_SEC=10, MAX_FRAME_WAIT_MS=150, RTP_PORT_MIN/MAX. Flake avoidance:
// poll API counters for audio activity instead of sleeping and assert video
// counters are zero after the send completes.
func TestIntegrationC1AudioOnlyProxy(t *testing.T) {
	instance, cleanup := startRtpCleaner(t, baseEnv("10"))
	t.Cleanup(cleanup)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHealth(instance.BaseURL, 2*time.Second); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	var createReq createSessionRequest
	createReq.CallID = "call-c1"
	createReq.FromTag = "from-c1"
	createReq.ToTag = "to-c1"
	createReq.Audio.Enable = true
	createReq.Video.Enable = false
	createResp, err := createSession(t, client, instance.BaseURL, createReq)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	recvPort := freeUDPPort(t)
	recvVideoPort := freeUDPPort(t)
	recvPCAP := filepath.Join(t.TempDir(), "recv.pcap")
	recvErr := make(chan error, 1)
	go func() {
		recvErr <- rtpPeerRecvPCAP(t, rtpPeerRecvConfig{
			AudioPort: recvPort,
			VideoPort: recvVideoPort,
			RecvPCAP:  recvPCAP,
			Duration:  4 * time.Second,
			Timeout:   10 * time.Second,
		})
	}()

	audioDest := fmt.Sprintf("127.0.0.1:%d", recvPort)
	_, status, err := updateSession(t, client, instance.BaseURL, createResp.ID, updateSessionRequest{
		Audio: &updateMediaRequest{RTPEngineDest: &audioDest},
	})
	if err != nil {
		t.Fatalf("update session audio: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("update session audio: expected 200, got %d", status)
	}

	videoSinkPort := freeUDPPort(t)
	sendErr := rtpPeerSendPCAP(t, rtpPeerSendConfig{
		AudioPort: freeUDPPort(t),
		VideoPort: freeUDPPort(t),
		AudioTo:   fmt.Sprintf("127.0.0.1:%d", createResp.Audio.APort),
		VideoTo:   fmt.Sprintf("127.0.0.1:%d", videoSinkPort),
		AudioSSRC: normalAudioSSRC,
		VideoSSRC: normalVideoSSRC,
		SendPCAP:  filepath.Join(repoRoot(t), "testdata", "normal.pcap"),
		Duration:  2 * time.Second,
		Timeout:   10 * time.Second,
	})
	if sendErr != nil {
		t.Fatalf("rtppeer send: %v", sendErr)
	}

	finalState, err := waitForSessionCondition(t, client, instance.BaseURL, createResp.ID, 3*time.Second, func(resp sessionStateResponse) bool {
		return resp.AudioBOutPkts > 0
	})
	if err != nil {
		t.Fatalf("wait for audio forwarding: %v", err)
	}
	if finalState.VideoAInPkts != 0 || finalState.VideoBOutPkts != 0 || finalState.VideoBInPkts != 0 || finalState.VideoAOutPkts != 0 {
		t.Fatalf("expected video counters to remain zero, got %+v", finalState)
	}

	if err := <-recvErr; err != nil {
		t.Fatalf("rtppeer recv: %v", err)
	}

	sources, err := rtpPeerListSources(t, recvPCAP)
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	if len(sources) != 1 || sources[0].SSRC != normalAudioSSRC {
		t.Fatalf("expected only audio SSRC 0x%08x, got %+v", normalAudioSSRC, sources)
	}
	if sources[0].Packets == 0 {
		t.Fatalf("expected audio packets in recv pcap")
	}
}

func stringPtr(value string) *string {
	return &value
}
