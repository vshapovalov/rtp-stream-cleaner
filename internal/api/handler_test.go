package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"rtp-stream-cleaner/internal/config"
	"rtp-stream-cleaner/internal/session"
)

type mockManager struct {
	createCalls int
	createInput struct {
		callID   string
		fromTag  string
		toTag    string
		videoFix bool
	}
	createResult *session.Session
	createErr    error

	createWithDestCalls int
	createWithDestInput struct {
		callID           string
		fromTag          string
		toTag            string
		videoFix         bool
		initialAudioDest *net.UDPAddr
		initialVideoDest *net.UDPAddr
	}
	createWithDestResult *session.Session
	createWithDestErr    error

	updateCalls int
	updateInput struct {
		id        string
		audioDest *net.UDPAddr
		videoDest *net.UDPAddr
	}
	updateResult *session.Session
	updateOK     bool

	deleteCalls int
	deleteID    string
	deleteOK    bool
}

func (m *mockManager) Create(callID, fromTag, toTag string, videoFix bool) (*session.Session, error) {
	m.createCalls++
	m.createInput.callID = callID
	m.createInput.fromTag = fromTag
	m.createInput.toTag = toTag
	m.createInput.videoFix = videoFix
	return m.createResult, m.createErr
}

func (m *mockManager) CreateWithInitialDest(callID, fromTag, toTag string, videoFix bool, initialAudioDest, initialVideoDest *net.UDPAddr) (*session.Session, error) {
	m.createWithDestCalls++
	m.createWithDestInput.callID = callID
	m.createWithDestInput.fromTag = fromTag
	m.createWithDestInput.toTag = toTag
	m.createWithDestInput.videoFix = videoFix
	m.createWithDestInput.initialAudioDest = initialAudioDest
	m.createWithDestInput.initialVideoDest = initialVideoDest
	return m.createWithDestResult, m.createWithDestErr
}

func (m *mockManager) Get(id string) (*session.Session, bool) {
	return nil, false
}

func (m *mockManager) UpdateRTPDest(id string, audioDest, videoDest *net.UDPAddr) (*session.Session, bool) {
	m.updateCalls++
	m.updateInput.id = id
	m.updateInput.audioDest = audioDest
	m.updateInput.videoDest = videoDest
	return m.updateResult, m.updateOK
}

func (m *mockManager) Delete(id string) bool {
	m.deleteCalls++
	m.deleteID = id
	return m.deleteOK
}

func newTestHandler(manager SessionManager) *Handler {
	cfg := config.Config{PublicIP: "203.0.113.1", InternalIP: "10.0.0.1", ServicePassword: "test-password"}
	return NewHandler(cfg, manager)
}

func performRequest(handler *Handler, method, path string, body io.Reader) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	handler.Register(mux)
	if path == "" {
		path = "/"
	}
	separator := "?"
	if bytes.Contains([]byte(path), []byte("?")) {
		separator = "&"
	}
	path = path + separator + "access_token=test-password"
	req := httptest.NewRequest(method, path, body)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)
	return recorder
}

func TestAPI_AccessTokenAuth_CorrectToken_AllowsRequest(t *testing.T) {
	manager := &mockManager{}
	handler := newTestHandler(manager)

	recorder := performRequest(handler, http.MethodGet, "/v1/health", nil)

	if recorder.Code == http.StatusUnauthorized {
		t.Fatalf("expected non-401 status, got %d", recorder.Code)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
}

func TestAPI_AccessTokenAuth_WrongToken_401(t *testing.T) {
	manager := &mockManager{}
	handler := newTestHandler(manager)

	mux := http.NewServeMux()
	handler.Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/v1/health?access_token=wrong", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, recorder.Code)
	}
}

func TestAPI_AccessTokenAuth_MissingToken_401(t *testing.T) {
	manager := &mockManager{}
	handler := newTestHandler(manager)

	mux := http.NewServeMux()
	handler.Register(mux)
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, recorder.Code)
	}
}

// TestAPI_CreateSession_BadJSON_400 verifies that the create-session handler
// rejects malformed JSON with a 400 status and does not invoke the manager.
// This matters because clients must receive clear validation errors and the
// service must not create sessions from corrupted input. Preconditions: a
// handler with a configured public IP and a mock manager. Inputs: HTTP POST
// with an invalid JSON payload. Edge case: the JSON decoder fails before any
// field validation. The expected output is HTTP 400 and zero manager Create
// calls. Assertions are stable because json.Decoder deterministically fails on
// invalid syntax. Flakiness is avoided by using httptest without network or
// timers. A regression would show a non-400 status or an unexpected manager
// invocation on invalid JSON.
func TestAPI_CreateSession_BadJSON_400(t *testing.T) {
	manager := &mockManager{}
	handler := newTestHandler(manager)

	recorder := performRequest(handler, http.MethodPost, "/v1/session", bytes.NewBufferString("{bad"))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, recorder.Code)
	}
	if manager.createCalls != 0 {
		t.Fatalf("expected Create not to be called")
	}
}

// TestAPI_CreateSession_MissingFields_400 ensures that required identifiers
// (call_id, from_tag, to_tag) are validated and missing values return HTTP 400.
// This matters because the manager requires these identifiers to track sessions
// correctly. Preconditions: handler with public IP set and a mock manager.
// Inputs: HTTP POST JSON missing call_id. Edge case: other fields present but
// one required field empty. The expected output is HTTP 400 with no manager
// Create calls, which is stable because validation is a deterministic string
// check. Flakiness is avoided by using httptest without concurrency. A
// regression would call Create despite missing fields or return a non-400 code.
func TestAPI_CreateSession_MissingFields_400(t *testing.T) {
	manager := &mockManager{}
	handler := newTestHandler(manager)

	payload := map[string]string{
		"from_tag": "from",
		"to_tag":   "to",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	recorder := performRequest(handler, http.MethodPost, "/v1/session", bytes.NewBuffer(body))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, recorder.Code)
	}
	if manager.createCalls != 0 {
		t.Fatalf("expected Create not to be called")
	}
}

// TestAPI_CreateSession_WithAudioInitialDest verifies that the create-session
// handler forwards an optional audio rtpengine_dest when supplied. This matters
// because callers should be able to set the initial destination without a
// follow-up update request. Preconditions: handler with a mock manager.
// Inputs: POST payload with audio rtpengine_dest and required identifiers.
// Edge case: video rtpengine_dest omitted. The expected output is HTTP 200 and
// a CreateWithInitialDest call carrying only the audio destination. Assertions
// are stable because parseDest deterministically parses the address. Flakiness
// is avoided by using httptest without timers. A regression would call Create
// or pass a non-nil video destination.
func TestAPI_CreateSession_WithAudioInitialDest(t *testing.T) {
	manager := &mockManager{}
	manager.createWithDestResult = &session.Session{
		ID:      "sess-audio-dest",
		CallID:  "call-audio",
		FromTag: "from-audio",
		ToTag:   "to-audio",
		Audio:   session.Media{APort: 13000, BPort: 13001},
		Video:   session.Media{APort: 13002, BPort: 13003},
	}
	handler := newTestHandler(manager)

	payload := map[string]any{
		"call_id":  "call-audio",
		"from_tag": "from-audio",
		"to_tag":   "to-audio",
		"audio": map[string]any{
			"enable":         true,
			"rtpengine_dest": "192.0.2.30:40100",
		},
		"video": map[string]any{
			"enable": true,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	recorder := performRequest(handler, http.MethodPost, "/v1/session", bytes.NewBuffer(body))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if manager.createWithDestCalls != 1 {
		t.Fatalf("expected CreateWithInitialDest to be called once")
	}
	if manager.createCalls != 0 {
		t.Fatalf("expected Create not to be called")
	}
	if manager.createWithDestInput.initialAudioDest == nil {
		t.Fatalf("expected initial audio dest to be set")
	}
	if manager.createWithDestInput.initialAudioDest.Port != 40100 {
		t.Fatalf("expected audio dest port 40100, got %d", manager.createWithDestInput.initialAudioDest.Port)
	}
	if manager.createWithDestInput.initialVideoDest != nil {
		t.Fatalf("expected initial video dest to be nil")
	}
}

// TestAPI_CreateSession_AllowsVideoPortZero verifies that create accepts a
// video rtpengine_dest with port 0 to disable media on creation. This matters
// because SDP can signal disabled video and the API must accept it without a
// separate update call. Preconditions: handler with a mock manager. Inputs:
// POST payload with video rtpengine_dest 0.0.0.0:0 and required identifiers.
// Edge case: audio destination omitted. The expected output is HTTP 200 and a
// CreateWithInitialDest call carrying a video destination with port 0.
// Assertions are stable because parseDest deterministically handles port 0.
// Flakiness is avoided by using httptest without concurrency. A regression
// would return HTTP 400 or pass a non-zero port.
func TestAPI_CreateSession_AllowsVideoPortZero(t *testing.T) {
	manager := &mockManager{}
	manager.createWithDestResult = &session.Session{
		ID:      "sess-video-zero",
		CallID:  "call-video",
		FromTag: "from-video",
		ToTag:   "to-video",
		Audio:   session.Media{APort: 14000, BPort: 14001},
		Video:   session.Media{APort: 14002, BPort: 14003},
	}
	handler := newTestHandler(manager)

	payload := map[string]any{
		"call_id":  "call-video",
		"from_tag": "from-video",
		"to_tag":   "to-video",
		"audio": map[string]any{
			"enable": true,
		},
		"video": map[string]any{
			"enable":         true,
			"rtpengine_dest": "0.0.0.0:0",
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	recorder := performRequest(handler, http.MethodPost, "/v1/session", bytes.NewBuffer(body))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if manager.createWithDestCalls != 1 {
		t.Fatalf("expected CreateWithInitialDest to be called once")
	}
	if manager.createWithDestInput.initialVideoDest == nil {
		t.Fatalf("expected initial video dest to be set")
	}
	if manager.createWithDestInput.initialVideoDest.Port != 0 {
		t.Fatalf("expected initial video dest port 0, got %d", manager.createWithDestInput.initialVideoDest.Port)
	}
	if manager.createWithDestInput.initialAudioDest != nil {
		t.Fatalf("expected initial audio dest to be nil")
	}
}

// TestAPI_UpdateSession_UnknownID_404 verifies that updating a non-existent
// session returns HTTP 404 and does not falsely succeed. This matters so clients
// can detect stale IDs and retry appropriately. Preconditions: handler with a
// mock manager that reports missing sessions. Inputs: POST to the update route
// with a valid rtpengine_dest. Edge case: valid JSON but unknown ID. The
// expected output is HTTP 404 and exactly one UpdateRTPDest call. Assertions are
// stable because the manager's response is deterministic. Flakiness is avoided
// by using httptest and no time-based logic. A regression would return 200 or
// another status for unknown sessions.
func TestAPI_UpdateSession_UnknownID_404(t *testing.T) {
	manager := &mockManager{updateOK: false}
	handler := newTestHandler(manager)

	payload := map[string]map[string]string{
		"audio": {"rtpengine_dest": "192.0.2.10:9000"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	recorder := performRequest(handler, http.MethodPost, "/v1/session/unknown/update", bytes.NewBuffer(body))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, recorder.Code)
	}
	if manager.updateCalls != 1 {
		t.Fatalf("expected UpdateRTPDest to be called once")
	}
}

// TestAPI_UpdateSession_PartialUpdate_CallsManagerCorrectly ensures that the
// update handler forwards only the media destinations present in the request,
// leaving the other media destination nil. This matters because a partial update
// should not overwrite the other leg's destination. Preconditions: handler with
// a mock manager that echoes a valid session. Inputs: two POST requests: one
// with only audio rtpengine_dest and one with only video rtpengine_dest. Edge
// cases: nil audio or video destinations must remain nil in the manager call.
// Expected output: HTTP 200 for both requests with UpdateRTPDest receiving a
// non-nil address only for the specified media. Assertions are stable because
// parseDest is deterministic and mock captures exact arguments. Flakiness is
// avoided by using httptest and no time-based logic. A regression would pass
// non-nil destinations for omitted media or swap audio/video values.
func TestAPI_UpdateSession_PartialUpdate_CallsManagerCorrectly(t *testing.T) {
	t.Run("audio-only", func(t *testing.T) {
		manager := &mockManager{updateOK: true}
		manager.updateResult = &session.Session{
			ID:      "sess-a",
			CallID:  "call-a",
			FromTag: "from-a",
			ToTag:   "to-a",
			Audio:   session.Media{APort: 10000, BPort: 10001},
			Video:   session.Media{APort: 10002, BPort: 10003},
		}
		handler := newTestHandler(manager)

		payload := map[string]map[string]string{
			"audio": {"rtpengine_dest": "192.0.2.11:9000"},
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("unexpected marshal error: %v", err)
		}
		recorder := performRequest(handler, http.MethodPost, "/v1/session/sess-a/update", bytes.NewBuffer(body))

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
		}
		if manager.updateInput.audioDest == nil {
			t.Fatalf("expected audio destination to be set")
		}
		if manager.updateInput.videoDest != nil {
			t.Fatalf("expected video destination to be nil for audio-only update")
		}
	})

	t.Run("video-only", func(t *testing.T) {
		manager := &mockManager{updateOK: true}
		manager.updateResult = &session.Session{
			ID:      "sess-v",
			CallID:  "call-v",
			FromTag: "from-v",
			ToTag:   "to-v",
			Audio:   session.Media{APort: 11000, BPort: 11001},
			Video:   session.Media{APort: 11002, BPort: 11003},
		}
		handler := newTestHandler(manager)

		payload := map[string]map[string]string{
			"video": {"rtpengine_dest": "192.0.2.12:9002"},
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("unexpected marshal error: %v", err)
		}
		recorder := performRequest(handler, http.MethodPost, "/v1/session/sess-v/update", bytes.NewBuffer(body))

		if recorder.Code != http.StatusOK {
			t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
		}
		if manager.updateInput.videoDest == nil {
			t.Fatalf("expected video destination to be set")
		}
		if manager.updateInput.audioDest != nil {
			t.Fatalf("expected audio destination to be nil for video-only update")
		}
	})
}

// TestAPI_UpdateSession_AllowsPortZero verifies that update accepts
// rtpengine_dest with port 0 to disable media. This matters because SDP can
// signal disabled video with port 0 and the API must still apply audio updates.
// Preconditions: handler with a mock manager that returns a valid session.
// Inputs: POST with video rtpengine_dest of 0.0.0.0:0. Edge case: port 0 is a
// valid value for disabling media. The expected output is HTTP 200 and a
// parsed destination with port 0 forwarded to the manager. Assertions are
// stable because parseDest deterministically handles the port range. Flakiness
// is avoided by using httptest and deterministic mocks. A regression would
// return HTTP 400 or parse a non-zero port.
func TestAPI_UpdateSession_AllowsPortZero(t *testing.T) {
	manager := &mockManager{updateOK: true}
	manager.updateResult = &session.Session{
		ID:      "sess-zero",
		CallID:  "call-zero",
		FromTag: "from-zero",
		ToTag:   "to-zero",
		Audio:   session.Media{APort: 12000, BPort: 12001},
		Video:   session.Media{APort: 12002, BPort: 12003},
	}
	handler := newTestHandler(manager)

	payload := map[string]map[string]string{
		"video": {"rtpengine_dest": "0.0.0.0:0"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	recorder := performRequest(handler, http.MethodPost, "/v1/session/sess-zero/update", bytes.NewBuffer(body))

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if manager.updateInput.videoDest == nil {
		t.Fatalf("expected video destination to be set")
	}
	if manager.updateInput.videoDest.Port != 0 {
		t.Fatalf("expected video destination port 0, got %d", manager.updateInput.videoDest.Port)
	}
}

// TestAPI_DeleteSession_UnknownID_404 verifies that deleting a non-existent
// session returns HTTP 404 and does not report success. This matters because
// callers need accurate feedback when an ID is stale. Preconditions: handler
// with a mock manager that returns false for Delete. Inputs: HTTP DELETE on a
// session ID that does not exist. Edge case: route matches a valid ID but the
// session is missing. The expected output is HTTP 404 with a
// single Delete call, which is stable because the handler forwards directly to
// the manager. Flakiness is avoided by not using network or time. A regression
// would return 200 or skip the Delete call for unknown IDs.
func TestAPI_DeleteSession_UnknownID_404(t *testing.T) {
	manager := &mockManager{deleteOK: false}
	handler := newTestHandler(manager)

	recorder := performRequest(handler, http.MethodDelete, "/v1/session/unknown", nil)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, recorder.Code)
	}
	if manager.deleteCalls != 1 {
		t.Fatalf("expected Delete to be called once")
	}
}

// TestAPI_DeleteSessionPost_UnknownID_404 verifies that the POST fallback delete
// route returns HTTP 404 for missing sessions. This matters because clients
// without DELETE support still need accurate errors. Preconditions: handler with
// a mock manager that returns false for Delete. Inputs: HTTP POST on the delete
// fallback route for an unknown session ID. Edge case: explicit /delete suffix.
// The expected output is HTTP 404 and a single Delete call, which is stable
// because the handler delegates directly to the manager. Flakiness is avoided
// by using httptest without external dependencies. A regression would return
// 200 or skip Delete.
func TestAPI_DeleteSessionPost_UnknownID_404(t *testing.T) {
	manager := &mockManager{deleteOK: false}
	handler := newTestHandler(manager)

	recorder := performRequest(handler, http.MethodPost, "/v1/session/unknown/delete", nil)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, recorder.Code)
	}
	if manager.deleteCalls != 1 {
		t.Fatalf("expected Delete to be called once")
	}
}
