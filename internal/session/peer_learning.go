package session

import (
	"log/slog"
	"net"
	"sync"
	"time"
)

type peerLearningMode string

const (
	peerLearningModeLearning peerLearningMode = "LEARNING"
	peerLearningModeLocked   peerLearningMode = "LOCKED"
)

type peerLearningTracker struct {
	mu           sync.RWMutex
	state        peerLearningMode
	learnedPeer  *net.UDPAddr
	lastSuitable time.Time
	candidates   map[string]peerLearningCandidate

	minPackets   int
	candidateTTL time.Duration
	relearnIdle  time.Duration

	logger    *slog.Logger
	sessionID string
	direction string
	proxyType string
}

type peerLearningCandidate struct {
	count    int
	lastSeen time.Time
}

func newPeerLearningTracker(minPackets int, candidateTTL, relearnIdle time.Duration, logger *slog.Logger, sessionID, direction, proxyType string) *peerLearningTracker {
	if minPackets <= 0 {
		minPackets = 1
	}
	return &peerLearningTracker{
		state:        peerLearningModeLearning,
		minPackets:   minPackets,
		candidateTTL: candidateTTL,
		relearnIdle:  relearnIdle,
		candidates:   make(map[string]peerLearningCandidate),
		logger:       logger,
		sessionID:    sessionID,
		direction:    direction,
		proxyType:    proxyType,
	}
}

func (p *peerLearningTracker) observe(addr *net.UDPAddr, suitable bool, now time.Time) bool {
	if addr == nil {
		return false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.state == peerLearningModeLocked && p.relearnIdle > 0 && !p.lastSuitable.IsZero() && now.Sub(p.lastSuitable) > p.relearnIdle {
		previousPeer := p.learnedPeer
		idleFor := now.Sub(p.lastSuitable)
		p.state = peerLearningModeLearning
		p.learnedPeer = nil
		p.lastSuitable = time.Time{}
		p.candidates = make(map[string]peerLearningCandidate)
		p.logStateTransition("peer.learning.reactivated", peerLearningModeLocked, peerLearningModeLearning, previousPeer, nil, "idle_timeout", 0, idleFor)
	}

	if p.state == peerLearningModeLocked {
		if sameUDPAddr(p.learnedPeer, addr) {
			if suitable {
				p.lastSuitable = now
			}
			return true
		}
		return false
	}

	if !suitable {
		return false
	}

	p.expireCandidates(now)
	key := addr.String()
	candidate := p.candidates[key]
	candidate.count++
	candidate.lastSeen = now
	p.candidates[key] = candidate
	if candidate.count < p.minPackets {
		return false
	}

	oldState := p.state
	p.state = peerLearningModeLocked
	p.learnedPeer = cloneUDPAddr(addr)
	p.lastSuitable = now
	p.candidates = make(map[string]peerLearningCandidate)
	p.logStateTransition("peer.learning.locked", oldState, peerLearningModeLocked, nil, p.learnedPeer, "initial_lock", candidate.count, 0)
	return true
}

func (p *peerLearningTracker) learned() *net.UDPAddr {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneUDPAddr(p.learnedPeer)
}

func (p *peerLearningTracker) expireCandidates(now time.Time) {
	if p.candidateTTL <= 0 {
		return
	}
	for key, candidate := range p.candidates {
		if now.Sub(candidate.lastSeen) > p.candidateTTL {
			delete(p.candidates, key)
		}
	}
}

func (p *peerLearningTracker) logStateTransition(message string, from, to peerLearningMode, learnedPeer, newPeer *net.UDPAddr, reason string, candidateCount int, idle time.Duration) {
	if p.logger == nil {
		return
	}
	args := []any{
		"session_id", p.sessionID,
		"direction", p.direction,
		"proxy_type", p.proxyType,
		"state_from", from,
		"state_to", to,
		"reason", reason,
		"candidate_count", candidateCount,
	}
	if learnedPeer != nil {
		args = append(args, "learned_peer", learnedPeer.String())
	}
	if newPeer != nil {
		args = append(args, "new_peer", newPeer.String())
	}
	if idle > 0 {
		args = append(args, "idle_ms", idle.Milliseconds())
	}
	p.logger.Info(message, args...)
}

func sameUDPAddr(a, b *net.UDPAddr) bool {
	if a == nil || b == nil {
		return false
	}
	return a.IP.Equal(b.IP) && a.Port == b.Port
}
