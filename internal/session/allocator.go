package session

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

var ErrNoPortsAvailable = errors.New("no available ports")

type PortAllocator struct {
	mu        sync.Mutex
	min       int
	max       int
	available []int
	inUse     map[int]bool
}

func NewPortAllocator(minPort, maxPort int) (*PortAllocator, error) {
	if minPort <= 0 || maxPort <= 0 {
		return nil, fmt.Errorf("invalid port range %d-%d", minPort, maxPort)
	}
	if minPort > maxPort {
		return nil, fmt.Errorf("invalid port range %d-%d", minPort, maxPort)
	}
	available := make([]int, 0, maxPort-minPort+1)
	for port := minPort; port <= maxPort; port++ {
		available = append(available, port)
	}
	return &PortAllocator{
		min:       minPort,
		max:       maxPort,
		available: available,
		inUse:     make(map[int]bool),
	}, nil
}

func (p *PortAllocator) Allocate(count int) ([]int, error) {
	if count <= 0 {
		return nil, fmt.Errorf("invalid port request size %d", count)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if count > len(p.available) {
		return nil, ErrNoPortsAvailable
	}
	ports := append([]int(nil), p.available[:count]...)
	p.available = append([]int(nil), p.available[count:]...)
	for _, port := range ports {
		p.inUse[port] = true
	}
	return ports, nil
}

func (p *PortAllocator) Release(ports []int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, port := range ports {
		if !p.inUse[port] {
			continue
		}
		delete(p.inUse, port)
		if port < p.min || port > p.max {
			continue
		}
		p.available = append(p.available, port)
	}
	sort.Ints(p.available)
}
