package session

import (
	"net"
	"testing"
)

func TestProxySourceStatsMaintainsInsertionOrderAndCounts(t *testing.T) {
	var stats proxySourceStats

	src1 := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 1111}
	src2 := &net.UDPAddr{IP: net.ParseIP("10.0.0.2"), Port: 2222}

	stats.observe(src1)
	stats.observe(src1)
	stats.observe(src2)
	stats.observe(src1)

	if got, want := stats.format(), "10.0.0.1:1111=3 10.0.0.2:2222=1"; got != want {
		t.Fatalf("unexpected sources format: got %q want %q", got, want)
	}
}
