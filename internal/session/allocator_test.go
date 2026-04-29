package session

import (
	"errors"
	"net"
	"testing"
)

func TestPortAllocator_AllocateBindingsReturnsFourUniqueBoundPorts(t *testing.T) {
	allocator, err := NewPortAllocator(20000, 20020, 5)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := allocator.AllocateBindings("udp4", net.IPv4zero)
	if err != nil {
		t.Fatal(err)
	}
	defer allocator.ReleaseBindings(bindings)
	ports := []int{bindings.AudioA.Port, bindings.AudioB.Port, bindings.VideoA.Port, bindings.VideoB.Port}
	seen := map[int]bool{}
	for _, p := range ports {
		if p < 20000 || p > 20020 {
			t.Fatalf("out of range %d", p)
		}
		if seen[p] {
			t.Fatalf("duplicate %d", p)
		}
		seen[p] = true
	}
	for _, c := range []*net.UDPConn{bindings.AudioA.Conn, bindings.AudioB.Conn, bindings.VideoA.Conn, bindings.VideoB.Conn} {
		if c == nil {
			t.Fatal("nil conn")
		}
	}
}

func TestPortAllocator_RetryOnBindFailure(t *testing.T) {
	allocator, _ := NewPortAllocator(21000, 21020, 2)
	calls := 0
	allocator.listenUDP = func(network string, laddr *net.UDPAddr) (*net.UDPConn, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("busy")
		}
		return net.ListenUDP(network, laddr)
	}
	bindings, err := allocator.AllocateBindings("udp4", net.IPv4zero)
	if err != nil {
		t.Fatal(err)
	}
	allocator.ReleaseBindings(bindings)
	if calls < 5 {
		t.Fatalf("expected retry, calls=%d", calls)
	}
}

func TestPortAllocator_AttemptsExhausted(t *testing.T) {
	allocator, _ := NewPortAllocator(22000, 22020, 1)
	allocator.listenUDP = func(string, *net.UDPAddr) (*net.UDPConn, error) { return nil, errors.New("busy") }
	_, err := allocator.AllocateBindings("udp4", net.IPv4zero)
	if !errors.Is(err, ErrNoPortsAvailable) {
		t.Fatalf("expected ErrNoPortsAvailable got %v", err)
	}
}
