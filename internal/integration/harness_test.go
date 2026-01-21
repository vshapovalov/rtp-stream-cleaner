package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	normalAudioSSRC  uint32 = 0xedcc15a7
	normalVideoSSRC  uint32 = 0x259989ef
	problemAudioSSRC uint32 = 0x7260ee6c
	problemVideoSSRC uint32 = 0x45db6713
)

type binaryCache struct {
	once sync.Once
	path string
	err  error
}

var rtpCleanerBinary binaryCache
var rtpPeerBinary binaryCache

type rtpPeerSourceStats struct {
	SSRC        uint32
	PayloadType int
	Packets     int
	SPS         int
	PPS         int
	IDR         int
	NonIDR      int
}

type createSessionRequest struct {
	CallID  string `json:"call_id"`
	FromTag string `json:"from_tag"`
	ToTag   string `json:"to_tag"`
	Audio   struct {
		Enable bool `json:"enable"`
	} `json:"audio"`
	Video struct {
		Enable bool  `json:"enable"`
		Fix    *bool `json:"fix,omitempty"`
	} `json:"video"`
}

type updateSessionRequest struct {
	Audio *updateMediaRequest `json:"audio,omitempty"`
	Video *updateMediaRequest `json:"video,omitempty"`
}

type updateMediaRequest struct {
	RTPEngineDest *string `json:"rtpengine_dest,omitempty"`
}

type createSessionResponse struct {
	ID         string       `json:"id"`
	PublicIP   string       `json:"public_ip"`
	InternalIP string       `json:"internal_ip"`
	Audio      portResponse `json:"audio"`
	Video      portResponse `json:"video"`
}

type sessionStateResponse struct {
	ID                   string             `json:"id"`
	CallID               string             `json:"call_id"`
	FromTag              string             `json:"from_tag"`
	ToTag                string             `json:"to_tag"`
	PublicIP             string             `json:"public_ip"`
	InternalIP           string             `json:"internal_ip"`
	Audio                mediaStateResponse `json:"audio"`
	Video                mediaStateResponse `json:"video"`
	AudioAInPkts         uint64             `json:"audio_a_in_pkts"`
	AudioBOutPkts        uint64             `json:"audio_b_out_pkts"`
	AudioBInPkts         uint64             `json:"audio_b_in_pkts"`
	AudioAOutPkts        uint64             `json:"audio_a_out_pkts"`
	VideoAInPkts         uint64             `json:"video_a_in_pkts"`
	VideoBOutPkts        uint64             `json:"video_b_out_pkts"`
	VideoBInPkts         uint64             `json:"video_b_in_pkts"`
	VideoAOutPkts        uint64             `json:"video_a_out_pkts"`
	VideoFramesStarted   uint64             `json:"video_frames_started"`
	VideoFramesEnded     uint64             `json:"video_frames_ended"`
	VideoFramesFlushed   uint64             `json:"video_frames_flushed"`
	VideoForcedFlushes   uint64             `json:"video_forced_flushes"`
	VideoInjectedSPS     uint64             `json:"video_injected_sps"`
	VideoInjectedPPS     uint64             `json:"video_injected_pps"`
	VideoSeqDeltaCurrent uint64             `json:"video_seq_delta_current"`
	State                string             `json:"state"`
}

type portResponse struct {
	APort int `json:"a_port"`
	BPort int `json:"b_port"`
}

type mediaStateResponse struct {
	APort         int    `json:"a_port"`
	BPort         int    `json:"b_port"`
	RTPEngineDest string `json:"rtpengine_dest"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type rtpCleanerInstance struct {
	BaseURL string
	cmd     *exec.Cmd
	output  *bytes.Buffer
}

type rtpPeerSendConfig struct {
	AudioPort int
	VideoPort int
	AudioTo   string
	VideoTo   string
	AudioSSRC uint32
	VideoSSRC uint32
	SendPCAP  string
	RecvPCAP  string
	Pacing    string
	Duration  time.Duration
	Timeout   time.Duration
}

type rtpPeerRecvConfig struct {
	AudioPort int
	VideoPort int
	RecvPCAP  string
	Duration  time.Duration
	Timeout   time.Duration
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected tcp addr type %T", listener.Addr())
	}
	return addr.Port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("unexpected udp addr type %T", conn.LocalAddr())
	}
	return addr.Port
}

func startRtpCleaner(t *testing.T, env map[string]string) (*rtpCleanerInstance, func()) {
	t.Helper()
	binary := buildRtpCleaner(t)
	baseEnv := map[string]string{
		"PUBLIC_IP": "127.0.0.1",
	}
	for key, value := range env {
		baseEnv[key] = value
	}

	apiPort := freeTCPPort(t)
	baseEnv["API_LISTEN_ADDR"] = fmt.Sprintf("127.0.0.1:%d", apiPort)

	cmd := exec.Command(binary)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), flattenEnv(baseEnv)...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("start rtp-cleaner: %v", err)
	}
	instance := &rtpCleanerInstance{
		BaseURL: fmt.Sprintf("http://127.0.0.1:%d", apiPort),
		cmd:     cmd,
		output:  &output,
	}

	cleanup := func() {
		stopProcess(t, cmd, 5*time.Second)
	}

	if err := waitForHealth(instance.BaseURL, 5*time.Second); err != nil {
		cleanup()
		t.Fatalf("rtp-cleaner health: %v\n%s", err, output.String())
	}
	return instance, cleanup
}

func waitForHealth(baseURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(baseURL + "/v1/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for /v1/health")
}

func createSession(t *testing.T, client *http.Client, baseURL string, req createSessionRequest) (createSessionResponse, error) {
	t.Helper()
	var resp createSessionResponse
	status, err := doJSONRequest(client, http.MethodPost, baseURL+"/v1/session", req, &resp)
	if err != nil {
		return resp, err
	}
	if status != http.StatusOK {
		return resp, fmt.Errorf("create session status %d", status)
	}
	return resp, nil
}

func getSession(t *testing.T, client *http.Client, baseURL, id string) (sessionStateResponse, int, error) {
	t.Helper()
	var resp sessionStateResponse
	status, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/session/"+id, nil, &resp)
	return resp, status, err
}

func deleteSession(t *testing.T, client *http.Client, baseURL, id string) (int, error) {
	t.Helper()
	return doJSONRequest(client, http.MethodDelete, baseURL+"/v1/session/"+id, nil, nil)
}

func updateSession(t *testing.T, client *http.Client, baseURL, id string, req updateSessionRequest) (sessionStateResponse, int, error) {
	t.Helper()
	var resp sessionStateResponse
	status, err := doJSONRequest(client, http.MethodPost, baseURL+"/v1/session/"+id+"/update", req, &resp)
	return resp, status, err
}

func doJSONRequest(client *http.Client, method, url string, body any, dst any) (int, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if dst != nil {
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func rtpPeerListSources(t *testing.T, pcapPath string) ([]rtpPeerSourceStats, error) {
	t.Helper()
	binary := buildRtpPeer(t)
	cmd := exec.Command(binary, "--send-pcap", pcapPath, "--list-sources")
	cmd.Dir = repoRoot(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("rtppeer list-sources: %w\n%s", err, string(output))
	}
	return parseRtpPeerSources(output)
}

func rtpPeerSendPCAP(t *testing.T, cfg rtpPeerSendConfig) error {
	t.Helper()
	binary := buildRtpPeer(t)
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	args := []string{
		"--bind-ip", "127.0.0.1",
		"--audio-port", strconv.Itoa(cfg.AudioPort),
		"--video-port", strconv.Itoa(cfg.VideoPort),
		"--audio-to", cfg.AudioTo,
		"--video-to", cfg.VideoTo,
		"--audio-ssrc", fmt.Sprintf("0x%08x", cfg.AudioSSRC),
		"--video-ssrc", fmt.Sprintf("0x%08x", cfg.VideoSSRC),
		"--send-pcap", cfg.SendPCAP,
	}
	if cfg.RecvPCAP != "" {
		args = append(args, "--recv-pcap", cfg.RecvPCAP)
	}
	if cfg.Pacing != "" {
		args = append(args, "--pacing", cfg.Pacing)
	}
	if cfg.Duration > 0 {
		args = append(args, "--duration", fmt.Sprintf("%d", int(cfg.Duration.Seconds())))
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = repoRoot(t)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("rtppeer send timed out: %w\n%s", ctx.Err(), string(output))
	}
	if err != nil {
		return fmt.Errorf("rtppeer send: %w\n%s", err, string(output))
	}
	return nil
}

func rtpPeerRecvPCAP(t *testing.T, cfg rtpPeerRecvConfig) error {
	t.Helper()
	binary := buildRtpPeer(t)
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	args := []string{
		"--bind-ip", "127.0.0.1",
		"--audio-port", strconv.Itoa(cfg.AudioPort),
		"--video-port", strconv.Itoa(cfg.VideoPort),
		"--recv-pcap", cfg.RecvPCAP,
	}
	if cfg.Duration > 0 {
		args = append(args, "--duration", fmt.Sprintf("%d", int(cfg.Duration.Seconds())))
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = repoRoot(t)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("rtppeer recv timed out: %w\n%s", ctx.Err(), string(output))
	}
	if err != nil {
		return fmt.Errorf("rtppeer recv: %w\n%s", err, string(output))
	}
	return nil
}

func parseRtpPeerSources(output []byte) ([]rtpPeerSourceStats, error) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	var stats []rtpPeerSourceStats
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "ssrc=") {
			continue
		}
		var entry rtpPeerSourceStats
		if _, err := fmt.Sscanf(
			line,
			"ssrc=0x%08x payload_type=%d packets=%d sps=%d pps=%d idr=%d non_idr=%d",
			&entry.SSRC,
			&entry.PayloadType,
			&entry.Packets,
			&entry.SPS,
			&entry.PPS,
			&entry.IDR,
			&entry.NonIDR,
		); err != nil {
			return nil, fmt.Errorf("parse list-sources line %q: %w", line, err)
		}
		stats = append(stats, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

func buildRtpCleaner(t *testing.T) string {
	t.Helper()
	return buildBinary(t, &rtpCleanerBinary, "./cmd/rtp-cleaner", "rtp-cleaner")
}

func buildRtpPeer(t *testing.T) string {
	t.Helper()
	return buildBinary(t, &rtpPeerBinary, "./cmd/rtppeer", "rtppeer")
}

func buildBinary(t *testing.T, cache *binaryCache, pkgPath, binaryName string) string {
	t.Helper()
	cache.once.Do(func() {
		dir, err := os.MkdirTemp("", binaryName+"-bin-")
		if err != nil {
			cache.err = err
			return
		}
		outputPath := filepath.Join(dir, binaryName)
		cmd := exec.Command("go", "build", "-o", outputPath, pkgPath)
		cmd.Dir = repoRoot(t)
		output, err := cmd.CombinedOutput()
		if err != nil {
			cache.err = fmt.Errorf("build %s: %w\n%s", binaryName, err, string(output))
			return
		}
		cache.path = outputPath
	})
	if cache.err != nil {
		t.Fatalf("build %s: %v", binaryName, cache.err)
	}
	if cache.path == "" {
		t.Fatalf("build %s: missing output path", binaryName)
	}
	return cache.path
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found")
		}
		dir = parent
	}
}

func flattenEnv(env map[string]string) []string {
	flat := make([]string, 0, len(env))
	for key, value := range env {
		flat = append(flat, fmt.Sprintf("%s=%s", key, value))
	}
	return flat
}

func stopProcess(t *testing.T, cmd *exec.Cmd, timeout time.Duration) {
	t.Helper()
	if cmd == nil || cmd.Process == nil {
		return
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case <-done:
		return
	case <-time.After(100 * time.Millisecond):
	}
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("rtp-cleaner did not exit within %s", timeout)
	}
}

func assertNotFound(t *testing.T, client *http.Client, baseURL, id string) {
	t.Helper()
	status, err := doJSONRequest(client, http.MethodGet, baseURL+"/v1/session/"+id, nil, &errorResponse{})
	if err != nil {
		t.Fatalf("get session after delete: %v", err)
	}
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", status)
	}
}

func parsePort(value string) (int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, errors.New("empty port")
	}
	port, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, err
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("port out of range: %d", port)
	}
	return port, nil
}
