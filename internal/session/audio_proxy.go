package session

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"rtp-stream-cleaner/internal/logging"
	"rtp-stream-cleaner/internal/rtpfix"
)

const udpReadBufferSize = 2048

type audioCounters struct {
	aInPkts         atomic.Uint64
	aInBytes        atomic.Uint64
	bOutPkts        atomic.Uint64
	bOutBytes       atomic.Uint64
	bInPkts         atomic.Uint64
	bInBytes        atomic.Uint64
	aOutPkts        atomic.Uint64
	aOutBytes       atomic.Uint64
	drops           atomic.Uint64
	ignoredDisabled atomic.Uint64
}

type AudioCounters struct {
	AInPkts   uint64
	AInBytes  uint64
	BOutPkts  uint64
	BOutBytes uint64
	BInPkts   uint64
	BInBytes  uint64
	AOutPkts  uint64
	AOutBytes uint64
}

type audioProxy struct {
	session             *Session
	aConn               *net.UDPConn
	bConn               *net.UDPConn
	peerLearningWindow  time.Duration
	statsInterval       time.Duration
	packetLog           bool
	packetLogSampleN    uint64
	packetLogOnAnomaly  bool
	logger              *slog.Logger
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	peerMu              sync.RWMutex
	doorphonePeer       *net.UDPAddr
	doorphoneLearnedAt  time.Time
	lastMissingDestNsec atomic.Int64
}

func newAudioProxy(session *Session, aConn, bConn *net.UDPConn, peerLearningWindow time.Duration, logConfig ProxyLogConfig) *audioProxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &audioProxy{
		session:            session,
		aConn:              aConn,
		bConn:              bConn,
		peerLearningWindow: peerLearningWindow,
		statsInterval:      logConfig.StatsInterval,
		packetLog:          logConfig.PacketLog,
		packetLogSampleN:   logConfig.PacketLogSampleN,
		packetLogOnAnomaly: logConfig.PacketLogOnAnomaly,
		logger:             logging.WithSessionID(session.ID),
		ctx:                ctx,
		cancel:             cancel,
	}
}

func (p *audioProxy) start() {
	p.wg.Add(2)
	go func() {
		defer p.wg.Done()
		p.loopAIn()
	}()
	go func() {
		defer p.wg.Done()
		p.loopBIn()
	}()
	if p.statsInterval > 0 {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.logStatsLoop()
		}()
	}
}

func (p *audioProxy) stop() {
	p.cancel()
	_ = p.aConn.SetReadDeadline(time.Now())
	_ = p.bConn.SetReadDeadline(time.Now())
	p.wg.Wait()
	_ = p.aConn.Close()
	_ = p.bConn.Close()
}

func (p *audioProxy) loopAIn() {
	buffer := make([]byte, udpReadBufferSize)
	var packetCount uint64
	var lastSeq uint16
	var hasLastSeq bool
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
			p.logger.Error("audio a leg read failed", "error", err)
			continue
		}
		p.session.markActivity(time.Now())
		p.session.audioCounters.aInPkts.Add(1)
		p.session.audioCounters.aInBytes.Add(uint64(n))
		if !p.session.audioEnabled.Load() {
			p.session.audioCounters.ignoredDisabled.Add(1)
			continue
		}
		p.logPacketIfNeeded(buffer[:n], n, "a->b", &packetCount, &lastSeq, &hasLastSeq)
		if !p.updateDoorphonePeer(addr) {
			p.session.audioCounters.drops.Add(1)
			continue
		}
		dest := p.session.audioDest.Load()
		if dest == nil {
			p.logMissingDest()
			p.session.audioCounters.drops.Add(1)
			continue
		}
		if _, err := p.bConn.WriteToUDP(buffer[:n], dest); err != nil {
			p.logger.Error("audio b leg write failed", "error", err)
			p.session.audioCounters.drops.Add(1)
			continue
		}
		p.session.audioCounters.bOutPkts.Add(1)
		p.session.audioCounters.bOutBytes.Add(uint64(n))
	}
}

func (p *audioProxy) loopBIn() {
	buffer := make([]byte, udpReadBufferSize)
	var packetCount uint64
	var lastSeq uint16
	var hasLastSeq bool
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
			p.logger.Error("audio b leg read failed", "error", err)
			continue
		}
		p.session.markActivity(time.Now())
		if !p.session.audioEnabled.Load() {
			p.session.audioCounters.ignoredDisabled.Add(1)
			continue
		}
		dest := p.session.audioDest.Load()
		if dest == nil || !dest.IP.Equal(addr.IP) {
			p.session.audioCounters.drops.Add(1)
			continue
		}
		p.session.audioCounters.bInPkts.Add(1)
		p.session.audioCounters.bInBytes.Add(uint64(n))
		p.logPacketIfNeeded(buffer[:n], n, "b->a", &packetCount, &lastSeq, &hasLastSeq)
		peer := p.getDoorphonePeer()
		if peer == nil {
			p.session.audioCounters.drops.Add(1)
			continue
		}
		if _, err := p.aConn.WriteToUDP(buffer[:n], peer); err != nil {
			p.logger.Error("audio a leg write failed", "error", err)
			p.session.audioCounters.drops.Add(1)
			continue
		}
		p.session.audioCounters.aOutPkts.Add(1)
		p.session.audioCounters.aOutBytes.Add(uint64(n))
	}
}

func (p *audioProxy) updateDoorphonePeer(addr *net.UDPAddr) bool {
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

func (p *audioProxy) getDoorphonePeer() *net.UDPAddr {
	p.peerMu.RLock()
	defer p.peerMu.RUnlock()
	return cloneUDPAddr(p.doorphonePeer)
}

func (p *audioProxy) logMissingDest() {
	now := time.Now().UnixNano()
	last := p.lastMissingDestNsec.Load()
	if last != 0 && now-last < int64(5*time.Second) {
		return
	}
	if p.lastMissingDestNsec.CompareAndSwap(last, now) {
		p.logger.Warn("audio rtpengine destination not set")
	}
}

func (p *audioProxy) logStatsLoop() {
	ticker := time.NewTicker(p.statsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.logStats(false)
		case <-p.ctx.Done():
			p.logStats(true)
			return
		}
	}
}

func (p *audioProxy) logStats(final bool) {
	counters := &p.session.audioCounters
	pktsIn := counters.aInPkts.Load() + counters.bInPkts.Load()
	pktsOut := counters.aOutPkts.Load() + counters.bOutPkts.Load()
	bytesIn := counters.aInBytes.Load() + counters.bInBytes.Load()
	bytesOut := counters.aOutBytes.Load() + counters.bOutBytes.Load()
	drops := counters.drops.Load()
	ignoredDisabled := counters.ignoredDisabled.Load()
	enabled := p.session.audioEnabled.Load()
	disabledReason := loadAtomicString(&p.session.audioDisabledReason)
	if enabled {
		disabledReason = ""
	}
	if final {
		p.logger.Info("audio.proxy.stats",
			"pkts_in", pktsIn,
			"pkts_out", pktsOut,
			"bytes_in", bytesIn,
			"bytes_out", bytesOut,
			"drops", drops,
			"ignored_disabled", ignoredDisabled,
			"enabled", enabled,
			"disabled_reason", disabledReason,
			"final", true,
		)
		return
	}
	p.logger.Info("audio.proxy.stats",
		"pkts_in", pktsIn,
		"pkts_out", pktsOut,
		"bytes_in", bytesIn,
		"bytes_out", bytesOut,
		"drops", drops,
		"ignored_disabled", ignoredDisabled,
		"enabled", enabled,
		"disabled_reason", disabledReason,
	)
}

func (p *audioProxy) logPacketIfNeeded(packet []byte, size int, direction string, packetCount *uint64, lastSeq *uint16, hasLastSeq *bool) {
	if !p.packetLog {
		return
	}
	*packetCount++
	logSample := p.packetLogSampleN > 0 && *packetCount%p.packetLogSampleN == 0
	if !logSample && !p.packetLogOnAnomaly {
		return
	}
	header, ok := rtpfix.ParseRTPHeader(packet)
	anomaly := false
	if !ok {
		anomaly = true
	} else {
		if *hasLastSeq {
			expected := *lastSeq + 1
			if header.Seq != expected {
				anomaly = true
			}
		}
		*lastSeq = header.Seq
		*hasLastSeq = true
	}
	if anomaly && p.packetLogOnAnomaly {
		p.logPacket("audio.proxy.packet.anomaly", direction, header, size)
		return
	}
	if logSample {
		p.logPacket("audio.proxy.packet", direction, header, size)
	}
}

func (p *audioProxy) logPacket(msg, direction string, header rtpfix.RTPHeader, size int) {
	p.logger.Debug(msg,
		"direction", direction,
		"seq", header.Seq,
		"ts", header.TS,
		"marker", header.Marker,
		"pt", header.PT,
		"ssrc", header.SSRC,
		"size", size,
	)
}

func snapshotAudioCounters(counters *audioCounters) AudioCounters {
	if counters == nil {
		return AudioCounters{}
	}
	return AudioCounters{
		AInPkts:   counters.aInPkts.Load(),
		AInBytes:  counters.aInBytes.Load(),
		BOutPkts:  counters.bOutPkts.Load(),
		BOutBytes: counters.bOutBytes.Load(),
		BInPkts:   counters.bInPkts.Load(),
		BInBytes:  counters.bInBytes.Load(),
		AOutPkts:  counters.aOutPkts.Load(),
		AOutBytes: counters.aOutBytes.Load(),
	}
}
