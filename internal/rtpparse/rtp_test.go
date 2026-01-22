package rtpparse

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

// TestRTPParse_ValidMinimalHeader validates the RTP v2 fixed header parsing that
// rtp-cleaner relies on for identifying streams. The rule under test is that a
// minimal 12-byte RTP header (no CSRCs, no extensions) must expose marker bit,
// payload type, sequence number, timestamp, and SSRC deterministically. The
// synthetic packet is constructed byte-by-byte with version=2, marker=true,
// payload type=96, sequence=0x1234, timestamp=0x01020304, and SSRC=0x0a0b0c0d
// followed by a 3-byte payload. The expected outputs are deterministic because
// Parse reads fixed offsets in the header regardless of payload content; the
// test guards against regressions that would shift header offsets or drop marker
// or timing fields needed for later frame logic.
func TestRTPParse_ValidMinimalHeader(t *testing.T) {
	payload := []byte{0xaa, 0xbb, 0xcc}
	packet := buildRTPPacket(true, 96, 0x1234, 0x01020304, 0x0a0b0c0d, payload)

	parsed, err := Parse(packet)
	if err != nil {
		t.Fatalf("expected parse success: %v", err)
	}
	if !parsed.Marker {
		t.Fatalf("expected marker bit to be true")
	}
	if parsed.PayloadType != 96 {
		t.Fatalf("unexpected payload type: got=%d want=96", parsed.PayloadType)
	}
	if parsed.Seq != 0x1234 {
		t.Fatalf("unexpected sequence: got=%#x want=%#x", parsed.Seq, 0x1234)
	}
	if parsed.TS != 0x01020304 {
		t.Fatalf("unexpected timestamp: got=%#x want=%#x", parsed.TS, 0x01020304)
	}
	if parsed.SSRC != 0x0a0b0c0d {
		t.Fatalf("unexpected SSRC: got=%#x want=%#x", parsed.SSRC, 0x0a0b0c0d)
	}
	if parsed.HeaderSize != 12 {
		t.Fatalf("unexpected header size: got=%d want=12", parsed.HeaderSize)
	}
}

// TestRTPParse_TruncatedBufferFails ensures that truncated buffers are rejected
// safely so rtp-cleaner never reads beyond available bytes. The rule is that
// RTP parsing must fail if fewer than 12 bytes are present, because fixed-header
// fields live at offsets 0..11. The synthetic payload is a 2-byte slice that
// still has the version bits set to 2, proving the failure is strictly due to
// length, not version. The expected output is a deterministic error, guarding
// against accidental acceptance of undersized buffers that would lead to
// panics or misclassification of UDP payloads.
func TestRTPParse_TruncatedBufferFails(t *testing.T) {
	packet := []byte{0x80, 0x60}

	_, err := Parse(packet)
	if err == nil {
		t.Fatalf("expected parse error for truncated buffer")
	}
}

// TestRTPParse_MarkerBitAndPTExtraction verifies correct bit masking of the RTP
// marker flag and 7-bit payload type. This matters because rtp-cleaner uses the
// payload type to classify streams, and the marker bit shares the same byte so
// a masking bug would corrupt classification. The synthetic packet uses marker
// bit set with payload type 35 and a distinct sequence/timestamp/SSRC; only the
// marker and payload type fields are asserted. The outputs are deterministic
// because Parse masks the second byte and should not be influenced by other
// header values. The test guards against off-by-one bit errors where marker is
// treated as part of the payload type.
func TestRTPParse_MarkerBitAndPTExtraction(t *testing.T) {
	packet := buildRTPPacket(true, 35, 0x0001, 0x00000001, 0x01020304, nil)

	parsed, err := Parse(packet)
	if err != nil {
		t.Fatalf("expected parse success: %v", err)
	}
	if !parsed.Marker {
		t.Fatalf("expected marker bit to be true")
	}
	if parsed.PayloadType != 35 {
		t.Fatalf("unexpected payload type: got=%d want=35", parsed.PayloadType)
	}
}
