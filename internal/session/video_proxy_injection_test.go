package session

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"

	"rtp-stream-cleaner/internal/rtpfix"
)

func TestVideoProxyInjectCachedSPSPPSOnIDR(t *testing.T) {
	// We cannot reliably test VIDEO_INJECT_CACHED_SPSPPS with existing PCAPs
	// because problem.pcap already includes SPS=6/PPS=6/IDR=6, so cached
	// parameter sets are already present before IDRs and injection is not
	// required (and therefore not deterministic). Injection specifically
	// triggers when we have cached SPS/PPS and see an IDR frame start without
	// any pending SPS/PPS to prepend to that frame, so the cached values must
	// be injected immediately before the IDR. To model that deterministically,
	// we synthesize minimal, valid RTP/H264 packets: a single-byte SPS NAL (type
	// 7), a single-byte PPS NAL (type 8), and a single-byte IDR NAL (type 5).
	// We cache the SPS/PPS payloads from those packets, then feed only the IDR
	// packet through the fix logic so it represents an IDR with no preceding
	// SPS/PPS in the frame. We assert that output order is SPS then PPS then the
	// original IDR payload, and that injected counters and seq-delta reflect
	// exactly one SPS and one PPS insertion.
	session := &Session{ID: "S-inject"}
	proxy := &videoProxy{
		session:            session,
		fixEnabled:         true,
		injectCachedSPSPPS: true,
	}
	var output [][]byte
	proxy.writeToDest = func(packet []byte, dest *net.UDPAddr) error {
		clone := make([]byte, len(packet))
		copy(clone, packet)
		output = append(output, clone)
		return nil
	}

	dest := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}
	spsPacket := makeRTPPacket(10, 9000, []byte{0x67})
	ppsPacket := makeRTPPacket(11, 9000, []byte{0x68})
	idrPacket := makeRTPPacket(12, 9000, []byte{0x65})

	spsInfo, ok := parseH264Packet(spsPacket)
	if !ok || !spsInfo.info.IsSPS {
		t.Fatalf("expected SPS packet to parse")
	}
	ppsInfo, ok := parseH264Packet(ppsPacket)
	if !ok || !ppsInfo.info.IsPPS {
		t.Fatalf("expected PPS packet to parse")
	}
	proxy.cacheParameterSet(spsInfo.payload, true)
	proxy.cacheParameterSet(ppsInfo.payload, false)

	proxy.handleVideoPacket(idrPacket, dest)

	if len(output) != 3 {
		t.Fatalf("expected 3 output packets, got %d", len(output))
	}
	if !bytes.Equal(output[0][12:], spsInfo.payload) {
		t.Fatalf("unexpected SPS payload: got=%v want=%v", output[0][12:], spsInfo.payload)
	}
	if !bytes.Equal(output[1][12:], ppsInfo.payload) {
		t.Fatalf("unexpected PPS payload: got=%v want=%v", output[1][12:], ppsInfo.payload)
	}
	if !bytes.Equal(output[2][12:], idrPacket[12:]) {
		t.Fatalf("unexpected IDR payload: got=%v want=%v", output[2][12:], idrPacket[12:])
	}

	firstSeq := binary.BigEndian.Uint16(output[0][2:4])
	secondSeq := binary.BigEndian.Uint16(output[1][2:4])
	thirdSeq := binary.BigEndian.Uint16(output[2][2:4])
	if firstSeq+1 != secondSeq || secondSeq+1 != thirdSeq {
		t.Fatalf("unexpected seq order: got=%d,%d,%d", firstSeq, secondSeq, thirdSeq)
	}
	idrPayload := output[2][12:]
	idrInfo, ok := rtpfix.ParseH264(idrPayload)
	if !ok || !idrInfo.IsIDR {
		t.Fatalf("expected IDR payload in final packet")
	}

	counters := snapshotVideoCounters(&session.videoCounters)
	if counters.VideoInjectedSPS != 1 || counters.VideoInjectedPPS != 1 {
		t.Fatalf("unexpected injected counts: sps=%d pps=%d", counters.VideoInjectedSPS, counters.VideoInjectedPPS)
	}
	if counters.VideoSeqDelta != 2 {
		t.Fatalf("unexpected seq delta: got=%d want=2", counters.VideoSeqDelta)
	}
}
