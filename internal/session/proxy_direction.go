package session

import "net"

const (
	proxyDirectionAToB = "A->B"
	proxyDirectionBToA = "B->A"
)

func inferProxyDirection(aConn, bConn *net.UDPConn, aPort, bPort int) string {
	aLocalPort := localUDPPort(aConn)
	bLocalPort := localUDPPort(bConn)
	if aLocalPort == bPort && bLocalPort == aPort {
		return proxyDirectionBToA
	}
	return proxyDirectionAToB
}

func localUDPPort(conn *net.UDPConn) int {
	if conn == nil {
		return 0
	}
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr == nil {
		return 0
	}
	return addr.Port
}
