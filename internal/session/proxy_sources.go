package session

import (
	"net"
	"strconv"
	"strings"
	"sync"
)

type proxySourceStats struct {
	mu     sync.RWMutex
	counts map[string]uint64
	order  []string
}

func (s *proxySourceStats) observe(addr *net.UDPAddr) {
	if addr == nil {
		return
	}
	src := addr.String()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = make(map[string]uint64)
	}
	if _, exists := s.counts[src]; !exists {
		s.order = append(s.order, src)
	}
	s.counts[src]++
}

func (s *proxySourceStats) format() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.order) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.order))
	for _, src := range s.order {
		parts = append(parts, src+"="+strconv.FormatUint(s.counts[src], 10))
	}
	return strings.Join(parts, " ")
}
