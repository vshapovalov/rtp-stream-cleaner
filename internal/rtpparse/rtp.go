package rtpparse

import "fmt"

// Packet represents a minimally parsed RTP packet.
type Packet struct {
	SSRC        uint32
	PayloadType uint8
	HeaderSize  int
}

// Parse inspects payload and returns RTP metadata when it looks like RTP.
func Parse(payload []byte) (Packet, error) {
	if len(payload) < 12 {
		return Packet{}, fmt.Errorf("rtp payload too short: %d", len(payload))
	}
	version := payload[0] >> 6
	if version != 2 {
		return Packet{}, fmt.Errorf("unsupported rtp version: %d", version)
	}
	cc := int(payload[0] & 0x0f)
	extension := payload[0]&0x10 != 0
	headerSize := 12 + cc*4
	if len(payload) < headerSize {
		return Packet{}, fmt.Errorf("rtp header truncated")
	}
	if extension {
		if len(payload) < headerSize+4 {
			return Packet{}, fmt.Errorf("rtp extension truncated")
		}
		extLen := int(payload[headerSize+2])<<8 | int(payload[headerSize+3])
		headerSize += 4 + extLen*4
		if len(payload) < headerSize {
			return Packet{}, fmt.Errorf("rtp extension data truncated")
		}
	}
	payloadType := payload[1] & 0x7f
	ssrc := uint32(payload[8])<<24 | uint32(payload[9])<<16 | uint32(payload[10])<<8 | uint32(payload[11])
	return Packet{SSRC: ssrc, PayloadType: payloadType, HeaderSize: headerSize}, nil
}
