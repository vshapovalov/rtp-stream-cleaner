package api

import (
	"bytes"
	"encoding/json"
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
	cfg := config.Config{PublicIP: "203.0.113.1", InternalIP: "10.0.0.1"}
	return NewHandler(cfg, manager)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/session", bytes.NewBufferString("{bad"))
	recorder := httptest.NewRecorder()

	handler.handleSessionCreate(recorder, req)

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
	req := httptest.NewRequest(http.MethodPost, "/v1/session", bytes.NewBuffer(body))
	recorder := httptest.NewRecorder()

	handler.handleSessionCreate(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, recorder.Code)
	}
	if manager.createCalls != 0 {
		t.Fatalf("expected Create not to be called")
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
	req := httptest.NewRequest(http.MethodPost, "/v1/session/unknown/update", bytes.NewBuffer(body))
	recorder := httptest.NewRecorder()

	handler.handleSessionByID(recorder, req)

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
		req := httptest.NewRequest(http.MethodPost, "/v1/session/sess-a/update", bytes.NewBuffer(body))
		recorder := httptest.NewRecorder()

		handler.handleSessionByID(recorder, req)

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
		req := httptest.NewRequest(http.MethodPost, "/v1/session/sess-v/update", bytes.NewBuffer(body))
		recorder := httptest.NewRecorder()

		handler.handleSessionByID(recorder, req)

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

// TestAPI_DeleteSession_UnknownID_404 verifies that deleting a non-existent
// session returns HTTP 404 and does not report success. This matters because
// callers need accurate feedback when an ID is stale. Preconditions: handler
// with a mock manager that returns false for Delete. Inputs: HTTP DELETE on a
// session ID that does not exist. Edge case: method routing to handleSessionByID
// with a valid ID but missing session. The expected output is HTTP 404 with a
// single Delete call, which is stable because the handler forwards directly to
// the manager. Flakiness is avoided by not using network or time. A regression
// would return 200 or skip the Delete call for unknown IDs.
func TestAPI_DeleteSession_UnknownID_404(t *testing.T) {
	manager := &mockManager{deleteOK: false}
	handler := newTestHandler(manager)

	req := httptest.NewRequest(http.MethodDelete, "/v1/session/unknown", nil)
	recorder := httptest.NewRecorder()

	handler.handleSessionByID(recorder, req)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, recorder.Code)
	}
	if manager.deleteCalls != 1 {
		t.Fatalf("expected Delete to be called once")
	}
}
