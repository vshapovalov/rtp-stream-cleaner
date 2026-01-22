package rtpfix

import "testing"

func buildRTPPacket(marker bool, payloadType uint8, seq uint16, ts uint32, ssrc uint32, payload []byte) []byte {
	packet := make([]byte, 12+len(payload))
	packet[0] = 0x80
	if marker {
		packet[1] = 0x80 | (payloadType & 0x7f)
	} else {
		packet[1] = payloadType & 0x7f
	}
	packet[2] = byte(seq >> 8)
	packet[3] = byte(seq)
	packet[4] = byte(ts >> 24)
	packet[5] = byte(ts >> 16)
	packet[6] = byte(ts >> 8)
	packet[7] = byte(ts)
	packet[8] = byte(ssrc >> 24)
	packet[9] = byte(ssrc >> 16)
	packet[10] = byte(ssrc >> 8)
	packet[11] = byte(ssrc)
	copy(packet[12:], payload)
	return packet
}

// TestH264NALTypeParsing_SPS_PPS_IDR_NonIDR validates the H264 NAL unit type
// decoding rule that rtp-cleaner uses to identify parameter sets and slice
// frames for buffering and injection. Each synthetic payload is a one-byte NAL
// header with the target type: SPS (7), PPS (8), IDR slice (5), and non-IDR
// slice (1). The expected outputs are deterministic because ParseH264 only
// inspects the low 5 bits of the first byte, so no start codes or extra data
// are needed. The test guards against misclassification that would either skip
// needed SPS/PPS caching or mis-handle slice frames during frame assembly.
func TestH264NALTypeParsing_SPS_PPS_IDR_NonIDR(t *testing.T) {
	cases := []struct {
		name      string
		payload   []byte
		wantType  uint8
		wantSPS   bool
		wantPPS   bool
		wantIDR   bool
		wantSlice bool
	}{
		{
			name:      "sps",
			payload:   []byte{0x67},
			wantType:  7,
			wantSPS:   true,
			wantSlice: false,
		},
		{
			name:      "pps",
			payload:   []byte{0x68},
			wantType:  8,
			wantPPS:   true,
			wantSlice: false,
		},
		{
			name:      "idr",
			payload:   []byte{0x65},
			wantType:  5,
			wantIDR:   true,
			wantSlice: true,
		},
		{
			name:      "non-idr",
			payload:   []byte{0x61},
			wantType:  1,
			wantSlice: true,
		},
	}

	for _, tc := range cases {
		info, ok := ParseH264(tc.payload)
		if !ok {
			t.Fatalf("expected %s payload to parse", tc.name)
		}
		if info.NALType != tc.wantType {
			t.Fatalf("%s: unexpected NAL type: got=%d want=%d", tc.name, info.NALType, tc.wantType)
		}
		if info.IsSPS != tc.wantSPS {
			t.Fatalf("%s: unexpected SPS flag: got=%v want=%v", tc.name, info.IsSPS, tc.wantSPS)
		}
		if info.IsPPS != tc.wantPPS {
			t.Fatalf("%s: unexpected PPS flag: got=%v want=%v", tc.name, info.IsPPS, tc.wantPPS)
		}
		if info.IsIDR != tc.wantIDR {
			t.Fatalf("%s: unexpected IDR flag: got=%v want=%v", tc.name, info.IsIDR, tc.wantIDR)
		}
		if info.IsSlice != tc.wantSlice {
			t.Fatalf("%s: unexpected slice flag: got=%v want=%v", tc.name, info.IsSlice, tc.wantSlice)
		}
	}
}

// TestFrameBoundaryDetection_ByMarkerOrTimestamp demonstrates the frame boundary
// rules used by the current H264 parser: slice boundaries are detected only by
// NAL unit structure (single NAL or FU-A start/end), not by RTP marker bits or
// timestamp deltas. The synthetic payloads are minimal FU-A fragments carrying
// a slice type of 5 (IDR): a start fragment (S=1,E=0), a middle fragment
// (S=0,E=0), and an end fragment (S=0,E=1). Each is wrapped in RTP headers with
// deliberately different marker flags and timestamps to show those header fields
// do not influence boundary detection. The expected outputs are deterministic:
// IsFrameStart is true only for the FU start fragment, IsFrameEnd is true only
// for the FU end fragment, and both are true for a single-NAL slice. This guards
// against future changes that accidentally tie frame boundaries to marker or
// timestamp rather than H264 fragmentation rules.
func TestFrameBoundaryDetection_ByMarkerOrTimestamp(t *testing.T) {
	fuIndicator := byte(28) | 0x60
	fuStart := []byte{fuIndicator, 0x80 | 0x05}
	fuMiddle := []byte{fuIndicator, 0x05}
	fuEnd := []byte{fuIndicator, 0x40 | 0x05}

	startPacket := buildRTPPacket(true, 96, 1, 1000, 0x01020304, fuStart)
	middlePacket := buildRTPPacket(false, 96, 2, 1001, 0x01020304, fuMiddle)
	endPacket := buildRTPPacket(true, 96, 3, 2000, 0x01020304, fuEnd)

	startInfo, ok := ParseH264(startPacket[12:])
	if !ok {
		t.Fatalf("expected FU start payload to parse")
	}
	if !IsFrameStart(startInfo) || IsFrameEnd(startInfo) {
		t.Fatalf("unexpected FU start boundaries: start=%v end=%v", IsFrameStart(startInfo), IsFrameEnd(startInfo))
	}

	middleInfo, ok := ParseH264(middlePacket[12:])
	if !ok {
		t.Fatalf("expected FU middle payload to parse")
	}
	if IsFrameStart(middleInfo) || IsFrameEnd(middleInfo) {
		t.Fatalf("unexpected FU middle boundaries: start=%v end=%v", IsFrameStart(middleInfo), IsFrameEnd(middleInfo))
	}

	endInfo, ok := ParseH264(endPacket[12:])
	if !ok {
		t.Fatalf("expected FU end payload to parse")
	}
	if IsFrameStart(endInfo) || !IsFrameEnd(endInfo) {
		t.Fatalf("unexpected FU end boundaries: start=%v end=%v", IsFrameStart(endInfo), IsFrameEnd(endInfo))
	}

	singleNAL := []byte{0x65}
	singleInfo, ok := ParseH264(singleNAL)
	if !ok {
		t.Fatalf("expected single NAL payload to parse")
	}
	if !IsFrameStart(singleInfo) || !IsFrameEnd(singleInfo) {
		t.Fatalf("unexpected single NAL boundaries: start=%v end=%v", IsFrameStart(singleInfo), IsFrameEnd(singleInfo))
	}
}
