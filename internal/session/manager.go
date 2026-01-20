package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"time"
)

type Media struct {
	APort         int
	BPort         int
	RTPEngineDest *net.UDPAddr
}

type Session struct {
	ID      string
	CallID  string
	FromTag string
	ToTag   string
	Audio   Media
	Video   Media
}

type Manager struct {
	mu        sync.Mutex
	sessions  map[string]*Session
	allocator *PortAllocator
}

func NewManager(allocator *PortAllocator) *Manager {
	return &Manager{
		sessions:  make(map[string]*Session),
		allocator: allocator,
	}
}

func (m *Manager) Create(callID, fromTag, toTag string) (*Session, error) {
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

	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		if _, exists := m.sessions[session.ID]; !exists {
			break
		}
		session.ID = m.generateID()
	}
	m.sessions[session.ID] = session
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
		session.Audio.RTPEngineDest = cloneUDPAddr(audioDest)
	}
	if videoDest != nil {
		session.Video.RTPEngineDest = cloneUDPAddr(videoDest)
	}
	return cloneSession(session), true
}

func (m *Manager) Delete(id string) bool {
	m.mu.Lock()
	session, ok := m.sessions[id]
	if ok {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	if !ok {
		return false
	}
	m.allocator.Release([]int{
		session.Audio.APort,
		session.Audio.BPort,
		session.Video.APort,
		session.Video.BPort,
	})
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
	clone.Audio = cloneMedia(session.Audio)
	clone.Video = cloneMedia(session.Video)
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
