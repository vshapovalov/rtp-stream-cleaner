package session

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"rtp-stream-cleaner/internal/logging"
	"rtp-stream-cleaner/internal/rtpfix"
)

type videoCounters struct {
	aInPkts            atomic.Uint64
	aInBytes           atomic.Uint64
	bOutPkts           atomic.Uint64
	bOutBytes          atomic.Uint64
	bInPkts            atomic.Uint64
	bInBytes           atomic.Uint64
	aOutPkts           atomic.Uint64
	aOutBytes          atomic.Uint64
	videoFramesStarted atomic.Uint64
	videoFramesEnded   atomic.Uint64
	videoFramesFlushed atomic.Uint64
	videoForcedFlushes atomic.Uint64
	videoInjectedSPS   atomic.Uint64
	videoInjectedPPS   atomic.Uint64
	videoSeqDelta      atomic.Uint64
}

type VideoCounters struct {
	AInPkts            uint64
	AInBytes           uint64
	BOutPkts           uint64
	BOutBytes          uint64
	BInPkts            uint64
	BInBytes           uint64
	AOutPkts           uint64
	AOutBytes          uint64
	VideoFramesStarted uint64
	VideoFramesEnded   uint64
	VideoFramesFlushed uint64
	VideoForcedFlushes uint64
	VideoInjectedSPS   uint64
	VideoInjectedPPS   uint64
	VideoSeqDelta      uint64
}

type videoProxy struct {
	session             *Session
	aConn               *net.UDPConn
	bConn               *net.UDPConn
	peerLearningWindow  time.Duration
	maxFrameWait        time.Duration
	logger              *slog.Logger
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	peerMu              sync.RWMutex
	doorphonePeer       *net.UDPAddr
	doorphoneLearnedAt  time.Time
	lastMissingDestNsec atomic.Int64
	frameBuffer         [][]byte
	frameBufferStart    time.Time
	frameBufferActive   bool
	lastFrameSentTime   time.Time
	frameTS             uint32
	frameTSInitialized  bool
	currentFrameTS      uint32
	currentFrameTSSet   bool
	fixEnabled          bool
	pendingSPS          []byte
	pendingPPS          []byte
	cachedSPS           []byte
	cachedPPS           []byte
	injectCachedSPSPPS  bool
	seqDelta            uint16
	lastOutSeq          uint16
	hasLastOutSeq       bool
	writeToDest         func([]byte, *net.UDPAddr) error
}

func newVideoProxy(session *Session, aConn, bConn *net.UDPConn, peerLearningWindow, maxFrameWait time.Duration, fixEnabled, injectCachedSPSPPS bool) *videoProxy {
	ctx, cancel := context.WithCancel(context.Background())
	if !fixEnabled {
		injectCachedSPSPPS = false
	}
	proxy := &videoProxy{
		session:            session,
		aConn:              aConn,
		bConn:              bConn,
		peerLearningWindow: peerLearningWindow,
		maxFrameWait:       maxFrameWait,
		ctx:                ctx,
		cancel:             cancel,
		fixEnabled:         fixEnabled,
		injectCachedSPSPPS: injectCachedSPSPPS,
		logger:             logging.WithSessionID(session.ID),
	}
	proxy.writeToDest = func(packet []byte, dest *net.UDPAddr) error {
		if bConn == nil {
			return errors.New("video b conn is nil")
		}
		_, err := bConn.WriteToUDP(packet, dest)
		return err
	}
	return proxy
}

func (p *videoProxy) start() {
	p.wg.Add(2)
	go func() {
		defer p.wg.Done()
		p.loopAIn()
	}()
	go func() {
		defer p.wg.Done()
		p.loopBIn()
	}()
}

func (p *videoProxy) stop() {
	p.cancel()
	_ = p.aConn.SetReadDeadline(time.Now())
	_ = p.bConn.SetReadDeadline(time.Now())
	p.wg.Wait()
	_ = p.aConn.Close()
	_ = p.bConn.Close()
}

func (p *videoProxy) loopAIn() {
	buffer := make([]byte, udpReadBufferSize)
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		_ = p.aConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := p.aConn.ReadFromUDP(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			p.logger.Error("video a leg read failed", "error", err)
			continue
		}
		p.session.markActivity(time.Now())
		p.session.videoCounters.aInPkts.Add(1)
		p.session.videoCounters.aInBytes.Add(uint64(n))
		if p.fixEnabled {
			p.analyzeFrameBoundaries(buffer[:n])
		}
		if !p.updateDoorphonePeer(addr) {
			continue
		}
		dest := p.session.videoDest.Load()
		if dest == nil {
			if p.fixEnabled {
				p.resetFrameBuffer()
			}
			p.logMissingDest()
			continue
		}
		if p.fixEnabled {
			p.handleVideoPacket(buffer[:n], dest)
			continue
		}
		p.forwardRawPacket(buffer[:n], dest)
	}
}

func (p *videoProxy) loopBIn() {
	buffer := make([]byte, udpReadBufferSize)
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		_ = p.bConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := p.bConn.ReadFromUDP(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			p.logger.Error("video b leg read failed", "error", err)
			continue
		}
		p.session.markActivity(time.Now())
		dest := p.session.videoDest.Load()
		if dest == nil || !dest.IP.Equal(addr.IP) {
			continue
		}
		p.session.videoCounters.bInPkts.Add(1)
		p.session.videoCounters.bInBytes.Add(uint64(n))
		peer := p.getDoorphonePeer()
		if peer == nil {
			continue
		}
		if _, err := p.aConn.WriteToUDP(buffer[:n], peer); err != nil {
			p.logger.Error("video a leg write failed", "error", err)
			continue
		}
		p.session.videoCounters.aOutPkts.Add(1)
		p.session.videoCounters.aOutBytes.Add(uint64(n))
	}
}

func (p *videoProxy) updateDoorphonePeer(addr *net.UDPAddr) bool {
	if addr == nil {
		return false
	}
	p.peerMu.Lock()
	defer p.peerMu.Unlock()
	now := time.Now()
	if p.doorphonePeer == nil {
		p.doorphonePeer = cloneUDPAddr(addr)
		p.doorphoneLearnedAt = now
		return true
	}
	if p.doorphonePeer.IP.Equal(addr.IP) && p.doorphonePeer.Port == addr.Port {
		return true
	}
	if now.Sub(p.doorphoneLearnedAt) <= p.peerLearningWindow {
		p.doorphonePeer = cloneUDPAddr(addr)
		return true
	}
	return false
}

func (p *videoProxy) getDoorphonePeer() *net.UDPAddr {
	p.peerMu.RLock()
	defer p.peerMu.RUnlock()
	return cloneUDPAddr(p.doorphonePeer)
}

func (p *videoProxy) logMissingDest() {
	now := time.Now().UnixNano()
	last := p.lastMissingDestNsec.Load()
	if last != 0 && now-last < int64(5*time.Second) {
		return
	}
	if p.lastMissingDestNsec.CompareAndSwap(last, now) {
		p.logger.Warn("video rtpengine destination not set")
	}
}

func snapshotVideoCounters(counters *videoCounters) VideoCounters {
	if counters == nil {
		return VideoCounters{}
	}
	return VideoCounters{
		AInPkts:            counters.aInPkts.Load(),
		AInBytes:           counters.aInBytes.Load(),
		BOutPkts:           counters.bOutPkts.Load(),
		BOutBytes:          counters.bOutBytes.Load(),
		BInPkts:            counters.bInPkts.Load(),
		BInBytes:           counters.bInBytes.Load(),
		AOutPkts:           counters.aOutPkts.Load(),
		AOutBytes:          counters.aOutBytes.Load(),
		VideoFramesStarted: counters.videoFramesStarted.Load(),
		VideoFramesEnded:   counters.videoFramesEnded.Load(),
		VideoFramesFlushed: counters.videoFramesFlushed.Load(),
		VideoForcedFlushes: counters.videoForcedFlushes.Load(),
		VideoInjectedSPS:   counters.videoInjectedSPS.Load(),
		VideoInjectedPPS:   counters.videoInjectedPPS.Load(),
		VideoSeqDelta:      counters.videoSeqDelta.Load(),
	}
}

func (p *videoProxy) analyzeFrameBoundaries(packet []byte) {
	header, ok := rtpfix.ParseRTPHeader(packet)
	if !ok {
		return
	}
	if header.HeaderLen >= len(packet) {
		return
	}
	payload := packet[header.HeaderLen:]
	info, ok := rtpfix.ParseH264(payload)
	if !ok {
		return
	}
	if rtpfix.IsFrameStart(info) {
		p.session.videoCounters.videoFramesStarted.Add(1)
	}
	if rtpfix.IsFrameEnd(info) {
		p.session.videoCounters.videoFramesEnded.Add(1)
	}
}

func (p *videoProxy) handleVideoPacket(packet []byte, dest *net.UDPAddr) {
	packetInfo, ok := parseH264Packet(packet)
	if ok {
		now := time.Now()
		if packetInfo.info.IsSlice {
			p.flushOnTimeout(now, dest)
			if rtpfix.IsFrameStart(packetInfo.info) {
				if p.frameBufferActive && len(p.frameBuffer) > 0 {
					p.flushFrameBuffer(now, dest, false)
				}
				p.startFrameBuffer(now, packet)
				if packetInfo.info.IsIDR {
					p.injectCachedParameterSets(packetInfo.header, dest)
				}
				p.appendPendingToFrameBuffer()
			}
			if p.frameBufferActive {
				p.bufferFramePacket(packet)
				if rtpfix.IsFrameEnd(packetInfo.info) {
					p.flushFrameBuffer(now, dest, false)
				}
				return
			}
		}
		if packetInfo.info.IsSPS || packetInfo.info.IsPPS {
			p.cacheParameterSet(packetInfo.payload, packetInfo.info.IsSPS)
			p.flushOnTimeout(now, dest)
			if p.frameBufferActive {
				p.bufferFramePacket(packet)
			} else {
				p.storePendingParameterSet(packet, packetInfo.info.IsSPS)
			}
			return
		}
	}
	p.flushOnTimeout(time.Now(), dest)
	p.sendPacket(packet, dest)
}

type h264Packet struct {
	header  rtpfix.RTPHeader
	payload []byte
	info    rtpfix.H264Info
}

func parseH264Packet(packet []byte) (h264Packet, bool) {
	header, ok := rtpfix.ParseRTPHeader(packet)
	if !ok {
		return h264Packet{}, false
	}
	if header.HeaderLen >= len(packet) {
		return h264Packet{}, false
	}
	payload := packet[header.HeaderLen:]
	info, ok := rtpfix.ParseH264(payload)
	if !ok {
		return h264Packet{}, false
	}
	return h264Packet{
		header:  header,
		payload: payload,
		info:    info,
	}, true
}

func (p *videoProxy) startFrameBuffer(now time.Time, seedPacket []byte) {
	p.frameBuffer = p.frameBuffer[:0]
	p.frameBufferStart = now
	p.frameBufferActive = true
	p.currentFrameTS = p.nextFrameTimestamp(now, seedPacket)
	p.currentFrameTSSet = true
}

func (p *videoProxy) bufferFramePacket(packet []byte) {
	clone := make([]byte, len(packet))
	copy(clone, packet)
	p.frameBuffer = append(p.frameBuffer, clone)
}

func (p *videoProxy) storePendingParameterSet(packet []byte, isSPS bool) {
	clone := make([]byte, len(packet))
	copy(clone, packet)
	if isSPS {
		p.pendingSPS = clone
		return
	}
	p.pendingPPS = clone
}

func (p *videoProxy) cacheParameterSet(payload []byte, isSPS bool) {
	clone := make([]byte, len(payload))
	copy(clone, payload)
	if isSPS {
		p.cachedSPS = clone
		return
	}
	p.cachedPPS = clone
}

func (p *videoProxy) appendPendingToFrameBuffer() {
	if p.pendingSPS != nil {
		p.frameBuffer = append(p.frameBuffer, p.pendingSPS)
		p.pendingSPS = nil
	}
	if p.pendingPPS != nil {
		p.frameBuffer = append(p.frameBuffer, p.pendingPPS)
		p.pendingPPS = nil
	}
}

func (p *videoProxy) flushOnTimeout(now time.Time, dest *net.UDPAddr) {
	if !p.frameBufferActive || len(p.frameBuffer) == 0 {
		return
	}
	if now.Sub(p.frameBufferStart) <= p.maxFrameWait {
		return
	}
	p.flushFrameBuffer(now, dest, true)
}

func (p *videoProxy) flushFrameBuffer(now time.Time, dest *net.UDPAddr, forced bool) {
	if len(p.frameBuffer) == 0 {
		p.frameBufferActive = false
		return
	}
	frameTS := p.currentFrameTS
	if !p.currentFrameTSSet {
		frameTS = p.nextFrameTimestamp(now, p.frameBuffer[0])
	}
	last := len(p.frameBuffer) - 1
	for i, packet := range p.frameBuffer {
		setMarker(packet, i == last)
		setTimestamp(packet, frameTS)
		p.sendPacket(packet, dest)
	}
	p.session.videoCounters.videoFramesFlushed.Add(1)
	if forced {
		p.session.videoCounters.videoForcedFlushes.Add(1)
	}
	p.frameBufferActive = false
	p.currentFrameTSSet = false
	p.frameBuffer = p.frameBuffer[:0]
}

func (p *videoProxy) sendPacket(packet []byte, dest *net.UDPAddr) {
	if p.injectCachedSPSPPS {
		p.rewriteSeqForOutput(packet)
	}
	if err := p.writeToDest(packet, dest); err != nil {
		p.logger.Error("video b leg write failed", "error", err)
		return
	}
	p.session.videoCounters.bOutPkts.Add(1)
	p.session.videoCounters.bOutBytes.Add(uint64(len(packet)))
}

func (p *videoProxy) forwardRawPacket(packet []byte, dest *net.UDPAddr) {
	if err := p.writeToDest(packet, dest); err != nil {
		p.logger.Error("video b leg write failed", "error", err)
		return
	}
	p.session.videoCounters.bOutPkts.Add(1)
	p.session.videoCounters.bOutBytes.Add(uint64(len(packet)))
}

func (p *videoProxy) resetFrameBuffer() {
	p.frameBufferActive = false
	p.frameBuffer = p.frameBuffer[:0]
	p.frameBufferStart = time.Time{}
	p.currentFrameTSSet = false
}

func (p *videoProxy) injectCachedParameterSets(header rtpfix.RTPHeader, dest *net.UDPAddr) {
	if !p.injectCachedSPSPPS {
		return
	}
	if p.pendingSPS != nil || p.pendingPPS != nil {
		return
	}
	if p.cachedSPS == nil && p.cachedPPS == nil {
		return
	}
	p.ensureSeqBaseline(header.Seq)
	if p.cachedSPS != nil {
		p.sendInjectedPacket(p.cachedSPS, header, dest, true)
	}
	if p.cachedPPS != nil {
		p.sendInjectedPacket(p.cachedPPS, header, dest, false)
	}
}

func (p *videoProxy) sendInjectedPacket(payload []byte, header rtpfix.RTPHeader, dest *net.UDPAddr, isSPS bool) {
	seq := p.lastOutSeq + 1
	packet := make([]byte, 12+len(payload))
	packet[0] = 0x80
	packet[1] = header.PT & 0x7f
	binary.BigEndian.PutUint16(packet[2:4], seq)
	binary.BigEndian.PutUint32(packet[4:8], p.currentFrameTS)
	binary.BigEndian.PutUint32(packet[8:12], header.SSRC)
	copy(packet[12:], payload)
	if err := p.writeToDest(packet, dest); err != nil {
		p.logger.Error("video b leg write failed", "error", err)
		return
	}
	p.session.videoCounters.bOutPkts.Add(1)
	p.session.videoCounters.bOutBytes.Add(uint64(len(packet)))
	p.lastOutSeq = seq
	p.hasLastOutSeq = true
	p.seqDelta++
	p.session.videoCounters.videoSeqDelta.Store(uint64(p.seqDelta))
	if isSPS {
		p.session.videoCounters.videoInjectedSPS.Add(1)
	} else {
		p.session.videoCounters.videoInjectedPPS.Add(1)
	}
}

func (p *videoProxy) ensureSeqBaseline(seq uint16) {
	if p.hasLastOutSeq {
		return
	}
	p.lastOutSeq = seq - 1
	p.hasLastOutSeq = true
}

func (p *videoProxy) rewriteSeqForOutput(packet []byte) {
	if len(packet) < 4 {
		return
	}
	seqIn := binary.BigEndian.Uint16(packet[2:4])
	seqOut := seqIn + p.seqDelta
	binary.BigEndian.PutUint16(packet[2:4], seqOut)
	p.lastOutSeq = seqOut
	p.hasLastOutSeq = true
}

func (p *videoProxy) nextFrameTimestamp(now time.Time, seedPacket []byte) uint32 {
	if !p.frameTSInitialized {
		header, ok := rtpfix.ParseRTPHeader(seedPacket)
		if ok {
			p.frameTS = header.TS
		}
		p.frameTSInitialized = true
		p.lastFrameSentTime = now
		return p.frameTS
	}
	dt := now.Sub(p.lastFrameSentTime)
	if dt < 10*time.Millisecond {
		dt = 10 * time.Millisecond
	}
	if dt > 100*time.Millisecond {
		dt = 100 * time.Millisecond
	}
	increment := uint32((dt.Seconds() * 90000) + 0.5)
	p.frameTS += increment
	p.lastFrameSentTime = now
	return p.frameTS
}

func setMarker(packet []byte, marker bool) {
	if len(packet) < 2 {
		return
	}
	if marker {
		packet[1] |= 0x80
		return
	}
	packet[1] &^= 0x80
}

func setTimestamp(packet []byte, timestamp uint32) {
	if len(packet) < 8 {
		return
	}
	binary.BigEndian.PutUint32(packet[4:8], timestamp)
}
