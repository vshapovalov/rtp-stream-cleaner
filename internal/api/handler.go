package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"rtp-stream-cleaner/internal/config"
	"rtp-stream-cleaner/internal/session"
)

type Handler struct {
	manager    *session.Manager
	publicIP   string
	internalIP string
}

func NewHandler(cfg config.Config, manager *session.Manager) *Handler {
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
	mux.HandleFunc("/v1/health", h.handleHealth)
	mux.HandleFunc("/v1/session", h.handleSessionCreate)
	mux.HandleFunc("/v1/session/", h.handleSessionByID)
}

type createSessionRequest struct {
	CallID  string `json:"call_id"`
	FromTag string `json:"from_tag"`
	ToTag   string `json:"to_tag"`
	Audio   struct {
		Enable bool `json:"enable"`
	} `json:"audio"`
	Video struct {
		Enable bool `json:"enable"`
		Fix    bool `json:"fix"`
	} `json:"video"`
}

type portResponse struct {
	APort int `json:"a_port"`
	BPort int `json:"b_port"`
}

type mediaStateResponse struct {
	APort         int    `json:"a_port"`
	BPort         int    `json:"b_port"`
	RTPEngineDest string `json:"rtpengine_dest"`
}

type createSessionResponse struct {
	ID         string       `json:"id"`
	PublicIP   string       `json:"public_ip"`
	InternalIP string       `json:"internal_ip"`
	Audio      portResponse `json:"audio"`
	Video      portResponse `json:"video"`
}

type getSessionResponse struct {
	ID         string             `json:"id"`
	CallID     string             `json:"call_id"`
	FromTag    string             `json:"from_tag"`
	ToTag      string             `json:"to_tag"`
	PublicIP   string             `json:"public_ip"`
	InternalIP string             `json:"internal_ip"`
	Audio      mediaStateResponse `json:"audio"`
	Video      mediaStateResponse `json:"video"`
}

type errorResponse struct {
	Error string `json:"error"`
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.publicIP == "" {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "PUBLIC_IP is required"})
		return
	}
	var req createSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid json body"})
		return
	}
	created, err := h.manager.Create(req.CallID, req.FromTag, req.ToTag)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, session.ErrNoPortsAvailable) {
			status = http.StatusServiceUnavailable
		}
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
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleSessionByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/session/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.handleSessionGet(w, r, id)
	case http.MethodDelete:
		h.handleSessionDelete(w, r, id)
	default:
		w.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodDelete}, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleSessionGet(w http.ResponseWriter, r *http.Request, id string) {
	found, ok := h.manager.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}
	resp := getSessionResponse{
		ID:         found.ID,
		CallID:     found.CallID,
		FromTag:    found.FromTag,
		ToTag:      found.ToTag,
		PublicIP:   h.publicIP,
		InternalIP: h.internalIP,
		Audio: mediaStateResponse{
			APort:         found.Audio.APort,
			BPort:         found.Audio.BPort,
			RTPEngineDest: found.Audio.RTPEngineDest,
		},
		Video: mediaStateResponse{
			APort:         found.Video.APort,
			BPort:         found.Video.BPort,
			RTPEngineDest: found.Video.RTPEngineDest,
		},
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleSessionDelete(w http.ResponseWriter, r *http.Request, id string) {
	if deleted := h.manager.Delete(id); !deleted {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: "session not found"})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
