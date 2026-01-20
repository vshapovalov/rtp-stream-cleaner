package rtpfix

import "encoding/binary"

type RTPHeader struct {
	PT        uint8
	Seq       uint16
	TS        uint32
	SSRC      uint32
	Marker    bool
	HeaderLen int
}

func parseRTPHeader(packet []byte) (RTPHeader, bool) {
	if len(packet) < 12 {
		return RTPHeader{}, false
	}
	version := packet[0] >> 6
	if version != 2 {
		return RTPHeader{}, false
	}
	cc := int(packet[0] & 0x0f)
	hasExtension := packet[0]&0x10 != 0
	headerLen := 12 + cc*4
	if len(packet) < headerLen {
		return RTPHeader{}, false
	}
	if hasExtension {
		if len(packet) < headerLen+4 {
			return RTPHeader{}, false
		}
		extLenWords := int(binary.BigEndian.Uint16(packet[headerLen+2 : headerLen+4]))
		headerLen += 4 + extLenWords*4
		if len(packet) < headerLen {
			return RTPHeader{}, false
		}
	}
	return RTPHeader{
		PT:        packet[1] & 0x7f,
		Seq:       binary.BigEndian.Uint16(packet[2:4]),
		TS:        binary.BigEndian.Uint32(packet[4:8]),
		SSRC:      binary.BigEndian.Uint32(packet[8:12]),
		Marker:    packet[1]&0x80 != 0,
		HeaderLen: headerLen,
	}, true
}

func ParseRTPHeader(packet []byte) (RTPHeader, bool) {
	return parseRTPHeader(packet)
}
