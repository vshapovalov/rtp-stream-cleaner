package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sourceKey struct {
	ssrc        uint32
	payloadType uint8
}

type sourceStats struct {
	packets int
	sps     int
	pps     int
	idr     int
	nonIDR  int
}

func TestRTPPeerSendReceiveFromPCAP(t *testing.T) {
	rootDir := repoRoot(t)
	binaryPath := filepath.Join(t.TempDir(), "rtppeer")
	buildBinary(t, rootDir, binaryPath)

	audioSSRC := uint32(0xedcc15a7)
	videoSSRC := uint32(0x259989ef)
	expected := runListSources(t, rootDir, binaryPath, filepath.Join(rootDir, "testdata", "normal.pcap"))
	expected = filterSources(expected, map[uint32]struct{}{
		audioSSRC: {},
		videoSSRC: {},
	})
	if len(expected) == 0 {
		t.Fatal("expected list-sources output to be non-empty")
	}

	recvPCAP := filepath.Join(t.TempDir(), "recv_normal.pcap")
	recvAudioPort := freeUDPPort(t)
	recvVideoPort := freeUDPPort(t)
	sendAudioPort := freeUDPPort(t)
	sendVideoPort := freeUDPPort(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	recvArgs := []string{
		"--bind-ip", "127.0.0.1",
		"--audio-port", fmt.Sprint(recvAudioPort),
		"--video-port", fmt.Sprint(recvVideoPort),
		"--recv-pcap", recvPCAP,
		"--duration", "30",
	}
	sendArgs := []string{
		"--bind-ip", "127.0.0.1",
		"--audio-port", fmt.Sprint(sendAudioPort),
		"--video-port", fmt.Sprint(sendVideoPort),
		"--audio-to", fmt.Sprintf("127.0.0.1:%d", recvAudioPort),
		"--video-to", fmt.Sprintf("127.0.0.1:%d", recvVideoPort),
		"--audio-ssrc", fmt.Sprintf("0x%08x", audioSSRC),
		"--video-ssrc", fmt.Sprintf("0x%08x", videoSSRC),
		"--send-pcap", filepath.Join(rootDir, "testdata", "normal.pcap"),
		"--pacing", "capture",
	}

	recvCmd := exec.CommandContext(ctx, binaryPath, recvArgs...)
	recvCmd.Dir = rootDir
	var recvOutput bytes.Buffer
	recvCmd.Stdout = &recvOutput
	recvCmd.Stderr = &recvOutput
	if err := recvCmd.Start(); err != nil {
		t.Fatalf("start receiver: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	sendCmd := exec.CommandContext(ctx, binaryPath, sendArgs...)
	sendCmd.Dir = rootDir
	var sendOutput bytes.Buffer
	sendCmd.Stdout = &sendOutput
	sendCmd.Stderr = &sendOutput
	if err := sendCmd.Start(); err != nil {
		_ = recvCmd.Process.Kill()
		t.Fatalf("start sender: %v", err)
	}

	sendErr := sendCmd.Wait()
	recvErr := recvCmd.Wait()
	if sendErr != nil {
		t.Fatalf("sender failed: %v\n%s", sendErr, sendOutput.String())
	}
	if recvErr != nil {
		t.Fatalf("receiver failed: %v\n%s", recvErr, recvOutput.String())
	}

	actual := runListSources(t, rootDir, binaryPath, recvPCAP)
	assertSourceCounts(t, expected, actual)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

func buildBinary(t *testing.T, rootDir, outputPath string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", outputPath, "./cmd/rtppeer")
	cmd.Dir = rootDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build rtppeer: %v\n%s", err, string(output))
	}
}

func runListSources(t *testing.T, rootDir, binaryPath, pcapPath string) map[sourceKey]sourceStats {
	t.Helper()
	cmd := exec.Command(binaryPath, "--send-pcap", pcapPath, "--list-sources")
	cmd.Dir = rootDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list-sources failed: %v\n%s", err, string(output))
	}
	return parseSources(t, string(output))
}

func parseSources(t *testing.T, output string) map[sourceKey]sourceStats {
	t.Helper()
	output = strings.TrimSpace(output)
	if output == "" {
		return map[sourceKey]sourceStats{}
	}

	result := make(map[sourceKey]sourceStats)
	for _, line := range strings.Split(output, "\n") {
		var ssrc uint32
		var payloadType int
		var packets int
		var sps int
		var pps int
		var idr int
		var nonIDR int
		_, err := fmt.Sscanf(
			line,
			"ssrc=0x%08x payload_type=%d packets=%d sps=%d pps=%d idr=%d non_idr=%d",
			&ssrc,
			&payloadType,
			&packets,
			&sps,
			&pps,
			&idr,
			&nonIDR,
		)
		if err != nil {
			t.Fatalf("parse list-sources line %q: %v", line, err)
		}
		result[sourceKey{ssrc: ssrc, payloadType: uint8(payloadType)}] = sourceStats{
			packets: packets,
			sps:     sps,
			pps:     pps,
			idr:     idr,
			nonIDR:  nonIDR,
		}
	}
	return result
}

func assertSourceCounts(t *testing.T, expected, actual map[sourceKey]sourceStats) {
	t.Helper()
	if len(expected) != len(actual) {
		t.Fatalf("source count mismatch: expected %d entries, got %d", len(expected), len(actual))
	}
	for key, expectedStats := range expected {
		actualStats, ok := actual[key]
		if !ok {
			t.Fatalf("missing source ssrc=0x%08x payload_type=%d", key.ssrc, key.payloadType)
		}
		if actualStats != expectedStats {
			t.Fatalf(
				"source stats mismatch for ssrc=0x%08x payload_type=%d: expected %+v, got %+v",
				key.ssrc,
				key.payloadType,
				expectedStats,
				actualStats,
			)
		}
	}
}

func filterSources(sourceCounts map[sourceKey]sourceStats, allowedSSRCs map[uint32]struct{}) map[sourceKey]sourceStats {
	filtered := make(map[sourceKey]sourceStats)
	for key, stats := range sourceCounts {
		if _, ok := allowedSSRCs[key.ssrc]; ok {
			filtered[key] = stats
		}
	}
	return filtered
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}
