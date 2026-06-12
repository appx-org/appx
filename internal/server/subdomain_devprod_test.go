package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestSubdomainDispatch_DevAndProdPortSelection(t *testing.T) {
	prodBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("PROD"))
	}))
	defer prodBackend.Close()
	devBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("DEV"))
	}))
	defer devBackend.Close()

	prodPort := portOf(t, prodBackend.URL)
	devPort := portOf(t, devBackend.URL)

	handler, store, db := setupTestWithConfig(t, RouterConfig{BaseDomain: "localhost"})
	if _, err := db.Exec(
		`INSERT INTO projects (id, name, status, assigned_port, dev_port) VALUES (?, ?, ?, ?, ?)`,
		"pair-id", "myapp", "stopped", prodPort, devPort,
	); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	cases := []struct {
		host string
		want string
	}{
		{"myapp.localhost", "PROD"},
		{"myapp-dev.localhost", "DEV"},
	}
	for _, tc := range cases {
		req := authedRequest(t, store, "GET", "/", "")
		req.Host = tc.host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", tc.host, w.Code, w.Body.String())
		}
		if w.Body.String() != tc.want {
			t.Errorf("%s routed to %q, want %q", tc.host, w.Body.String(), tc.want)
		}
	}
}

func TestSubdomainDispatch_DevWithoutDevPortReturns404(t *testing.T) {
	handler, store, db := setupTestWithConfig(t, RouterConfig{BaseDomain: "localhost"})
	// Legacy single-port project: dev_port is NULL/0.
	if _, err := db.Exec(
		`INSERT INTO projects (id, name, status, assigned_port) VALUES (?, ?, ?, ?)`,
		"legacy-id", "legacy", "stopped", 51000,
	); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	req := authedRequest(t, store, "GET", "/", "")
	req.Host = "legacy-dev.localhost"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing dev env, got %d", w.Code)
	}
}

// TestSubdomainDispatch_WebSocketUpgradePassesThrough verifies the subdomain
// proxy forwards a WebSocket upgrade to the app (a generic capability for user
// apps, independent of the dev=prod build model).
func TestSubdomainDispatch_WebSocketUpgradePassesThrough(t *testing.T) {
	upgrader := websocket.Upgrader{}
	appBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("backend upgrade: %v", err)
			return
		}
		defer conn.Close()
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, append([]byte("echo:"), msg...))
	}))
	defer appBackend.Close()
	appPort := portOf(t, appBackend.URL)

	handler, store, db := setupTestWithConfig(t, RouterConfig{BaseDomain: "localhost", HTTPMode: true})
	if _, err := db.Exec(
		`INSERT INTO projects (id, name, status, assigned_port) VALUES (?, ?, ?, ?)`,
		"ws-id", "wsapp", "stopped", appPort,
	); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	// Front the appx handler with a real server so we can dial a WebSocket.
	front := httptest.NewServer(handler)
	defer front.Close()
	frontAddr := strings.TrimPrefix(front.URL, "http://")

	token, err := store.CreateSession()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	dialer := websocket.Dialer{
		// Route every dial to the appx front server regardless of the Host in the
		// URL, so the proxy sees Host: wsapp.localhost.
		NetDial: func(_, _ string) (net.Conn, error) { return net.Dial("tcp", frontAddr) },
	}
	header := http.Header{"Cookie": {"appx_session=" + token}}
	conn, resp, err := dialer.Dial("ws://wsapp.localhost/socket", header)
	if err != nil {
		t.Fatalf("dial websocket through proxy: %v (status %v)", err, resp.Status)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte("hi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(msg) != "echo:hi" {
		t.Errorf("got %q, want echo:hi", msg)
	}
}

// portOf extracts the numeric port from an httptest server URL.
func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	port, err := strconv.Atoi(strings.TrimPrefix(rawURL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatalf("parse port from %q: %v", rawURL, err)
	}
	return port
}
