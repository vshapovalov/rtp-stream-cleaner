package session

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type videoCounters struct {
	aInPkts   atomic.Uint64
	aInBytes  atomic.Uint64
	bOutPkts  atomic.Uint64
	bOutBytes atomic.Uint64
	bInPkts   atomic.Uint64
	bInBytes  atomic.Uint64
	aOutPkts  atomic.Uint64
	aOutBytes atomic.Uint64
}

type VideoCounters struct {
	AInPkts   uint64
	AInBytes  uint64
	BOutPkts  uint64
	BOutBytes uint64
	BInPkts   uint64
	BInBytes  uint64
	AOutPkts  uint64
	AOutBytes uint64
}

type videoProxy struct {
	session             *Session
	aConn               *net.UDPConn
	bConn               *net.UDPConn
	peerLearningWindow  time.Duration
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	peerMu              sync.RWMutex
	doorphonePeer       *net.UDPAddr
	doorphoneLearnedAt  time.Time
	lastMissingDestNsec atomic.Int64
}

func newVideoProxy(session *Session, aConn, bConn *net.UDPConn, peerLearningWindow time.Duration) *videoProxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &videoProxy{
		session:            session,
		aConn:              aConn,
		bConn:              bConn,
		peerLearningWindow: peerLearningWindow,
		ctx:                ctx,
		cancel:             cancel,
	}
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
			log.Printf("video a leg read failed session=%s err=%v", p.session.ID, err)
			continue
		}
		p.session.videoCounters.aInPkts.Add(1)
		p.session.videoCounters.aInBytes.Add(uint64(n))
		if !p.updateDoorphonePeer(addr) {
			continue
		}
		dest := p.session.videoDest.Load()
		if dest == nil {
			p.logMissingDest()
			continue
		}
		if _, err := p.bConn.WriteToUDP(buffer[:n], dest); err != nil {
			log.Printf("video b leg write failed session=%s err=%v", p.session.ID, err)
			continue
		}
		p.session.videoCounters.bOutPkts.Add(1)
		p.session.videoCounters.bOutBytes.Add(uint64(n))
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
			log.Printf("video b leg read failed session=%s err=%v", p.session.ID, err)
			continue
		}
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
			log.Printf("video a leg write failed session=%s err=%v", p.session.ID, err)
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
		log.Printf("video rtpengine destination not set session=%s", p.session.ID)
	}
}

func snapshotVideoCounters(counters *videoCounters) VideoCounters {
	if counters == nil {
		return VideoCounters{}
	}
	return VideoCounters{
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
