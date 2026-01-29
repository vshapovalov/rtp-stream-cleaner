package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"rtp-stream-cleaner/internal/config"
	"rtp-stream-cleaner/internal/logging"
	"rtp-stream-cleaner/internal/session"
)

type SessionManager interface {
	Create(callID, fromTag, toTag string, videoFix bool) (*session.Session, error)
	CreateWithInitialDest(callID, fromTag, toTag string, videoFix bool, initialAudioDest, initialVideoDest *net.UDPAddr) (*session.Session, error)
	Get(id string) (*session.Session, bool)
	UpdateRTPDest(id string, audioDest, videoDest *net.UDPAddr) (*session.Session, bool)
	Delete(id string) bool
}

type Handler struct {
	manager    SessionManager
	publicIP   string
	internalIP string
}

func NewHandler(cfg config.Config, manager SessionManager) *Handler {
	internalIP := cfg.InternalIP
	if internalIP == "" {
		internalIP = cfg.PublicIP
	}
	return &Handler{
		manager:    manager,
		publicIP:   cfg.PublicIP,
		internalIP: internalIP,
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/health", h.handleHealth)
	mux.HandleFunc("POST /v1/session", h.handleSessionCreate)
	mux.HandleFunc("GET /v1/session/{id}", h.handleSessionGetByID)
	mux.HandleFunc("DELETE /v1/session/{id}", h.handleSessionDeleteByID)
	mux.HandleFunc("POST /v1/session/{id}/update", h.handleSessionUpdateByID)
	mux.HandleFunc("POST /v1/session/{id}/delete", h.handleSessionDeleteByID)
}

type createSessionRequest struct {
	CallID  string `json:"call_id"`
	FromTag string `json:"from_tag"`
	ToTag   string `json:"to_tag"`
	Audio   struct {
		Enable        bool    `json:"enable"`
		RTPEngineDest *string `json:"rtpengine_dest"`
	} `json:"audio"`
	Video struct {
		Enable        bool    `json:"enable"`
		Fix           *bool   `json:"fix"`
		RTPEngineDest *string `json:"rtpengine_dest"`
	} `json:"video"`
}

type updateSessionRequest struct {
	Audio *updateMediaRequest `json:"audio"`
	Video *updateMediaRequest `json:"video"`
}

type updateMediaRequest struct {
	RTPEngineDest *string `json:"rtpengine_dest"`
}

type portResponse struct {
	APort int `json:"a_port"`
	BPort int `json:"b_port"`
}

type mediaStateResponse struct {
	APort          int    `json:"a_port"`
	BPort          int    `json:"b_port"`
	RTPEngineDest  string `json:"rtpengine_dest"`
	Enabled        bool   `json:"enabled"`
	DisabledReason string `json:"disabled_reason,omitempty"`
}

type createSessionResponse struct {
	ID         string       `json:"id"`
	PublicIP   string       `json:"public_ip"`
	InternalIP string       `json:"internal_ip"`
	Audio      portResponse `json:"audio"`
	Video      portResponse `json:"video"`
}

type getSessionResponse struct {
	ID                 string             `json:"id"`
	CallID             string             `json:"call_id"`
	FromTag            string             `json:"from_tag"`
	ToTag              string             `json:"to_tag"`
	PublicIP           string             `json:"public_ip"`
	InternalIP         string             `json:"internal_ip"`
	Audio              mediaStateResponse `json:"audio"`
	Video              mediaStateResponse `json:"video"`
	AudioAInPkts       uint64             `json:"audio_a_in_pkts"`
	AudioAInBytes      uint64             `json:"audio_a_in_bytes"`
	AudioBOutPkts      uint64             `json:"audio_b_out_pkts"`
	AudioBOutBytes     uint64             `json:"audio_b_out_bytes"`
	AudioBInPkts       uint64             `json:"audio_b_in_pkts"`
	AudioBInBytes      uint64             `json:"audio_b_in_bytes"`
	AudioAOutPkts      uint64             `json:"audio_a_out_pkts"`
	AudioAOutBytes     uint64             `json:"audio_a_out_bytes"`
	VideoAInPkts       uint64             `json:"video_a_in_pkts"`
	VideoAInBytes      uint64             `json:"video_a_in_bytes"`
	VideoBOutPkts      uint64             `json:"video_b_out_pkts"`
	VideoBOutBytes     uint64             `json:"video_b_out_bytes"`
	VideoBInPkts       uint64             `json:"video_b_in_pkts"`
	VideoBInBytes      uint64             `json:"video_b_in_bytes"`
	VideoAOutPkts      uint64             `json:"video_a_out_pkts"`
	VideoAOutBytes     uint64             `json:"video_a_out_bytes"`
	VideoFramesStarted uint64             `json:"video_frames_started"`
	VideoFramesEnded   uint64             `json:"video_frames_ended"`
	VideoFramesFlushed uint64             `json:"video_frames_flushed"`
	VideoForcedFlushes uint64             `json:"video_forced_flushes"`
	VideoInjectedSPS   uint64             `json:"video_injected_sps"`
	VideoInjectedPPS   uint64             `json:"video_injected_pps"`
	VideoSeqDelta      uint64             `json:"video_seq_delta_current"`
	LastActivity       string             `json:"last_activity"`
	State              string             `json:"state"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	if h.publicIP == "" {
		logging.L().Warn("session.create failed", "error", "PUBLIC_IP is required")
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "PUBLIC_IP is required"})
		return
	}
	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logging.L().Warn("session.create failed", "error", err)
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid json body"})
		return
	}
	if req.CallID == "" || req.FromTag == "" || req.ToTag == "" {
		logging.L().Warn("session.create failed", "error", "call_id, from_tag, and to_tag are required")
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "call_id, from_tag, and to_tag are required"})
		return
	}
	// Default to true when omitted to preserve legacy behavior (video fix enabled).
	videoFix := true
	if req.Video.Fix != nil {
		videoFix = *req.Video.Fix
	}
	var audioDest *net.UDPAddr
	if req.Audio.RTPEngineDest != nil {
		parsed, err := parseDest(*req.Audio.RTPEngineDest)
		if err != nil {
			logging.L().Warn("session.create failed", "error", err, "field", "audio.rtpengine_dest")
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("audio rtpengine_dest %s", err)})
			return
		}
		audioDest = parsed
	}
	var videoDest *net.UDPAddr
	if req.Video.RTPEngineDest != nil {
		parsed, err := parseDest(*req.Video.RTPEngineDest)
		if err != nil {
			logging.L().Warn("session.create failed", "error", err, "field", "video.rtpengine_dest")
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("video rtpengine_dest %s", err)})
			return
		}
		videoDest = parsed
	}
	var (
		created *session.Session
		err     error
	)
	if audioDest != nil || videoDest != nil {
		created, err = h.manager.CreateWithInitialDest(req.CallID, req.FromTag, req.ToTag, videoFix, audioDest, videoDest)
	} else {
		created, err = h.manager.Create(req.CallID, req.FromTag, req.ToTag, videoFix)
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, session.ErrNoPortsAvailable) {
			status = http.StatusServiceUnavailable
		}
		logging.L().Error("session.create failed", "error", err, "call_id", req.CallID, "from_tag", req.FromTag, "to_tag", req.ToTag)
		writeJSON(w, status, errorResponse{Error: err.Error()})
		return
	}
	resp := createSessionResponse{
		ID:         created.ID,
		PublicIP:   h.publicIP,
		InternalIP: h.internalIP,
		Audio: portResponse{
			APort: created.Audio.APort,
			BPort: created.Audio.BPort,
		},
		Video: portResponse{
			APort: created.Video.APort,
			BPort: created.Video.BPort,
		},
	}
	logging.WithSessionID(created.ID).Info(
		"session.create",
		"call_id",
		created.CallID,
		"from_tag",
		created.FromTag,
		"to_tag",
		created.ToTag,
		"audio_enabled",
		req.Audio.Enable,
		"video_enabled",
		req.Video.Enable,
		"video_fix",
		videoFix,
		"audio_a_port",
		created.Audio.APort,
		"audio_b_port",
		created.Audio.BPort,
		"video_a_port",
		created.Video.APort,
		"video_b_port",
		created.Video.BPort,
	)
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleSessionGetByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	h.handleSessionGet(w, r, id)
}

func (h *Handler) handleSessionUpdateByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	h.handleSessionUpdate(w, r, id)
}

func (h *Handler) handleSessionDeleteByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	h.handleSessionDelete(w, r, id)
}

func (h *Handler) handleSessionGet(w http.ResponseWriter, r *http.Request, id string) {
	found, ok := h.manager.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}
	resp := getSessionResponse{
		ID:                 found.ID,
		CallID:             found.CallID,
		FromTag:            found.FromTag,
		ToTag:              found.ToTag,
		PublicIP:           h.publicIP,
		InternalIP:         h.internalIP,
		AudioAInPkts:       found.AudioCounters.AInPkts,
		AudioAInBytes:      found.AudioCounters.AInBytes,
		AudioBOutPkts:      found.AudioCounters.BOutPkts,
		AudioBOutBytes:     found.AudioCounters.BOutBytes,
		AudioBInPkts:       found.AudioCounters.BInPkts,
		AudioBInBytes:      found.AudioCounters.BInBytes,
		AudioAOutPkts:      found.AudioCounters.AOutPkts,
		AudioAOutBytes:     found.AudioCounters.AOutBytes,
		VideoAInPkts:       found.VideoCounters.AInPkts,
		VideoAInBytes:      found.VideoCounters.AInBytes,
		VideoBOutPkts:      found.VideoCounters.BOutPkts,
		VideoBOutBytes:     found.VideoCounters.BOutBytes,
		VideoBInPkts:       found.VideoCounters.BInPkts,
		VideoBInBytes:      found.VideoCounters.BInBytes,
		VideoAOutPkts:      found.VideoCounters.AOutPkts,
		VideoAOutBytes:     found.VideoCounters.AOutBytes,
		VideoFramesStarted: found.VideoCounters.VideoFramesStarted,
		VideoFramesEnded:   found.VideoCounters.VideoFramesEnded,
		VideoFramesFlushed: found.VideoCounters.VideoFramesFlushed,
		VideoForcedFlushes: found.VideoCounters.VideoForcedFlushes,
		VideoInjectedSPS:   found.VideoCounters.VideoInjectedSPS,
		VideoInjectedPPS:   found.VideoCounters.VideoInjectedPPS,
		VideoSeqDelta:      found.VideoCounters.VideoSeqDelta,
		LastActivity:       formatTime(found.LastActivity),
		State:              found.State,
		Audio: mediaStateResponse{
			APort:          found.Audio.APort,
			BPort:          found.Audio.BPort,
			RTPEngineDest:  formatDest(found.Audio.RTPEngineDest),
			Enabled:        found.Audio.Enabled,
			DisabledReason: found.Audio.DisabledReason,
		},
		Video: mediaStateResponse{
			APort:          found.Video.APort,
			BPort:          found.Video.BPort,
			RTPEngineDest:  formatDest(found.Video.RTPEngineDest),
			Enabled:        found.Video.Enabled,
			DisabledReason: found.Video.DisabledReason,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleSessionUpdate(w http.ResponseWriter, r *http.Request, id string) {
	var req updateSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logging.WithSessionID(id).Warn("session.update failed", "error", err)
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid json body"})
		return
	}
	var audioDest *net.UDPAddr
	if req.Audio != nil && req.Audio.RTPEngineDest != nil {
		parsed, err := parseDest(*req.Audio.RTPEngineDest)
		if err != nil {
			logging.WithSessionID(id).Warn("session.update failed", "error", err, "field", "audio.rtpengine_dest")
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("audio rtpengine_dest %s", err)})
			return
		}
		audioDest = parsed
	}
	var videoDest *net.UDPAddr
	if req.Video != nil && req.Video.RTPEngineDest != nil {
		parsed, err := parseDest(*req.Video.RTPEngineDest)
		if err != nil {
			logging.WithSessionID(id).Warn("session.update failed", "error", err, "field", "video.rtpengine_dest")
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: fmt.Sprintf("video rtpengine_dest %s", err)})
			return
		}
		videoDest = parsed
	}
	updated, ok := h.manager.UpdateRTPDest(id, audioDest, videoDest)
	if !ok {
		logging.WithSessionID(id).Warn("session.update failed", "error", "session not found")
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}
	resp := getSessionResponse{
		ID:                 updated.ID,
		CallID:             updated.CallID,
		FromTag:            updated.FromTag,
		ToTag:              updated.ToTag,
		PublicIP:           h.publicIP,
		InternalIP:         h.internalIP,
		AudioAInPkts:       updated.AudioCounters.AInPkts,
		AudioAInBytes:      updated.AudioCounters.AInBytes,
		AudioBOutPkts:      updated.AudioCounters.BOutPkts,
		AudioBOutBytes:     updated.AudioCounters.BOutBytes,
		AudioBInPkts:       updated.AudioCounters.BInPkts,
		AudioBInBytes:      updated.AudioCounters.BInBytes,
		AudioAOutPkts:      updated.AudioCounters.AOutPkts,
		AudioAOutBytes:     updated.AudioCounters.AOutBytes,
		VideoAInPkts:       updated.VideoCounters.AInPkts,
		VideoAInBytes:      updated.VideoCounters.AInBytes,
		VideoBOutPkts:      updated.VideoCounters.BOutPkts,
		VideoBOutBytes:     updated.VideoCounters.BOutBytes,
		VideoBInPkts:       updated.VideoCounters.BInPkts,
		VideoBInBytes:      updated.VideoCounters.BInBytes,
		VideoAOutPkts:      updated.VideoCounters.AOutPkts,
		VideoAOutBytes:     updated.VideoCounters.AOutBytes,
		VideoFramesStarted: updated.VideoCounters.VideoFramesStarted,
		VideoFramesEnded:   updated.VideoCounters.VideoFramesEnded,
		VideoFramesFlushed: updated.VideoCounters.VideoFramesFlushed,
		VideoForcedFlushes: updated.VideoCounters.VideoForcedFlushes,
		VideoInjectedSPS:   updated.VideoCounters.VideoInjectedSPS,
		VideoInjectedPPS:   updated.VideoCounters.VideoInjectedPPS,
		VideoSeqDelta:      updated.VideoCounters.VideoSeqDelta,
		LastActivity:       formatTime(updated.LastActivity),
		State:              updated.State,
		Audio: mediaStateResponse{
			APort:          updated.Audio.APort,
			BPort:          updated.Audio.BPort,
			RTPEngineDest:  formatDest(updated.Audio.RTPEngineDest),
			Enabled:        updated.Audio.Enabled,
			DisabledReason: updated.Audio.DisabledReason,
		},
		Video: mediaStateResponse{
			APort:          updated.Video.APort,
			BPort:          updated.Video.BPort,
			RTPEngineDest:  formatDest(updated.Video.RTPEngineDest),
			Enabled:        updated.Video.Enabled,
			DisabledReason: updated.Video.DisabledReason,
		},
	}
	logAttrs := []any{}
	if audioDest != nil {
		logAttrs = append(logAttrs, "audio_dest", audioDest.String())
	}
	if videoDest != nil {
		logAttrs = append(logAttrs, "video_dest", videoDest.String())
	}
	logging.WithSessionID(id).Info("session.update", logAttrs...)
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleSessionDelete(w http.ResponseWriter, r *http.Request, id string) {
	var duration time.Duration
	if found, ok := h.manager.Get(id); ok && !found.CreatedAt.IsZero() {
		duration = time.Since(found.CreatedAt)
	}
	if deleted := h.manager.Delete(id); !deleted {
		logging.WithSessionID(id).Warn("session.delete failed", "error", "session not found")
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}
	logAttrs := []any{"reason", "api"}
	if duration > 0 {
		logAttrs = append(logAttrs, "duration", duration)
	}
	logging.WithSessionID(id).Info("session.delete", logAttrs...)
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func parseDest(raw string) (*net.UDPAddr, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return nil, fmt.Errorf("must be in ip:port format with port 0..65535 (0 disables media)")
	}
	if net.ParseIP(host) == nil {
		return nil, fmt.Errorf("must be in ip:port format with port 0..65535 (0 disables media)")
	}
	portValue, err := strconv.Atoi(port)
	if err != nil || portValue < 0 || portValue > 65535 {
		return nil, fmt.Errorf("must be in ip:port format with port 0..65535 (0 disables media)")
	}
	return &net.UDPAddr{IP: net.ParseIP(host), Port: portValue}, nil
}

func formatDest(addr *net.UDPAddr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
