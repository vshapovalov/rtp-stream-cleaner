package integration_test

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtp-stream-cleaner/internal/pcapio"
	"rtp-stream-cleaner/internal/rtpfix"
)

type videoFixRun struct {
	id         string
	baseURL    string
	client     *http.Client
	recvPCAP   string
	finalState sessionStateResponse
}

const maxVideoFixPacketsRaw = 400

type videoFixOptions struct {
	pacing       string
	recvDuration time.Duration
	recvTimeout  time.Duration
	sendTimeout  time.Duration
	waitTimeout  time.Duration
}

func defaultVideoFixOptions() videoFixOptions {
	return videoFixOptions{
		recvDuration: 8 * time.Second,
		recvTimeout:  12 * time.Second,
		sendTimeout:  12 * time.Second,
		waitTimeout:  8 * time.Second,
	}
}

func videoFixEnv() map[string]string {
	env := baseEnv("10")
	env["PUBLIC_IP"] = "127.0.0.1"
	env["INTERNAL_IP"] = "127.0.0.1"
	env["PEER_LEARNING_WINDOW_SEC"] = "1"
	env["MAX_FRAME_WAIT_MS"] = "150"
	env["IDLE_TIMEOUT_SEC"] = "10"
	env["RTP_PORT_MIN"] = "35000"
	env["RTP_PORT_MAX"] = "35050"
	return env
}

func boolPtr(value bool) *bool {
	return &value
}

func findSourceStats(t *testing.T, stats []rtpPeerSourceStats, ssrc uint32) rtpPeerSourceStats {
	t.Helper()
	for _, entry := range stats {
		if entry.SSRC == ssrc {
			return entry
		}
	}
	t.Fatalf("expected ssrc 0x%08x in list-sources output, got %+v", ssrc, stats)
	return rtpPeerSourceStats{}
}

func trimPCAP(t *testing.T, sourcePath string, maxPackets int) string {
	t.Helper()
	reader, err := pcapio.OpenReader(sourcePath)
	if err != nil {
		t.Fatalf("open pcap reader: %v", err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Fatalf("close pcap reader: %v", closeErr)
		}
	}()

	destPath := filepath.Join(t.TempDir(), "trimmed.pcap")
	file, err := os.Create(destPath)
	if err != nil {
		t.Fatalf("create trimmed pcap: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close trimmed pcap: %v", closeErr)
		}
	}()

	linkType := reader.LinkType()
	if linkType == 0 {
		linkType = 1
	}
	header := make([]byte, 24)
	binary.LittleEndian.PutUint32(header[0:4], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(header[4:6], 2)
	binary.LittleEndian.PutUint16(header[6:8], 4)
	binary.LittleEndian.PutUint32(header[8:12], 0)
	binary.LittleEndian.PutUint32(header[12:16], 0)
	binary.LittleEndian.PutUint32(header[16:20], 65535)
	binary.LittleEndian.PutUint32(header[20:24], linkType)
	if _, err := file.Write(header); err != nil {
		t.Fatalf("write trimmed pcap header: %v", err)
	}

	for i := 0; i < maxPackets; i++ {
		packet, err := reader.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("read pcap packet: %v", err)
		}
		recordHeader := make([]byte, 16)
		binary.LittleEndian.PutUint32(recordHeader[0:4], uint32(packet.Timestamp.Unix()))
		binary.LittleEndian.PutUint32(recordHeader[4:8], uint32(packet.Timestamp.Nanosecond()/1000))
		binary.LittleEndian.PutUint32(recordHeader[8:12], uint32(len(packet.Data)))
		binary.LittleEndian.PutUint32(recordHeader[12:16], uint32(len(packet.Data)))
		if _, err := file.Write(recordHeader); err != nil {
			t.Fatalf("write trimmed pcap record header: %v", err)
		}
		if _, err := file.Write(packet.Data); err != nil {
			t.Fatalf("write trimmed pcap record data: %v", err)
		}
	}

	return destPath
}

func trimPCAPWithGap(t *testing.T, sourcePath string, maxPackets int, gap time.Duration, videoSSRC uint32) string {
	t.Helper()
	reader, err := pcapio.OpenReader(sourcePath)
	if err != nil {
		t.Fatalf("open pcap reader: %v", err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Fatalf("close pcap reader: %v", closeErr)
		}
	}()

	destPath := filepath.Join(t.TempDir(), "trimmed_with_gap.pcap")
	file, err := os.Create(destPath)
	if err != nil {
		t.Fatalf("create trimmed pcap: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close trimmed pcap: %v", closeErr)
		}
	}()

	linkType := reader.LinkType()
	if linkType == 0 {
		linkType = 1
	}
	header := make([]byte, 24)
	binary.LittleEndian.PutUint32(header[0:4], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(header[4:6], 2)
	binary.LittleEndian.PutUint16(header[6:8], 4)
	binary.LittleEndian.PutUint32(header[8:12], 0)
	binary.LittleEndian.PutUint32(header[12:16], 0)
	binary.LittleEndian.PutUint32(header[16:20], 65535)
	binary.LittleEndian.PutUint32(header[20:24], linkType)
	if _, err := file.Write(header); err != nil {
		t.Fatalf("write trimmed pcap header: %v", err)
	}

	var gapInserted bool
	var extraDelay time.Duration
	for i := 0; i < maxPackets; i++ {
		packet, err := reader.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("read pcap packet: %v", err)
		}
		ts := packet.Timestamp.Add(extraDelay)
		recordHeader := make([]byte, 16)
		binary.LittleEndian.PutUint32(recordHeader[0:4], uint32(ts.Unix()))
		binary.LittleEndian.PutUint32(recordHeader[4:8], uint32(ts.Nanosecond()/1000))
		binary.LittleEndian.PutUint32(recordHeader[8:12], uint32(len(packet.Data)))
		binary.LittleEndian.PutUint32(recordHeader[12:16], uint32(len(packet.Data)))
		if _, err := file.Write(recordHeader); err != nil {
			t.Fatalf("write trimmed pcap record header: %v", err)
		}
		if _, err := file.Write(packet.Data); err != nil {
			t.Fatalf("write trimmed pcap record data: %v", err)
		}
		if !gapInserted {
			start, end := frameStartEndForSSRC(packet.Data, videoSSRC, linkType)
			if start && !end {
				extraDelay = gap
				gapInserted = true
			}
		}
	}

	if !gapInserted {
		t.Fatalf("no suitable frame start found to insert gap")
	}

	return destPath
}

func frameStartEndForSSRC(packet []byte, ssrc uint32, linkType uint32) (bool, bool) {
	payload, ok := rtpPayloadFromFrame(packet, linkType)
	if !ok {
		return false, false
	}
	header, ok := rtpfix.ParseRTPHeader(payload)
	if !ok {
		return false, false
	}
	if header.SSRC != ssrc {
		return false, false
	}
	if header.HeaderLen >= len(payload) {
		return false, false
	}
	info, ok := rtpfix.ParseH264(payload[header.HeaderLen:])
	if !ok || !info.IsSlice {
		return false, false
	}
	return rtpfix.IsFrameStart(info), rtpfix.IsFrameEnd(info)
}

func rtpPayloadFromFrame(packet []byte, linkType uint32) ([]byte, bool) {
	var ipOffset int
	switch linkType {
	case 1:
		if len(packet) < 14+20+8 {
			return nil, false
		}
		ethType := binary.BigEndian.Uint16(packet[12:14])
		if ethType != 0x0800 {
			return nil, false
		}
		ipOffset = 14
	case 113:
		if len(packet) < 16+20+8 {
			return nil, false
		}
		proto := binary.BigEndian.Uint16(packet[14:16])
		if proto != 0x0800 {
			return nil, false
		}
		ipOffset = 16
	default:
		return nil, false
	}
	ipHeader := packet[ipOffset:]
	if len(ipHeader) < 20 {
		return nil, false
	}
	ihl := int(ipHeader[0]&0x0f) * 4
	if len(ipHeader) < ihl+8 {
		return nil, false
	}
	if ipHeader[9] != 17 {
		return nil, false
	}
	udpStart := ipOffset + ihl
	payloadStart := udpStart + 8
	if payloadStart > len(packet) {
		return nil, false
	}
	return packet[payloadStart:], true
}

func runVideoFixScenario(
	t *testing.T,
	fixEnabled bool,
	pcapPath string,
	audioSSRC uint32,
	videoSSRC uint32,
	opts videoFixOptions,
	waitCond func(sessionStateResponse) bool,
) videoFixRun {
	t.Helper()
	instance, cleanup := startRtpCleaner(t, videoFixEnv())
	t.Cleanup(cleanup)

	client := &http.Client{Timeout: 2 * time.Second}
	if err := waitForHealth(instance.BaseURL, 2*time.Second); err != nil {
		t.Fatalf("health check failed: %v", err)
	}

	var createReq createSessionRequest
	createReq.CallID = fmt.Sprintf("call-video-fix-%t", fixEnabled)
	createReq.FromTag = "from-video-fix"
	createReq.ToTag = "to-video-fix"
	createReq.Audio.Enable = true
	createReq.Video.Enable = true
	createReq.Video.Fix = boolPtr(fixEnabled)
	createResp, err := createSession(t, client, instance.BaseURL, createReq)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	recvAudioPort := freeUDPPort(t)
	recvVideoPort := freeUDPPort(t)
	recvPCAP := filepath.Join(t.TempDir(), "recv.pcap")
	recvErr := make(chan error, 1)
	go func() {
		recvErr <- rtpPeerRecvPCAP(t, rtpPeerRecvConfig{
			AudioPort: recvAudioPort,
			VideoPort: recvVideoPort,
			RecvPCAP:  recvPCAP,
			Duration:  opts.recvDuration,
			Timeout:   opts.recvTimeout,
		})
	}()

	audioDest := fmt.Sprintf("127.0.0.1:%d", recvAudioPort)
	videoDest := fmt.Sprintf("127.0.0.1:%d", recvVideoPort)
	_, status, err := updateSession(t, client, instance.BaseURL, createResp.ID, updateSessionRequest{
		Audio: &updateMediaRequest{RTPEngineDest: &audioDest},
		Video: &updateMediaRequest{RTPEngineDest: &videoDest},
	})
	if err != nil {
		t.Fatalf("update session: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("update session: expected 200, got %d", status)
	}

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- rtpPeerSendPCAP(t, rtpPeerSendConfig{
			AudioPort: freeUDPPort(t),
			VideoPort: freeUDPPort(t),
			AudioTo:   fmt.Sprintf("127.0.0.1:%d", createResp.Audio.APort),
			VideoTo:   fmt.Sprintf("127.0.0.1:%d", createResp.Video.APort),
			AudioSSRC: audioSSRC,
			VideoSSRC: videoSSRC,
			SendPCAP:  pcapPath,
			Pacing:    opts.pacing,
			Timeout:   opts.sendTimeout,
		})
	}()

	if _, err := waitForSessionCondition(t, client, instance.BaseURL, createResp.ID, opts.waitTimeout, waitCond); err != nil {
		t.Fatalf("wait for session condition: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("rtppeer send: %v", err)
	}

	finalState, status, err := getSession(t, client, instance.BaseURL, createResp.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("get session: expected 200, got %d", status)
	}

	if err := <-recvErr; err != nil {
		t.Fatalf("rtppeer recv: %v", err)
	}

	return videoFixRun{
		id:         createResp.ID,
		baseURL:    instance.BaseURL,
		client:     client,
		recvPCAP:   recvPCAP,
		finalState: finalState,
	}
}

// TestIntegrationD1VideoFixRawNormal ensures raw mode (video.fix=false) is a
// transparent proxy for a healthy stream. Raw mode means rtp-cleaner performs
// no frame-boundary analysis and therefore does not flush frames, inject SPS/PPS,
// or compute any sequence delta; fix mode (video.fix=true) is the opposite and
// enables frame-boundary analysis plus flush logic. The normal.pcap stream has
// clean frame boundaries, so even when fix mode is enabled it should not trigger
// forced flushes, whereas problem.pcap is known to have problematic boundaries
// that should drive forced flushes when fix is enabled. The authoritative signals
// for this are the GET /v1/session/{id} counters: video_frames_started,
// video_frames_ended, video_frames_flushed, video_forced_flushes,
// video_injected_sps, video_injected_pps, and video_seq_delta_current; in raw
// mode they must stay at zero to confirm no analysis or mutation. We also use
// rtppeer --list-sources on the input and output PCAPs to assert “no mutation”
// in raw mode by requiring the output video packet count to match the input and
// the SPS/PPS/IDR/Non-IDR breakdown to be unchanged. Flake control: we poll
// session counters with a deadline, keep rtppeer send/recv durations bounded,
// assume localhost-only ports, and avoid unbounded sleeps.
func TestIntegrationD1VideoFixRawNormal(t *testing.T) {
	pcapPath := filepath.Join(repoRoot(t), "testdata", "normal.pcap")
	trimmedPCAP := trimPCAP(t, pcapPath, maxVideoFixPacketsRaw)
	inputStats, err := rtpPeerListSources(t, trimmedPCAP)
	if err != nil {
		t.Fatalf("list sources input: %v", err)
	}
	inputVideo := findSourceStats(t, inputStats, normalVideoSSRC)
	if inputVideo.Packets == 0 {
		t.Fatalf("expected input video packets, got 0")
	}

	run := runVideoFixScenario(t, false, trimmedPCAP, normalAudioSSRC, normalVideoSSRC, defaultVideoFixOptions(), func(resp sessionStateResponse) bool {
		return resp.VideoAInPkts > 0 && resp.VideoBOutPkts > 0
	})

	if run.finalState.VideoAInPkts == 0 || run.finalState.VideoBOutPkts == 0 {
		t.Fatalf("expected video packets to flow, got %+v", run.finalState)
	}
	if run.finalState.VideoFramesStarted != 0 || run.finalState.VideoFramesEnded != 0 || run.finalState.VideoFramesFlushed != 0 {
		t.Fatalf("expected raw mode to keep frame counters at zero, got %+v", run.finalState)
	}
	if run.finalState.VideoForcedFlushes != 0 {
		t.Fatalf("expected raw mode to keep forced flushes at zero, got %d", run.finalState.VideoForcedFlushes)
	}
	if run.finalState.VideoInjectedSPS != 0 || run.finalState.VideoInjectedPPS != 0 {
		t.Fatalf("expected raw mode to avoid SPS/PPS injection, got %+v", run.finalState)
	}
	if run.finalState.VideoSeqDeltaCurrent != 0 {
		t.Fatalf("expected raw mode to keep seq delta at zero, got %d", run.finalState.VideoSeqDeltaCurrent)
	}

	outputStats, err := rtpPeerListSources(t, run.recvPCAP)
	if err != nil {
		t.Fatalf("list sources output: %v", err)
	}
	outputVideo := findSourceStats(t, outputStats, normalVideoSSRC)
	if outputVideo.Packets != inputVideo.Packets {
		t.Fatalf("expected output video packets to match input: %d != %d", outputVideo.Packets, inputVideo.Packets)
	}
	if outputVideo.SPS != inputVideo.SPS || outputVideo.PPS != inputVideo.PPS || outputVideo.IDR != inputVideo.IDR || outputVideo.NonIDR != inputVideo.NonIDR {
		t.Fatalf("expected output SPS/PPS/IDR/Non-IDR to match input, got input=%+v output=%+v", inputVideo, outputVideo)
	}
}

// TestIntegrationD2VideoFixRawProblem ensures raw mode (video.fix=false) is a
// transparent proxy for a problematic stream. Raw mode means rtp-cleaner performs
// no frame-boundary analysis and therefore does not flush frames, inject SPS/PPS,
// or compute any sequence delta; fix mode (video.fix=true) is the opposite and
// enables frame-boundary analysis plus flush logic. The normal.pcap stream has
// clean frame boundaries, so even when fix mode is enabled it should not trigger
// forced flushes, whereas problem.pcap is known to have problematic boundaries
// that should drive forced flushes when fix is enabled. The authoritative signals
// for this are the GET /v1/session/{id} counters: video_frames_started,
// video_frames_ended, video_frames_flushed, video_forced_flushes,
// video_injected_sps, video_injected_pps, and video_seq_delta_current; in raw
// mode they must stay at zero to confirm no analysis or mutation. We also use
// rtppeer --list-sources on the input and output PCAPs to assert “no mutation”
// in raw mode by requiring the output video packet count to match the input and
// the SPS/PPS/IDR/Non-IDR breakdown to be unchanged. Flake control: we poll
// session counters with a deadline, keep rtppeer send/recv durations bounded,
// assume localhost-only ports, and avoid unbounded sleeps.
func TestIntegrationD2VideoFixRawProblem(t *testing.T) {
	pcapPath := filepath.Join(repoRoot(t), "testdata", "problem.pcap")
	trimmedPCAP := trimPCAP(t, pcapPath, maxVideoFixPacketsRaw)
	inputStats, err := rtpPeerListSources(t, trimmedPCAP)
	if err != nil {
		t.Fatalf("list sources input: %v", err)
	}
	inputVideo := findSourceStats(t, inputStats, problemVideoSSRC)
	if inputVideo.Packets == 0 {
		t.Fatalf("expected input video packets, got 0")
	}

	run := runVideoFixScenario(t, false, trimmedPCAP, problemAudioSSRC, problemVideoSSRC, defaultVideoFixOptions(), func(resp sessionStateResponse) bool {
		return resp.VideoAInPkts > 0 && resp.VideoBOutPkts > 0
	})

	if run.finalState.VideoAInPkts == 0 || run.finalState.VideoBOutPkts == 0 {
		t.Fatalf("expected video packets to flow, got %+v", run.finalState)
	}
	if run.finalState.VideoFramesStarted != 0 || run.finalState.VideoFramesEnded != 0 || run.finalState.VideoFramesFlushed != 0 {
		t.Fatalf("expected raw mode to keep frame counters at zero, got %+v", run.finalState)
	}
	if run.finalState.VideoForcedFlushes != 0 {
		t.Fatalf("expected raw mode to keep forced flushes at zero, got %d", run.finalState.VideoForcedFlushes)
	}
	if run.finalState.VideoInjectedSPS != 0 || run.finalState.VideoInjectedPPS != 0 {
		t.Fatalf("expected raw mode to avoid SPS/PPS injection, got %+v", run.finalState)
	}
	if run.finalState.VideoSeqDeltaCurrent != 0 {
		t.Fatalf("expected raw mode to keep seq delta at zero, got %d", run.finalState.VideoSeqDeltaCurrent)
	}

	outputStats, err := rtpPeerListSources(t, run.recvPCAP)
	if err != nil {
		t.Fatalf("list sources output: %v", err)
	}
	outputVideo := findSourceStats(t, outputStats, problemVideoSSRC)
	if outputVideo.Packets != inputVideo.Packets {
		t.Fatalf("expected output video packets to match input: %d != %d", outputVideo.Packets, inputVideo.Packets)
	}
	if outputVideo.SPS != inputVideo.SPS || outputVideo.PPS != inputVideo.PPS || outputVideo.IDR != inputVideo.IDR || outputVideo.NonIDR != inputVideo.NonIDR {
		t.Fatalf("expected output SPS/PPS/IDR/Non-IDR to match input, got input=%+v output=%+v", inputVideo, outputVideo)
	}
}

// TestIntegrationE1VideoFixNormal ensures fix mode (video.fix=true) performs
// frame-boundary analysis and flush logic on a healthy stream. Raw mode means
// no frame analysis, flush, SPS/PPS injection, or seq-delta tracking; fix mode
// enables those behaviors. The normal.pcap stream has clean frame boundaries, so
// even with fix enabled it should not require forced flushes, whereas problem.pcap
// is known to have problematic boundaries that should trigger forced flushes when
// fix is enabled. The authoritative signals are the GET /v1/session/{id} counters
// video_frames_started, video_frames_ended, video_frames_flushed,
// video_forced_flushes, video_injected_sps, video_injected_pps, and
// video_seq_delta_current, which we poll until frame metrics are non-zero and
// then assert forced flushes remain zero. In raw-mode tests we also use
// rtppeer --list-sources to assert no mutation by comparing output and input
// packet/SPS/PPS/IDR/Non-IDR counts. Flake control: bounded rtppeer send/recv
// durations, deadline-based polling (no unbounded sleeps), and localhost-only
// ports keep this deterministic.
func TestIntegrationE1VideoFixNormal(t *testing.T) {
	pcapPath := filepath.Join(repoRoot(t), "testdata", "normal.pcap")
	trimmedPCAP := trimPCAP(t, pcapPath, maxVideoFixPacketsRaw)
	run := runVideoFixScenario(t, true, trimmedPCAP, normalAudioSSRC, normalVideoSSRC, defaultVideoFixOptions(), func(resp sessionStateResponse) bool {
		return resp.VideoAInPkts > 0 && resp.VideoBOutPkts > 0 && resp.VideoFramesStarted > 0 && resp.VideoFramesEnded > 0 && resp.VideoFramesFlushed > 0
	})

	if run.finalState.VideoFramesStarted < 1 || run.finalState.VideoFramesEnded < 1 || run.finalState.VideoFramesFlushed < 1 {
		t.Fatalf("expected frame metrics to be non-zero, got %+v", run.finalState)
	}
	if run.finalState.VideoForcedFlushes != 0 {
		t.Fatalf("expected no forced flushes for normal stream, got %d", run.finalState.VideoForcedFlushes)
	}
}

// TestIntegrationE2VideoFixProblem ensures fix mode (video.fix=true) detects
// problematic frame boundaries. Raw mode means no frame analysis, flush,
// SPS/PPS injection, or seq-delta tracking; fix mode enables those behaviors.
// The problem.pcap stream has known boundary issues, so when fix is enabled we
// expect forced flushes to occur, while normal.pcap should not trigger them. The
// authoritative signals are the GET /v1/session/{id} counters video_frames_started,
// video_frames_ended, video_frames_flushed, video_forced_flushes,
// video_injected_sps, video_injected_pps, and video_seq_delta_current. In raw
// mode tests we also use rtppeer --list-sources to assert no mutation by comparing
// output and input packet/SPS/PPS/IDR/Non-IDR counts. Flake control: bounded
// rtppeer send/recv durations, deadline-based polling, and localhost-only ports
// avoid unbounded sleeps and reduce timing variance.
func TestIntegrationE2VideoFixProblem(t *testing.T) {
	pcapPath := filepath.Join(repoRoot(t), "testdata", "problem.pcap")
	trimmedPCAP := trimPCAPWithGap(t, pcapPath, maxVideoFixPacketsRaw, 200*time.Millisecond, problemVideoSSRC)
	run := runVideoFixScenario(t, true, trimmedPCAP, problemAudioSSRC, problemVideoSSRC, defaultVideoFixOptions(), func(resp sessionStateResponse) bool {
		return resp.VideoAInPkts > 0 && resp.VideoBOutPkts > 0 && resp.VideoFramesFlushed > 0
	})

	finalState := run.finalState
	if finalState.VideoForcedFlushes < 1 {
		updated, err := waitForSessionCondition(t, run.client, run.baseURL, run.id, 10*time.Second, func(resp sessionStateResponse) bool {
			return resp.VideoForcedFlushes > 0
		})
		if err != nil {
			t.Fatalf("wait for forced flushes: %v", err)
		}
		finalState = updated
	}
	if finalState.VideoFramesFlushed < 1 {
		t.Fatalf("expected frames flushed to be non-zero, got %+v", finalState)
	}
	if finalState.VideoForcedFlushes < 1 {
		t.Fatalf("expected forced flushes for problem stream, got %d", finalState.VideoForcedFlushes)
	}
	if finalState.VideoFramesFlushed <= finalState.VideoFramesEnded {
		t.Logf("frames flushed (%d) did not exceed frames ended (%d), which is acceptable but unexpected", finalState.VideoFramesFlushed, finalState.VideoFramesEnded)
	}
}
