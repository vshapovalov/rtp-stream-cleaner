package rtpfix

type H264Info struct {
	IsSlice bool
	IsFU    bool
	FUStart bool
	FUEnd   bool
	NALType uint8
	IsSPS   bool
	IsPPS   bool
	IsIDR   bool
}

func parseH264(payload []byte) (H264Info, bool) {
	if len(payload) == 0 {
		return H264Info{}, false
	}
	first := payload[0]
	unitType := first & 0x1f
	info := H264Info{}
	if unitType == 28 {
		if len(payload) < 2 {
			return H264Info{}, false
		}
		fuHeader := payload[1]
		info.IsFU = true
		info.FUStart = fuHeader&0x80 != 0
		info.FUEnd = fuHeader&0x40 != 0
		info.NALType = fuHeader & 0x1f
	} else {
		info.NALType = unitType
	}
	info.IsSPS = info.NALType == 7
	info.IsPPS = info.NALType == 8
	info.IsIDR = info.NALType == 5
	info.IsSlice = info.NALType >= 1 && info.NALType <= 5
	return info, true
}

func ParseH264(payload []byte) (H264Info, bool) {
	return parseH264(payload)
}

func isFrameStart(info H264Info) bool {
	if !info.IsSlice {
		return false
	}
	if info.IsFU {
		return info.FUStart
	}
	return true
}

func isFrameEnd(info H264Info) bool {
	if !info.IsSlice {
		return false
	}
	if info.IsFU {
		return info.FUEnd
	}
	return true
}

func IsFrameStart(info H264Info) bool {
	return isFrameStart(info)
}

func IsFrameEnd(info H264Info) bool {
	return isFrameEnd(info)
}
