package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"rtp-stream-cleaner/internal/logging"
)

type Media struct {
	APort          int
	BPort          int
	RTPEngineDest  *net.UDPAddr
	Enabled        bool
	DisabledReason string
}

type Session struct {
	ID                  string
	CallID              string
	FromTag             string
	ToTag               string
	CreatedAt           time.Time
	Audio               Media
	Video               Media
	LastActivity        time.Time
	State               string
	AudioCounters       AudioCounters
	VideoCounters       VideoCounters
	audioProxy          sessionProxy
	audioCounters       audioCounters
	audioDest           atomic.Pointer[net.UDPAddr]
	audioEnabled        atomic.Bool
	audioDisabledReason atomic.Value
	videoProxy          sessionProxy
	videoCounters       videoCounters
	videoDest           atomic.Pointer[net.UDPAddr]
	videoEnabled        atomic.Bool
	videoDisabledReason atomic.Value
	lastActivityNsec    atomic.Int64
	state               atomic.Int32
}

type Manager struct {
	mu                      sync.Mutex
	sessions                map[string]*Session
	allocator               *PortAllocator
	peerLearningWindow      time.Duration
	maxFrameWait            time.Duration
	idleTimeout             time.Duration
	videoInjectCachedSPSPPS bool
	proxyLogConfig          ProxyLogConfig
	now                     func() time.Time
	listenUDP               func(network string, laddr *net.UDPAddr) (*net.UDPConn, error)
	newAudioProxy           func(session *Session, aConn, bConn *net.UDPConn, peerLearningWindow time.Duration, logConfig ProxyLogConfig) sessionProxy
	newVideoProxy           func(session *Session, aConn, bConn *net.UDPConn, peerLearningWindow, maxFrameWait time.Duration, videoFix bool, inject bool, logConfig ProxyLogConfig) sessionProxy
	stopCh                  chan struct{}
	stopOnce                sync.Once
	wg                      sync.WaitGroup
}

type sessionProxy interface {
	start()
	stop()
}

type managerDeps struct {
	now           func() time.Time
	listenUDP     func(network string, laddr *net.UDPAddr) (*net.UDPConn, error)
	newAudioProxy func(session *Session, aConn, bConn *net.UDPConn, peerLearningWindow time.Duration, logConfig ProxyLogConfig) sessionProxy
	newVideoProxy func(session *Session, aConn, bConn *net.UDPConn, peerLearningWindow, maxFrameWait time.Duration, videoFix bool, inject bool, logConfig ProxyLogConfig) sessionProxy
	startReaper   bool
}

type ProxyLogConfig struct {
	StatsInterval      time.Duration
	PacketLog          bool
	PacketLogSampleN   uint64
	PacketLogOnAnomaly bool
}

func NewManager(allocator *PortAllocator, peerLearningWindow, maxFrameWait, idleTimeout time.Duration, videoInjectCachedSPSPPS bool, logConfig ProxyLogConfig) *Manager {
	return newManagerWithDeps(allocator, peerLearningWindow, maxFrameWait, idleTimeout, videoInjectCachedSPSPPS, logConfig, managerDeps{startReaper: true})
}

func newManagerWithDeps(allocator *PortAllocator, peerLearningWindow, maxFrameWait, idleTimeout time.Duration, videoInjectCachedSPSPPS bool, logConfig ProxyLogConfig, deps managerDeps) *Manager {
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.listenUDP == nil {
		deps.listenUDP = net.ListenUDP
	}
	if deps.newAudioProxy == nil {
		deps.newAudioProxy = func(session *Session, aConn, bConn *net.UDPConn, peerLearningWindow time.Duration, logConfig ProxyLogConfig) sessionProxy {
			return newAudioProxy(session, aConn, bConn, peerLearningWindow, logConfig)
		}
	}
	if deps.newVideoProxy == nil {
		deps.newVideoProxy = func(session *Session, aConn, bConn *net.UDPConn, peerLearningWindow, maxFrameWait time.Duration, videoFix bool, inject bool, logConfig ProxyLogConfig) sessionProxy {
			return newVideoProxy(session, aConn, bConn, peerLearningWindow, maxFrameWait, videoFix, inject, logConfig)
		}
	}
	manager := &Manager{
		sessions:                make(map[string]*Session),
		allocator:               allocator,
		peerLearningWindow:      peerLearningWindow,
		maxFrameWait:            maxFrameWait,
		idleTimeout:             idleTimeout,
		videoInjectCachedSPSPPS: videoInjectCachedSPSPPS,
		proxyLogConfig:          logConfig,
		now:                     deps.now,
		listenUDP:               deps.listenUDP,
		newAudioProxy:           deps.newAudioProxy,
		newVideoProxy:           deps.newVideoProxy,
		stopCh:                  make(chan struct{}),
	}
	if idleTimeout > 0 && deps.startReaper {
		manager.wg.Add(1)
		go manager.reapIdleSessions()
	}
	return manager
}

func (m *Manager) Create(callID, fromTag, toTag string, videoFix bool) (*Session, error) {
	return m.createWithDest(callID, fromTag, toTag, videoFix, nil, nil)
}

func (m *Manager) CreateWithInitialDest(callID, fromTag, toTag string, videoFix bool, initialAudioDest, initialVideoDest *net.UDPAddr) (*Session, error) {
	return m.createWithDest(callID, fromTag, toTag, videoFix, initialAudioDest, initialVideoDest)
}

func (m *Manager) createWithDest(callID, fromTag, toTag string, videoFix bool, initialAudioDest, initialVideoDest *net.UDPAddr) (*Session, error) {
	ports, err := m.allocator.Allocate(4)
	if err != nil {
		return nil, err
	}
	session := &Session{
		ID:        m.generateID(),
		CallID:    callID,
		FromTag:   fromTag,
		ToTag:     toTag,
		CreatedAt: m.now(),
		Audio: Media{
			APort:          ports[0],
			BPort:          ports[1],
			Enabled:        true,
			DisabledReason: "",
		},
		Video: Media{
			APort:          ports[2],
			BPort:          ports[3],
			Enabled:        true,
			DisabledReason: "",
		},
	}
	session.setState(stateCreated)
	session.setLastActivity(m.now())
	session.audioDest.Store((*net.UDPAddr)(nil))
	session.videoDest.Store((*net.UDPAddr)(nil))
	session.audioEnabled.Store(true)
	session.videoEnabled.Store(true)
	session.audioDisabledReason.Store("")
	session.videoDisabledReason.Store("")
	applyRTPDest(session, initialAudioDest, initialVideoDest)

	aConn, err := m.listenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: session.Audio.APort})
	if err != nil {
		logging.WithSessionID(session.ID).Error("session.create failed", "error", err)
		m.allocator.Release(ports)
		return nil, fmt.Errorf("audio a socket: %w", err)
	}
	bConn, err := m.listenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: session.Audio.BPort})
	if err != nil {
		logging.WithSessionID(session.ID).Error("session.create failed", "error", err)
		if aConn != nil {
			_ = aConn.Close()
		}
		m.allocator.Release(ports)
		return nil, fmt.Errorf("audio b socket: %w", err)
	}
	videoAConn, err := m.listenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: session.Video.APort})
	if err != nil {
		logging.WithSessionID(session.ID).Error("session.create failed", "error", err)
		if aConn != nil {
			_ = aConn.Close()
		}
		if bConn != nil {
			_ = bConn.Close()
		}
		m.allocator.Release(ports)
		return nil, fmt.Errorf("video a socket: %w", err)
	}
	videoBConn, err := m.listenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: session.Video.BPort})
	if err != nil {
		logging.WithSessionID(session.ID).Error("session.create failed", "error", err)
		if aConn != nil {
			_ = aConn.Close()
		}
		if bConn != nil {
			_ = bConn.Close()
		}
		if videoAConn != nil {
			_ = videoAConn.Close()
		}
		m.allocator.Release(ports)
		return nil, fmt.Errorf("video b socket: %w", err)
	}
	session.audioProxy = m.newAudioProxy(session, aConn, bConn, m.peerLearningWindow, m.proxyLogConfig)
	session.videoProxy = m.newVideoProxy(session, videoAConn, videoBConn, m.peerLearningWindow, m.maxFrameWait, videoFix, m.videoInjectCachedSPSPPS, m.proxyLogConfig)

	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		if _, exists := m.sessions[session.ID]; !exists {
			break
		}
		session.ID = m.generateID()
	}
	m.sessions[session.ID] = session
	session.audioProxy.start()
	session.videoProxy.start()
	return session, nil
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	return session, true
}

func (m *Manager) UpdateRTPDest(id string, audioDest, videoDest *net.UDPAddr) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	applyRTPDest(session, audioDest, videoDest)
	return session, true
}

func applyRTPDest(session *Session, audioDest, videoDest *net.UDPAddr) {
	if session == nil {
		return
	}
	if audioDest != nil {
		if audioDest.Port == 0 {
			session.Audio.RTPEngineDest = nil
			session.Audio.Enabled = false
			session.Audio.DisabledReason = "rtpengine_port_0"
			session.audioEnabled.Store(false)
			session.audioDisabledReason.Store("rtpengine_port_0")
			session.audioDest.Store((*net.UDPAddr)(nil))
		} else {
			clone := cloneUDPAddr(audioDest)
			session.Audio.RTPEngineDest = clone
			session.Audio.Enabled = true
			session.Audio.DisabledReason = ""
			session.audioEnabled.Store(true)
			session.audioDisabledReason.Store("")
			session.audioDest.Store(clone)
		}
	}
	if videoDest != nil {
		if videoDest.Port == 0 {
			session.Video.RTPEngineDest = nil
			session.Video.Enabled = false
			session.Video.DisabledReason = "rtpengine_port_0"
			session.videoEnabled.Store(false)
			session.videoDisabledReason.Store("rtpengine_port_0")
			session.videoDest.Store((*net.UDPAddr)(nil))
		} else {
			clone := cloneUDPAddr(videoDest)
			session.Video.RTPEngineDest = clone
			session.Video.Enabled = true
			session.Video.DisabledReason = ""
			session.videoEnabled.Store(true)
			session.videoDisabledReason.Store("")
			session.videoDest.Store(clone)
		}
	}
}

func (m *Manager) Delete(id string) bool {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if ok {
		session.setState(stateClosing)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	m.stopSession(session)
	return true
}

func (m *Manager) generateID() string {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("S-%d", time.Now().UnixNano())
	}
	return "S-" + hex.EncodeToString(buffer)
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	clone := *addr
	return &clone
}

func loadAtomicString(value *atomic.Value) string {
	if value == nil {
		return ""
	}
	loaded := value.Load()
	if loaded == nil {
		return ""
	}
	parsed, ok := loaded.(string)
	if !ok {
		return ""
	}
	return parsed
}

func (m *Manager) Close() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
		m.wg.Wait()
	})
}

func (m *Manager) Cleanup(now time.Time) {
	m.removeIdleSessions(now)
}

func (m *Manager) reapIdleSessions() {
	defer m.wg.Done()
	interval := m.idleTimeout / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.removeIdleSessions(m.now())
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) removeIdleSessions(now time.Time) {
	if m.idleTimeout <= 0 {
		return
	}
	var expired []*Session
	m.mu.Lock()
	for id, session := range m.sessions {
		last := session.lastActivity()
		if last.IsZero() {
			last = now
		}
		if now.Sub(last) >= m.idleTimeout {
			session.setState(stateClosing)
			delete(m.sessions, id)
			expired = append(expired, session)
		}
	}
	m.mu.Unlock()
	for _, session := range expired {
		m.stopSession(session)
	}
}

func (m *Manager) stopSession(session *Session) {
	if session == nil {
		return
	}
	if session.audioProxy != nil {
		session.audioProxy.stop()
	}
	if session.videoProxy != nil {
		session.videoProxy.stop()
	}
	m.allocator.Release([]int{session.Audio.APort, session.Audio.BPort, session.Video.APort, session.Video.BPort})
}

type sessionState int32

const (
	stateCreated sessionState = iota
	stateActive
	stateClosing
)

func (s sessionState) String() string {
	switch s {
	case stateCreated:
		return "created"
	case stateActive:
		return "active"
	case stateClosing:
		return "closing"
	default:
		return "created"
	}
}

func (s *Session) setState(state sessionState) {
	s.state.Store(int32(state))
}

func (s *Session) stateString() string {
	return sessionState(s.state.Load()).String()
}

func (s *Session) setLastActivity(now time.Time) {
	s.lastActivityNsec.Store(now.UnixNano())
}

func (s *Session) lastActivity() time.Time {
	nsec := s.lastActivityNsec.Load()
	if nsec == 0 {
		return time.Time{}
	}
	return time.Unix(0, nsec).UTC()
}

func (s *Session) markActivity(now time.Time) {
	s.lastActivityNsec.Store(now.UnixNano())
	s.state.CompareAndSwap(int32(stateCreated), int32(stateActive))
}
