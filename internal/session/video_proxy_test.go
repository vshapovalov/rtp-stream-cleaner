package session

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestVideoProxyRawModeForwardsPackets(t *testing.T) {
	session := &Session{ID: "S-raw"}
	session.videoEnabled.Store(true)
	aConn := mustListenUDP(t)
	bConn := mustListenUDP(t)
	rtpEngineConn := mustListenUDP(t)
	defer rtpEngineConn.Close()

	dest := localUDPAddr(rtpEngineConn)
	session.videoDest.Store(dest)

	proxy := newVideoProxy(session, aConn, bConn, 200*time.Millisecond, 50*time.Millisecond, false, true, ProxyLogConfig{})
	proxy.start()
	defer proxy.stop()

	doorphoneConn := mustListenUDP(t)
	defer doorphoneConn.Close()

	inputs := [][]byte{
		makeRTPPacket(1, 9000, []byte{0x65, 0x00}),
		makeRTPPacket(2, 9001, []byte{0x41, 0x01}),
	}

	for _, packet := range inputs {
		if _, err := doorphoneConn.WriteToUDP(packet, localUDPAddr(aConn)); err != nil {
			t.Fatalf("send to a-leg failed: %v", err)
		}
	}

	received := make([][]byte, 0, len(inputs))
	buffer := make([]byte, 2048)
	for i := 0; i < len(inputs); i++ {
		_ = rtpEngineConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, _, err := rtpEngineConn.ReadFromUDP(buffer)
		if err != nil {
			t.Fatalf("read from rtpengine failed: %v", err)
		}
		packet := make([]byte, n)
		copy(packet, buffer[:n])
		received = append(received, packet)
	}

	for i, packet := range inputs {
		if !bytes.Equal(packet, received[i]) {
			t.Fatalf("packet %d mismatch: got=%v want=%v", i, received[i], packet)
		}
	}

	counters := snapshotVideoCounters(&session.videoCounters)
	if counters.BOutPkts != uint64(len(inputs)) {
		t.Fatalf("unexpected output count: got=%d want=%d", counters.BOutPkts, len(inputs))
	}
	if counters.VideoInjectedSPS != 0 || counters.VideoInjectedPPS != 0 {
		t.Fatalf("unexpected injected parameter sets: sps=%d pps=%d", counters.VideoInjectedSPS, counters.VideoInjectedPPS)
	}
	if counters.VideoForcedFlushes != 0 {
		t.Fatalf("unexpected forced flushes: %d", counters.VideoForcedFlushes)
	}
}

func TestVideoProxyFixModeForcedFlush(t *testing.T) {
	session := &Session{ID: "S-fix"}
	aConn := mustListenUDP(t)
	bConn := mustListenUDP(t)
	defer aConn.Close()
	defer bConn.Close()

	rtpEngineConn := mustListenUDP(t)
	defer rtpEngineConn.Close()

	dest := localUDPAddr(rtpEngineConn)

	proxy := newVideoProxy(session, aConn, bConn, 200*time.Millisecond, time.Millisecond, true, true, ProxyLogConfig{})

	fuStart := makeRTPPacket(1, 9000, []byte{28, 0x85})
	proxy.handleVideoPacket(fuStart, dest)

	time.Sleep(2 * time.Millisecond)

	sps := makeRTPPacket(2, 9000, []byte{7})
	proxy.handleVideoPacket(sps, dest)

	counters := snapshotVideoCounters(&session.videoCounters)
	if counters.VideoForcedFlushes == 0 {
		t.Fatalf("expected forced flush in fix mode")
	}
	if counters.VideoFramesFlushed == 0 {
		t.Fatalf("expected flushed frame count in fix mode")
	}
}

func makeRTPPacket(seq uint16, ts uint32, payload []byte) []byte {
	packet := make([]byte, 12+len(payload))
	packet[0] = 0x80
	packet[1] = 96
	binary.BigEndian.PutUint16(packet[2:4], seq)
	binary.BigEndian.PutUint32(packet[4:8], ts)
	binary.BigEndian.PutUint32(packet[8:12], 0x11223344)
	copy(packet[12:], payload)
	return packet
}

func mustListenUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatalf("listen udp failed: %v", err)
	}
	return conn
}

func localUDPAddr(conn *net.UDPConn) *net.UDPAddr {
	addr := conn.LocalAddr().(*net.UDPAddr)
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: addr.Port}
}
