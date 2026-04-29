package session

import (
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"
)

const portsPerSession = 4

var ErrNoPortsAvailable = errors.New("no available ports")

type PortBinding struct {
	Port int
	Conn *net.UDPConn
}

type SessionPortBindings struct {
	AudioA PortBinding
	AudioB PortBinding
	VideoA PortBinding
	VideoB PortBinding
}

type PortAllocator struct {
	mu           sync.Mutex
	min          int
	max          int
	bindAttempts int
	inUse        map[int]bool
	listenUDP    func(network string, laddr *net.UDPAddr) (*net.UDPConn, error)
	rand         *rand.Rand
}

func NewPortAllocator(minPort, maxPort, bindAttempts int) (*PortAllocator, error) {
	if minPort <= 0 || maxPort <= 0 || minPort > maxPort {
		return nil, fmt.Errorf("invalid port range %d-%d", minPort, maxPort)
	}
	if bindAttempts <= 0 {
		return nil, fmt.Errorf("rtp_port_bind_attempts must be > 0")
	}
	return &PortAllocator{
		min:          minPort,
		max:          maxPort,
		bindAttempts: bindAttempts,
		inUse:        make(map[int]bool),
		listenUDP:    net.ListenUDP,
		rand:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}, nil
}

func (p *PortAllocator) AllocateBindings(network string, bindIP net.IP) (SessionPortBindings, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if (p.max-p.min+1)-len(p.inUse) < portsPerSession {
		return SessionPortBindings{}, fmt.Errorf("%w: range too small for 4 ports", ErrNoPortsAvailable)
	}

	for attempt := 0; attempt < p.bindAttempts; attempt++ {
		ports, err := p.pickPortsLocked(portsPerSession)
		if err != nil {
			return SessionPortBindings{}, err
		}
		bindings, ok := p.tryBindPortsLocked(network, bindIP, ports)
		if ok {
			return bindings, nil
		}
	}
	return SessionPortBindings{}, fmt.Errorf("%w: failed to bind 4 ports after %d attempts", ErrNoPortsAvailable, p.bindAttempts)
}

func (p *PortAllocator) ReleaseBindings(bindings SessionPortBindings) {
	closeIfNotNil(bindings.AudioA.Conn)
	closeIfNotNil(bindings.AudioB.Conn)
	closeIfNotNil(bindings.VideoA.Conn)
	closeIfNotNil(bindings.VideoB.Conn)
	p.Release([]int{bindings.AudioA.Port, bindings.AudioB.Port, bindings.VideoA.Port, bindings.VideoB.Port})
}

func (p *PortAllocator) Release(ports []int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, port := range ports {
		if !p.inUse[port] {
			continue
		}
		delete(p.inUse, port)
	}
}

func (p *PortAllocator) pickPortsLocked(count int) ([]int, error) {
	if count <= 0 {
		return nil, fmt.Errorf("invalid port request size %d", count)
	}
	if (p.max-p.min+1)-len(p.inUse) < count {
		return nil, ErrNoPortsAvailable
	}
	picked := make(map[int]struct{}, count)
	ports := make([]int, 0, count)
	for len(ports) < count {
		candidate := p.min + p.rand.Intn(p.max-p.min+1)
		if p.inUse[candidate] {
			continue
		}
		if _, exists := picked[candidate]; exists {
			continue
		}
		picked[candidate] = struct{}{}
		ports = append(ports, candidate)
	}
	return ports, nil
}

func (p *PortAllocator) tryBindPortsLocked(network string, bindIP net.IP, ports []int) (SessionPortBindings, bool) {
	opened := make([]PortBinding, 0, len(ports))
	for _, port := range ports {
		conn, err := p.listenUDP(network, &net.UDPAddr{IP: bindIP, Port: port})
		if err != nil {
			for _, b := range opened {
				_ = b.Conn.Close()
			}
			return SessionPortBindings{}, false
		}
		opened = append(opened, PortBinding{Port: port, Conn: conn})
	}
	for _, port := range ports {
		p.inUse[port] = true
	}
	return SessionPortBindings{AudioA: opened[0], AudioB: opened[1], VideoA: opened[2], VideoB: opened[3]}, true
}

func closeIfNotNil(conn *net.UDPConn) {
	if conn != nil {
		_ = conn.Close()
	}
}
