package egress

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestInternalHandler_ValidRequest(t *testing.T) {
	reg := NewPendingRegistry(nil)
	handler := newInternalHandler(reg)

	// Approve immediately in background.
	go func() {
		time.Sleep(50 * time.Millisecond)
		list := reg.List()
		if len(list) == 0 {
			t.Error("expected pending request")
			return
		}
		reg.Resolve(list[0].ID, true)
	}()

	body, _ := json.Marshal(map[string]any{
		"host":   "pypi.org",
		"port":   443,
		"reason": "install rich",
	})
	req := httptest.NewRequest("POST", "/egress/request", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Allowed bool `json:"allowed"`
		Timeout bool `json:"timeout"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Allowed {
		t.Error("expected allowed=true")
	}
	if resp.Timeout {
		t.Error("expected timeout=false")
	}
}

func TestInternalHandler_Timeout(t *testing.T) {
	reg := NewPendingRegistry(nil)
	reg.timeout = 100 * time.Millisecond
	handler := newInternalHandler(reg)

	body, _ := json.Marshal(map[string]any{
		"host":   "evil.com",
		"port":   443,
		"reason": "exfiltrate",
	})
	req := httptest.NewRequest("POST", "/egress/request", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Allowed bool `json:"allowed"`
		Timeout bool `json:"timeout"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Allowed {
		t.Error("expected allowed=false on timeout")
	}
	if !resp.Timeout {
		t.Error("expected timeout=true")
	}
}

func TestInternalHandler_BadMethod(t *testing.T) {
	reg := NewPendingRegistry(nil)
	handler := newInternalHandler(reg)
	req := httptest.NewRequest("GET", "/egress/request", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestInternalHandler_BadJSON(t *testing.T) {
	reg := NewPendingRegistry(nil)
	handler := newInternalHandler(reg)
	req := httptest.NewRequest("POST", "/egress/request", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
