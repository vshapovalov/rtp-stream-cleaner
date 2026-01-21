package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

type Media struct {
	APort         int
	BPort         int
	RTPEngineDest *net.UDPAddr
}

type Session struct {
	ID               string
	CallID           string
	FromTag          string
	ToTag            string
	Audio            Media
	Video            Media
	LastActivity     time.Time
	State            string
	AudioCounters    AudioCounters
	VideoCounters    VideoCounters
	audioProxy       *audioProxy
	audioCounters    audioCounters
	audioDest        atomic.Pointer[net.UDPAddr]
	videoProxy       *videoProxy
	videoCounters    videoCounters
	videoDest        atomic.Pointer[net.UDPAddr]
	lastActivityNsec atomic.Int64
	state            atomic.Int32
}

type Manager struct {
	mu                      sync.Mutex
	sessions                map[string]*Session
	allocator               *PortAllocator
	peerLearningWindow      time.Duration
	maxFrameWait            time.Duration
	idleTimeout             time.Duration
	videoInjectCachedSPSPPS bool
	stopCh                  chan struct{}
	stopOnce                sync.Once
	wg                      sync.WaitGroup
}

func NewManager(allocator *PortAllocator, peerLearningWindow, maxFrameWait, idleTimeout time.Duration, videoInjectCachedSPSPPS bool) *Manager {
	manager := &Manager{
		sessions:                make(map[string]*Session),
		allocator:               allocator,
		peerLearningWindow:      peerLearningWindow,
		maxFrameWait:            maxFrameWait,
		idleTimeout:             idleTimeout,
		videoInjectCachedSPSPPS: videoInjectCachedSPSPPS,
		stopCh:                  make(chan struct{}),
	}
	if idleTimeout > 0 {
		manager.wg.Add(1)
		go manager.reapIdleSessions()
	}
	return manager
}

func (m *Manager) Create(callID, fromTag, toTag string, videoFix bool) (*Session, error) {
	ports, err := m.allocator.Allocate(4)
	if err != nil {
		return nil, err
	}
	session := &Session{
		ID:      m.generateID(),
		CallID:  callID,
		FromTag: fromTag,
		ToTag:   toTag,
		Audio: Media{
			APort: ports[0],
			BPort: ports[1],
		},
		Video: Media{
			APort: ports[2],
			BPort: ports[3],
		},
	}
	session.setState(stateCreated)
	session.setLastActivity(time.Now())
	session.audioDest.Store((*net.UDPAddr)(nil))
	session.videoDest.Store((*net.UDPAddr)(nil))

	aConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: session.Audio.APort})
	if err != nil {
		m.allocator.Release(ports)
		return nil, fmt.Errorf("audio a socket: %w", err)
	}
	bConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: session.Audio.BPort})
	if err != nil {
		_ = aConn.Close()
		m.allocator.Release(ports)
		return nil, fmt.Errorf("audio b socket: %w", err)
	}
	videoAConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: session.Video.APort})
	if err != nil {
		_ = aConn.Close()
		_ = bConn.Close()
		m.allocator.Release(ports)
		return nil, fmt.Errorf("video a socket: %w", err)
	}
	videoBConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: session.Video.BPort})
	if err != nil {
		_ = aConn.Close()
		_ = bConn.Close()
		_ = videoAConn.Close()
		m.allocator.Release(ports)
		return nil, fmt.Errorf("video b socket: %w", err)
	}
	session.audioProxy = newAudioProxy(session, aConn, bConn, m.peerLearningWindow)
	session.videoProxy = newVideoProxy(session, videoAConn, videoBConn, m.peerLearningWindow, m.maxFrameWait, videoFix, m.videoInjectCachedSPSPPS)

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
	return cloneSession(session), nil
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	return cloneSession(session), true
}

func (m *Manager) UpdateRTPDest(id string, audioDest, videoDest *net.UDPAddr) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	if !ok {
		return nil, false
	}
	if audioDest != nil {
		clone := cloneUDPAddr(audioDest)
		session.Audio.RTPEngineDest = clone
		session.audioDest.Store(clone)
	}
	if videoDest != nil {
		clone := cloneUDPAddr(videoDest)
		session.Video.RTPEngineDest = clone
		session.videoDest.Store(clone)
	}
	return cloneSession(session), true
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

func cloneSession(session *Session) *Session {
	if session == nil {
		return nil
	}
	clone := *session
	clone.LastActivity = session.lastActivity()
	clone.State = session.stateString()
	clone.Audio = cloneMedia(session.Audio)
	clone.Video = cloneMedia(session.Video)
	clone.AudioCounters = snapshotAudioCounters(&session.audioCounters)
	clone.VideoCounters = snapshotVideoCounters(&session.videoCounters)
	return &clone
}

func cloneMedia(media Media) Media {
	clone := media
	if media.RTPEngineDest != nil {
		dest := *media.RTPEngineDest
		clone.RTPEngineDest = &dest
	}
	return clone
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	clone := *addr
	return &clone
}

func (m *Manager) Close() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
		m.wg.Wait()
	})
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
			m.removeIdleSessions(time.Now())
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
