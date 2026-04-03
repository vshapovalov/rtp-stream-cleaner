package integration_test

import (
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestIntegrationIPv6MediaProxyEndToEnd(t *testing.T) {
	env := baseEnv("10")
	env["PUBLIC_IP_V6"] = "::1"
	instance, cleanup := startRtpCleaner(t, env)
	t.Cleanup(cleanup)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHealth(instance.BaseURL, 2*time.Second); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	var createReq createSessionRequest
	createReq.CallID = "call-ipv6"
	createReq.FromTag = "from-ipv6"
	createReq.ToTag = "to-ipv6"
	createReq.IsIPv6 = true
	createReq.Audio.Enable = true
	createReq.Video.Enable = true
	createResp, err := createSession(t, client, instance.BaseURL, createReq)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	recvAudioPort := freeUDPPortOnIP(t, "::1")
	recvVideoPort := freeUDPPortOnIP(t, "::1")
	recvPCAP := filepath.Join(t.TempDir(), "recv_ipv6.pcap")
	recvErr := make(chan error, 1)
	go func() {
		recvErr <- rtpPeerRecvPCAP(t, rtpPeerRecvConfig{
			BindIP:    "::1",
			AudioPort: recvAudioPort,
			VideoPort: recvVideoPort,
			RecvPCAP:  recvPCAP,
			Duration:  5 * time.Second,
			Timeout:   12 * time.Second,
		})
	}()

	audioDest := fmt.Sprintf("[::1]:%d", recvAudioPort)
	videoDest := fmt.Sprintf("[::1]:%d", recvVideoPort)
	_, status, err := updateSession(t, client, instance.BaseURL, createResp.ID, updateSessionRequest{
		Audio: &updateMediaRequest{RTPEngineDest: &audioDest},
		Video: &updateMediaRequest{RTPEngineDest: &videoDest},
	})
	if err != nil {
		t.Fatalf("update session destinations: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("update session destinations: expected 200, got %d", status)
	}

	if err := rtpPeerSendPCAP(t, rtpPeerSendConfig{
		BindIP:    "::1",
		AudioPort: freeUDPPortOnIP(t, "::1"),
		VideoPort: freeUDPPortOnIP(t, "::1"),
		AudioTo:   fmt.Sprintf("[::1]:%d", createResp.Audio.APort),
		VideoTo:   fmt.Sprintf("[::1]:%d", createResp.Video.APort),
		AudioSSRC: normalAudioSSRC,
		VideoSSRC: normalVideoSSRC,
		SendPCAP:  filepath.Join(repoRoot(t), "testdata", "normal.pcap"),
		Duration:  2 * time.Second,
		Timeout:   10 * time.Second,
	}); err != nil {
		t.Fatalf("rtppeer send: %v", err)
	}

	if _, err := waitForSessionCondition(t, client, instance.BaseURL, createResp.ID, 4*time.Second, func(resp sessionStateResponse) bool {
		return resp.AudioBOutPkts > 0 && resp.VideoBOutPkts > 0
	}); err != nil {
		t.Fatalf("wait for IPv6 forwarding: %v", err)
	}

	if err := <-recvErr; err != nil {
		t.Fatalf("rtppeer recv: %v", err)
	}

	sources, err := rtpPeerListSources(t, recvPCAP)
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	if packetsForSSRC(sources, normalAudioSSRC) == 0 {
		t.Fatalf("expected IPv6 audio packets in recv pcap")
	}
	if packetsForSSRC(sources, normalVideoSSRC) == 0 {
		t.Fatalf("expected IPv6 video packets in recv pcap")
	}
}

func freeUDPPortOnIP(t *testing.T, bindIP string) int {
	t.Helper()
	ip := net.ParseIP(bindIP)
	if ip == nil {
		t.Fatalf("parse bind ip %s", bindIP)
	}
	network := "udp4"
	if ip.To4() == nil {
		network = "udp6"
	}
	conn, err := net.ListenUDP(network, &net.UDPAddr{IP: ip, Port: 0})
	if err != nil {
		t.Fatalf("listen udp on %s: %v", bindIP, err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}
