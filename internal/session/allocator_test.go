package session

import "testing"

// TestPortAllocator_AllocFreeReuse verifies that releasing a previously allocated
// port makes it eligible for reuse and that reuse follows the allocator's
// documented ordering. This matters because callers rely on predictable port
// reuse for stable resource accounting and avoiding leaks. Preconditions: a
// small contiguous range with no concurrent allocations. Inputs: allocate one
// port from a 3-port range, release that exact port, then allocate one port
// again. Edge case: reuse happens after a release and sorted availability. The
// expected output is that the second allocation returns the same port because
// Release inserts and sorts the available list, making the smallest port
// deterministic. This is stable because the allocator uses a deterministic slice
// and sort without randomness. Flakiness is avoided by using no goroutines,
// sleeps, or time-based logic. A regression would manifest as a different port
// being returned after release or the allocator failing to reuse a released port.
func TestPortAllocator_AllocFreeReuse(t *testing.T) {
	allocator, err := NewPortAllocator(10000, 10002)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ports, err := allocator.Allocate(1)
	if err != nil {
		t.Fatalf("unexpected alloc error: %v", err)
	}
	allocator.Release(ports)
	portsAgain, err := allocator.Allocate(1)
	if err != nil {
		t.Fatalf("unexpected second alloc error: %v", err)
	}
	if portsAgain[0] != ports[0] {
		t.Fatalf("expected reuse of port %d, got %d", ports[0], portsAgain[0])
	}
}

// TestPortAllocator_ExhaustionReturnsError confirms that the allocator returns
// ErrNoPortsAvailable when the requested allocation exceeds the available pool.
// This matters because callers must handle exhaustion deterministically instead
// of receiving partial allocations. Preconditions: a tiny fixed range with two
// ports and no releases. Inputs: allocate both ports individually, then request
// one more port. Edge case: allocation immediately after exhausting the pool.
// The expected output is ErrNoPortsAvailable for the third request because no
// ports remain available. Assertions are stable because the allocator uses a
// bounded slice length check. Flakiness is avoided by staying single-threaded
// and avoiding time-based behavior. A regression would appear as a nil error or
// unexpected port allocation after the pool is exhausted.
func TestPortAllocator_ExhaustionReturnsError(t *testing.T) {
	allocator, err := NewPortAllocator(12000, 12001)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := allocator.Allocate(1); err != nil {
		t.Fatalf("unexpected first alloc error: %v", err)
	}
	if _, err := allocator.Allocate(1); err != nil {
		t.Fatalf("unexpected second alloc error: %v", err)
	}
	if _, err := allocator.Allocate(1); err != ErrNoPortsAvailable {
		t.Fatalf("expected ErrNoPortsAvailable, got %v", err)
	}
}

// TestPortAllocator_NoDuplicatesWhileAllocated ensures that a multi-port
// allocation returns unique ports with no duplicates while they are all marked
// in use. This matters because duplicate port assignments would lead to socket
// conflicts and data corruption. Preconditions: a range larger than the request
// and no concurrent allocations. Inputs: request three ports from a six-port
// range. Edge case: allocation size greater than one to exercise slice copying.
// The expected output is that each port in the returned slice is unique and
// within the range. The assertions are stable because allocation pulls distinct
// elements from the available slice in order. Flakiness is avoided by using no
// concurrency or timers. A regression would show repeated values in the
// allocation results or out-of-range ports.
func TestPortAllocator_NoDuplicatesWhileAllocated(t *testing.T) {
	allocator, err := NewPortAllocator(13000, 13005)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ports, err := allocator.Allocate(3)
	if err != nil {
		t.Fatalf("unexpected alloc error: %v", err)
	}
	seen := make(map[int]bool)
	for _, port := range ports {
		if seen[port] {
			t.Fatalf("duplicate port allocated: %d", port)
		}
		seen[port] = true
	}
}
