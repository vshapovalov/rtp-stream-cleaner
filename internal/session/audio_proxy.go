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
	aToB audioDirectionCounters
	bToA audioDirectionCounters
}

type audioDirectionCounters struct {
	pktsIn                 atomic.Uint64
	pktsOut                atomic.Uint64
	bytesIn                atomic.Uint64
	bytesOut               atomic.Uint64
	ignoredDisabled        atomic.Uint64
	dropDestNotSet         atomic.Uint64
	dropDestIPMismatch     atomic.Uint64
	dropPeerNotLearned     atomic.Uint64
	dropPeerUpdateRejected atomic.Uint64
	dropWriteError         atomic.Uint64
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
	peerLearningTracker *peerLearningTracker
	statsInterval       time.Duration
	packetLog           bool
	packetLogSampleN    uint64
	packetLogOnAnomaly  bool
	logger              *slog.Logger
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	lastMissingDestNsec atomic.Int64
	aToBSources         proxySourceStats
	bToASources         proxySourceStats
}

func newAudioProxy(session *Session, aConn, bConn *net.UDPConn, minPackets int, candidateTTL, relearnIdle time.Duration, logConfig ProxyLogConfig) *audioProxy {
	ctx, cancel := context.WithCancel(context.Background())
	logger := logging.WithSessionID(session.ID)
	return &audioProxy{
		session:             session,
		aConn:               aConn,
		bConn:               bConn,
		peerLearningTracker: newPeerLearningTracker(minPackets, candidateTTL, relearnIdle, logger, session.ID, proxyDirectionAToB, "audio"),
		statsInterval:       logConfig.StatsInterval,
		packetLog:           logConfig.PacketLog,
		packetLogSampleN:    logConfig.PacketLogSampleN,
		packetLogOnAnomaly:  logConfig.PacketLogOnAnomaly,
		logger:              logger,
		ctx:                 ctx,
		cancel:              cancel,
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
		p.aToBSources.observe(addr)
		p.session.audioCounters.aToB.pktsIn.Add(1)
		p.session.audioCounters.aToB.bytesIn.Add(uint64(n))
		if !p.session.audioEnabled.Load() {
			p.session.audioCounters.aToB.ignoredDisabled.Add(1)
			continue
		}
		p.logPacketIfNeeded(buffer[:n], n, "a->b", &packetCount, &lastSeq, &hasLastSeq)
		headerValid := isValidRTPPacket(buffer[:n])
		if !p.updateDoorphonePeer(addr, headerValid) {
			p.session.audioCounters.aToB.dropPeerUpdateRejected.Add(1)
			continue
		}
		dest := p.session.audioDest.Load()
		if dest == nil {
			p.logMissingDest()
			p.session.audioCounters.aToB.dropDestNotSet.Add(1)
			continue
		}
		if _, err := p.bConn.WriteToUDP(buffer[:n], dest); err != nil {
			p.logger.Error("audio b leg write failed", "error", err)
			p.session.audioCounters.aToB.dropWriteError.Add(1)
			continue
		}
		p.session.audioCounters.aToB.pktsOut.Add(1)
		p.session.audioCounters.aToB.bytesOut.Add(uint64(n))
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
		p.bToASources.observe(addr)
		if !p.session.audioEnabled.Load() {
			p.session.audioCounters.bToA.ignoredDisabled.Add(1)
			continue
		}
		dest := p.session.audioDest.Load()
		if dest == nil {
			p.session.audioCounters.bToA.dropDestNotSet.Add(1)
			continue
		}
		if !dest.IP.Equal(addr.IP) {
			p.session.audioCounters.bToA.dropDestIPMismatch.Add(1)
			continue
		}
		p.session.audioCounters.bToA.pktsIn.Add(1)
		p.session.audioCounters.bToA.bytesIn.Add(uint64(n))
		p.logPacketIfNeeded(buffer[:n], n, "b->a", &packetCount, &lastSeq, &hasLastSeq)
		peer := p.getDoorphonePeer()
		if peer == nil {
			p.session.audioCounters.bToA.dropPeerNotLearned.Add(1)
			continue
		}
		if _, err := p.aConn.WriteToUDP(buffer[:n], peer); err != nil {
			p.logger.Error("audio a leg write failed", "error", err)
			p.session.audioCounters.bToA.dropWriteError.Add(1)
			continue
		}
		p.session.audioCounters.bToA.pktsOut.Add(1)
		p.session.audioCounters.bToA.bytesOut.Add(uint64(n))
	}
}

func (p *audioProxy) updateDoorphonePeer(addr *net.UDPAddr, suitable bool) bool {
	if p.peerLearningTracker == nil {
		return false
	}
	return p.peerLearningTracker.observe(addr, suitable, time.Now())
}

func (p *audioProxy) getDoorphonePeer() *net.UDPAddr {
	if p.peerLearningTracker == nil {
		return nil
	}
	return p.peerLearningTracker.learned()
}

func isValidRTPPacket(packet []byte) bool {
	_, ok := rtpfix.ParseRTPHeader(packet)
	return ok
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
	snapshots := snapshotAudioDirectionalStats(&p.session.audioCounters)
	enabled := p.session.audioEnabled.Load()
	disabledReason := loadAtomicString(&p.session.audioDisabledReason)
	if enabled {
		disabledReason = ""
	}
	for _, snapshot := range snapshots {
		learnedPeer := "none"
		if peer := p.getDoorphonePeer(); peer != nil {
			learnedPeer = peer.String()
		}
		args := []any{
			"direction", snapshot.Direction,
			"pkts_in", snapshot.PktsIn,
			"pkts_out", snapshot.PktsOut,
			"bytes_in", snapshot.BytesIn,
			"bytes_out", snapshot.BytesOut,
			"drops_total", snapshot.DropsTotal,
			"drop_dest_not_set", snapshot.DropDestNotSet,
			"drop_dest_ip_mismatch", snapshot.DropDestIPMismatch,
			"drop_peer_not_learned", snapshot.DropPeerNotLearned,
			"drop_peer_update_rejected", snapshot.DropPeerUpdateRejected,
			"drop_write_error", snapshot.DropWriteError,
			"ignored_disabled", snapshot.IgnoredDisabled,
			"learned_peer", learnedPeer,
			"enabled", enabled,
			"disabled_reason", disabledReason,
		}
		if final {
			args = append(args, "final", true)
		}
		p.logger.Info("audio.proxy.stats",
			args...,
		)
	}
	if final {
		p.logger.Info("audio.proxy.sources", "direction", proxyDirectionAToB, "sources", p.aToBSources.format())
		p.logger.Info("audio.proxy.sources", "direction", proxyDirectionBToA, "sources", p.bToASources.format())
	}
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
		AInPkts:   counters.aToB.pktsIn.Load(),
		AInBytes:  counters.aToB.bytesIn.Load(),
		BOutPkts:  counters.aToB.pktsOut.Load(),
		BOutBytes: counters.aToB.bytesOut.Load(),
		BInPkts:   counters.bToA.pktsIn.Load(),
		BInBytes:  counters.bToA.bytesIn.Load(),
		AOutPkts:  counters.bToA.pktsOut.Load(),
		AOutBytes: counters.bToA.bytesOut.Load(),
	}
}

type audioDirectionalStats struct {
	Direction              string
	PktsIn                 uint64
	PktsOut                uint64
	BytesIn                uint64
	BytesOut               uint64
	IgnoredDisabled        uint64
	DropsTotal             uint64
	DropDestNotSet         uint64
	DropDestIPMismatch     uint64
	DropPeerNotLearned     uint64
	DropPeerUpdateRejected uint64
	DropWriteError         uint64
}

func snapshotAudioDirectionalStats(counters *audioCounters) []audioDirectionalStats {
	if counters == nil {
		return nil
	}
	aToB := audioDirectionalStats{
		Direction:              proxyDirectionAToB,
		PktsIn:                 counters.aToB.pktsIn.Load(),
		PktsOut:                counters.aToB.pktsOut.Load(),
		BytesIn:                counters.aToB.bytesIn.Load(),
		BytesOut:               counters.aToB.bytesOut.Load(),
		IgnoredDisabled:        counters.aToB.ignoredDisabled.Load(),
		DropDestNotSet:         counters.aToB.dropDestNotSet.Load(),
		DropDestIPMismatch:     counters.aToB.dropDestIPMismatch.Load(),
		DropPeerNotLearned:     counters.aToB.dropPeerNotLearned.Load(),
		DropPeerUpdateRejected: counters.aToB.dropPeerUpdateRejected.Load(),
		DropWriteError:         counters.aToB.dropWriteError.Load(),
	}
	bToA := audioDirectionalStats{
		Direction:              proxyDirectionBToA,
		PktsIn:                 counters.bToA.pktsIn.Load(),
		PktsOut:                counters.bToA.pktsOut.Load(),
		BytesIn:                counters.bToA.bytesIn.Load(),
		BytesOut:               counters.bToA.bytesOut.Load(),
		IgnoredDisabled:        counters.bToA.ignoredDisabled.Load(),
		DropDestNotSet:         counters.bToA.dropDestNotSet.Load(),
		DropDestIPMismatch:     counters.bToA.dropDestIPMismatch.Load(),
		DropPeerNotLearned:     counters.bToA.dropPeerNotLearned.Load(),
		DropPeerUpdateRejected: counters.bToA.dropPeerUpdateRejected.Load(),
		DropWriteError:         counters.bToA.dropWriteError.Load(),
	}
	aToB.DropsTotal = aToB.DropDestNotSet + aToB.DropDestIPMismatch + aToB.DropPeerNotLearned + aToB.DropPeerUpdateRejected + aToB.DropWriteError
	bToA.DropsTotal = bToA.DropDestNotSet + bToA.DropDestIPMismatch + bToA.DropPeerNotLearned + bToA.DropPeerUpdateRejected + bToA.DropWriteError
	return []audioDirectionalStats{aToB, bToA}
}
