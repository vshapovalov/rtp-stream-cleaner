package session

import "time"

func (s *Session) AudioState() Media {
	if s == nil {
		return Media{}
	}
	return Media{
		APort:          s.Audio.APort,
		BPort:          s.Audio.BPort,
		RTPEngineDest:  cloneUDPAddr(s.audioDest.Load()),
		Enabled:        s.audioEnabled.Load(),
		DisabledReason: loadAtomicString(&s.audioDisabledReason),
	}
}

func (s *Session) VideoState() Media {
	if s == nil {
		return Media{}
	}
	return Media{
		APort:          s.Video.APort,
		BPort:          s.Video.BPort,
		RTPEngineDest:  cloneUDPAddr(s.videoDest.Load()),
		Enabled:        s.videoEnabled.Load(),
		DisabledReason: loadAtomicString(&s.videoDisabledReason),
	}
}

func (s *Session) AudioCountersSnapshot() AudioCounters {
	if s == nil {
		return AudioCounters{}
	}
	return snapshotAudioCounters(&s.audioCounters)
}

func (s *Session) VideoCountersSnapshot() VideoCounters {
	if s == nil {
		return VideoCounters{}
	}
	return snapshotVideoCounters(&s.videoCounters)
}

func (s *Session) LastActivityTime() time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.lastActivity()
}

func (s *Session) StateString() string {
	if s == nil {
		return ""
	}
	return s.stateString()
}
