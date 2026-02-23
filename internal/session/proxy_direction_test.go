package session

import (
	"net"
	"testing"
)

func TestInferProxyDirection(t *testing.T) {
	aConn := mustListenUDP(t)
	defer aConn.Close()
	bConn := mustListenUDP(t)
	defer bConn.Close()

	aPort := aConn.LocalAddr().(*net.UDPAddr).Port
	bPort := bConn.LocalAddr().(*net.UDPAddr).Port

	if got := inferProxyDirection(aConn, bConn, aPort, bPort); got != proxyDirectionAToB {
		t.Fatalf("expected %q, got %q", proxyDirectionAToB, got)
	}
	if got := inferProxyDirection(bConn, aConn, aPort, bPort); got != proxyDirectionBToA {
		t.Fatalf("expected %q, got %q", proxyDirectionBToA, got)
	}
}
