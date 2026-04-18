package terminal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/neuromaxer/appx/internal/auth"
	_ "modernc.org/sqlite"
)

// setupHandlerTest creates an in-memory SQLite database with the auth schema,
// a terminal Manager backed by a fakeDocker, and an httptest.Server serving the
// WebSocket handler behind auth middleware. It returns the test server, the
// Manager, the fakeDocker, and a valid session token for authenticated requests.
func setupHandlerTest(t *testing.T) (ts *httptest.Server, m *Manager, fd *fakeDocker, token string) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE sessions (token TEXT PRIMARY KEY, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, expires_at DATETIME);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}

	store := auth.NewStore(db)
	if err := store.SetPassword("testpassword1"); err != nil {
		t.Fatalf("set password: %v", err)
	}

	token, err = store.CreateSession()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	a := auth.New(store)

	fd = newFakeDocker()
	t.Cleanup(func() { fd.serverConn.Close() })

	m = NewManager(fd, 4096)
	t.Cleanup(func() { m.CloseAll() })

	handler := a.Middleware(http.HandlerFunc(HandleTerminalWS(m)))

	mux := http.NewServeMux()
	mux.Handle("/ws/term/", handler)

	ts = httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	return ts, m, fd, token
}

// wsURL converts an httptest.Server URL to a WebSocket URL for the given
// session ID.
func wsURL(ts *httptest.Server, sessionID string) string {
	return "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/term/" + sessionID
}

// dialWS connects to the WebSocket endpoint with a valid session cookie and
// Origin header. Returns the WebSocket connection and HTTP response.
func dialWS(t *testing.T, ts *httptest.Server, sessionID, token string) (*websocket.Conn, *http.Response) {
	t.Helper()

	dialer := websocket.Dialer{}
	header := http.Header{}
	header.Set("Cookie", "appx_session="+token)
	header.Set("Origin", ts.URL)

	conn, resp, err := dialer.Dial(wsURL(ts, sessionID), header)
	if err != nil {
		t.Fatalf("dial WebSocket: %v", err)
	}
	return conn, resp
}

// TestWS_Unauthenticated verifies that a WebSocket connection without a session
// cookie is rejected with 401 by the auth middleware.
func TestWS_Unauthenticated(t *testing.T) {
	ts, _, _, _ := setupHandlerTest(t)

	dialer := websocket.Dialer{}
	header := http.Header{}
	header.Set("Origin", ts.URL)

	_, resp, err := dialer.Dial(wsURL(ts, "fake-session"), header)
	if err == nil {
		t.Fatal("expected dial error for unauthenticated request")
	}
	if resp != nil && resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// TestWS_SessionNotFound verifies that connecting to a non-existent terminal
// session results in a WebSocket close frame with code 4004.
func TestWS_SessionNotFound(t *testing.T) {
	ts, _, _, token := setupHandlerTest(t)

	dialer := websocket.Dialer{}
	header := http.Header{}
	header.Set("Cookie", "appx_session="+token)
	header.Set("Origin", ts.URL)

	conn, _, err := dialer.Dial(wsURL(ts, "nonexistent-session"), header)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	// The server should send a close frame with code 4004.
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected read error (close frame)")
	}

	closeErr, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("expected CloseError, got %T: %v", err, err)
	}
	if closeErr.Code != 4004 {
		t.Errorf("close code = %d, want 4004", closeErr.Code)
	}
}

// TestWS_InputForwarded verifies that binary data sent from the WebSocket
// client is forwarded to the exec process stdin.
func TestWS_InputForwarded(t *testing.T) {
	ts, m, fd, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	conn, _ := dialWS(t, ts, sess.ID, token)
	defer conn.Close()

	// Read the initial ring buffer replay (empty, but handler sends it).
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn.ReadMessage() // ring buffer replay (may be empty binary frame)

	// Send binary input from the client.
	input := []byte("ls -la\n")
	if err := conn.WriteMessage(websocket.BinaryMessage, input); err != nil {
		t.Fatalf("write input: %v", err)
	}

	// Read from the server side of the pipe to verify the input arrived.
	buf := make([]byte, 256)
	fd.serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := fd.serverConn.Read(buf)
	if err != nil {
		t.Fatalf("read from server conn: %v", err)
	}
	if string(buf[:n]) != string(input) {
		t.Errorf("server received %q, want %q", buf[:n], input)
	}
}

// TestWS_OutputReceived verifies that data written to the exec stdout is
// forwarded to the WebSocket client as binary frames.
func TestWS_OutputReceived(t *testing.T) {
	ts, m, fd, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Write data from the "container" side before connecting so it appears in
	// the ring buffer replay, then the next message is new output.
	preload := []byte("preload\n")
	if _, err := fd.serverConn.Write(preload); err != nil {
		t.Fatalf("write preload: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	conn, _ := dialWS(t, ts, sess.ID, token)
	defer conn.Close()

	// Read the ring buffer replay (contains preload).
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn.ReadMessage()

	// Write new data from the "container" side.
	output := []byte("total 42\n")
	if _, err := fd.serverConn.Write(output); err != nil {
		t.Fatalf("write to server conn: %v", err)
	}

	// Read from the WebSocket client.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msgType, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read from ws: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Errorf("message type = %d, want BinaryMessage (%d)", msgType, websocket.BinaryMessage)
	}
	if string(data) != string(output) {
		t.Errorf("received %q, want %q", data, output)
	}
}

// TestWS_RingBufferReplayed verifies that the ring buffer contents are sent as
// the first binary message when a WebSocket client connects.
func TestWS_RingBufferReplayed(t *testing.T) {
	ts, m, fd, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Write some data to the ring buffer before connecting.
	preload := []byte("previous output\n")
	if _, err := fd.serverConn.Write(preload); err != nil {
		t.Fatalf("write preload: %v", err)
	}

	// Wait for the output pump to process the data.
	time.Sleep(100 * time.Millisecond)

	conn, _ := dialWS(t, ts, sess.ID, token)
	defer conn.Close()

	// The first message should be the ring buffer replay.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msgType, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read ring buffer replay: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Errorf("message type = %d, want BinaryMessage", msgType)
	}
	if string(data) != string(preload) {
		t.Errorf("replay = %q, want %q", data, preload)
	}
}

// TestWS_ResizeForwarded verifies that a JSON resize message sent as a text
// frame results in an ExecResize call with the correct dimensions.
func TestWS_ResizeForwarded(t *testing.T) {
	ts, m, fd, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	conn, _ := dialWS(t, ts, sess.ID, token)
	defer conn.Close()

	// Read the ring buffer replay.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn.ReadMessage()

	// Send a resize message as a text frame.
	resize := map[string]int{"cols": 120, "rows": 40}
	data, _ := json.Marshal(resize)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write resize: %v", err)
	}

	// Give the handler time to process the resize.
	time.Sleep(100 * time.Millisecond)

	fd.mu.Lock()
	defer fd.mu.Unlock()
	if !fd.execResizeCalled {
		t.Fatal("ExecResize was not called")
	}
	if fd.lastResizeCols != 120 {
		t.Errorf("resize cols = %d, want 120", fd.lastResizeCols)
	}
	if fd.lastResizeRows != 40 {
		t.Errorf("resize rows = %d, want 40", fd.lastResizeRows)
	}
}

// TestWS_SessionSurvivesDisconnect verifies that the terminal session remains
// alive after the WebSocket client disconnects (the session is not closed).
func TestWS_SessionSurvivesDisconnect(t *testing.T) {
	ts, m, _, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	conn, _ := dialWS(t, ts, sess.ID, token)

	// Read the ring buffer replay.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn.ReadMessage()

	// Close the WebSocket connection.
	conn.Close()

	// Give the handler time to clean up.
	time.Sleep(100 * time.Millisecond)

	// The session should still exist.
	if got := m.GetSession(sess.ID); got == nil {
		t.Error("session was closed after WebSocket disconnect; should survive")
	}
}

// TestWS_WrongOrigin verifies that a WebSocket connection with a mismatched
// Origin header is rejected.
func TestWS_WrongOrigin(t *testing.T) {
	ts, m, _, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	dialer := websocket.Dialer{}
	header := http.Header{}
	header.Set("Cookie", "appx_session="+token)
	header.Set("Origin", "http://evil.example.com")

	_, _, err = dialer.Dial(wsURL(ts, sess.ID), header)
	if err == nil {
		t.Fatal("expected dial error for wrong Origin")
	}
}

// TestWS_MissingOrigin verifies that a WebSocket connection without an Origin
// header is rejected.
func TestWS_MissingOrigin(t *testing.T) {
	ts, m, _, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	dialer := websocket.Dialer{}
	header := http.Header{}
	header.Set("Cookie", "appx_session="+token)
	// No Origin header.

	_, _, err = dialer.Dial(wsURL(ts, sess.ID), header)
	if err == nil {
		t.Fatal("expected dial error for missing Origin")
	}
}

// TestWS_ResizeNegativeDimensions verifies that resize messages with negative
// cols or rows are ignored (no ExecResize call).
func TestWS_ResizeNegativeDimensions(t *testing.T) {
	ts, m, fd, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	conn, _ := dialWS(t, ts, sess.ID, token)
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn.ReadMessage()

	resize := map[string]int{"cols": -1, "rows": 40}
	data, _ := json.Marshal(resize)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write resize: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.execResizeCalled {
		t.Error("ExecResize should not be called for negative dimensions")
	}
}

// TestWS_ResizeZeroDimensions verifies that resize messages with zero cols or
// rows are ignored.
func TestWS_ResizeZeroDimensions(t *testing.T) {
	ts, m, fd, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	conn, _ := dialWS(t, ts, sess.ID, token)
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn.ReadMessage()

	resize := map[string]int{"cols": 0, "rows": 24}
	data, _ := json.Marshal(resize)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write resize: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.execResizeCalled {
		t.Error("ExecResize should not be called for zero dimensions")
	}
}

// TestWS_ResizeExtremeDimensions verifies that resize messages with cols or
// rows exceeding 500 are clamped to 500.
func TestWS_ResizeExtremeDimensions(t *testing.T) {
	ts, m, fd, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	conn, _ := dialWS(t, ts, sess.ID, token)
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn.ReadMessage()

	resize := map[string]int{"cols": 9999, "rows": 1000}
	data, _ := json.Marshal(resize)
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write resize: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	fd.mu.Lock()
	defer fd.mu.Unlock()
	if !fd.execResizeCalled {
		t.Fatal("ExecResize was not called for extreme dimensions")
	}
	if fd.lastResizeCols != 500 {
		t.Errorf("resize cols = %d, want 500 (clamped)", fd.lastResizeCols)
	}
	if fd.lastResizeRows != 500 {
		t.Errorf("resize rows = %d, want 500 (clamped)", fd.lastResizeRows)
	}
}

// TestWS_ResizeMalformedJSON verifies that a text frame with invalid JSON is
// silently ignored (no crash, no resize).
func TestWS_ResizeMalformedJSON(t *testing.T) {
	ts, m, fd, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	conn, _ := dialWS(t, ts, sess.ID, token)
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, _ = conn.ReadMessage()

	// Send malformed JSON.
	if err := conn.WriteMessage(websocket.TextMessage, []byte("{not json")); err != nil {
		t.Fatalf("write malformed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	fd.mu.Lock()
	defer fd.mu.Unlock()
	if fd.execResizeCalled {
		t.Error("ExecResize should not be called for malformed JSON")
	}

	// Verify the connection is still alive by sending a valid binary message.
	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("x")); err != nil {
		t.Errorf("connection should still be alive after malformed JSON: %v", err)
	}
}

// TestWS_IdleTimeoutClosesConnection verifies that a WebSocket terminal
// connection is closed by the server when no input is received for the idle
// timeout duration. The test overrides terminalIdleTimeout to a short value so
// it completes quickly.
func TestWS_IdleTimeoutClosesConnection(t *testing.T) {
	// Override the idle timeout so the test completes in milliseconds.
	orig := terminalIdleTimeout
	terminalIdleTimeout = 100 * time.Millisecond
	t.Cleanup(func() { terminalIdleTimeout = orig })

	ts, m, _, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	conn, _ := dialWS(t, ts, sess.ID, token)
	defer conn.Close()

	// There is no preloaded data so the server sends no replay frame. Skip any
	// attempt to read one — the server's idle deadline starts immediately after
	// the connection is established. Just wait for the server to time us out.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed by server after idle timeout")
	}
}

// TestWS_IdleTimeoutResetOnActivity verifies that sending input resets the
// idle timer and prevents premature disconnection. The timeout is set to 300ms
// and input is sent every 100ms, so each send arrives well within the window.
func TestWS_IdleTimeoutResetOnActivity(t *testing.T) {
	// Override the idle timeout to a value comfortably larger than the send interval.
	orig := terminalIdleTimeout
	terminalIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { terminalIdleTimeout = orig })

	ts, m, _, token := setupHandlerTest(t)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	conn, _ := dialWS(t, ts, sess.ID, token)
	defer conn.Close()

	// There is no preloaded data so the server sends no replay frame.
	// Send input messages at 100ms intervals (well within the 300ms timeout).
	// Each received message resets the server-side deadline, so the connection
	// should remain alive for all iterations.
	for i := 0; i < 5; i++ {
		time.Sleep(100 * time.Millisecond)
		if err := conn.WriteMessage(websocket.BinaryMessage, []byte("ping")); err != nil {
			t.Fatalf("iteration %d: write failed (connection closed prematurely): %v", i, err)
		}
	}

	// After activity stops, the connection should close within ~300ms.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection to be closed after final idle period")
	}
}

// TestWS_ReplayCapLimit verifies that the ring buffer replay is capped to
// maxReplayBytes (64 KB) on reconnect, preventing bandwidth amplification from
// rapid reconnects even when the buffer is configured larger.
func TestWS_ReplayCapLimit(t *testing.T) {
	// Create a manager with a larger buffer (256 KB).
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE sessions (token TEXT PRIMARY KEY, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, expires_at DATETIME);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}

	store := auth.NewStore(db)
	if err := store.SetPassword("testpassword1"); err != nil {
		t.Fatalf("set password: %v", err)
	}

	token, err := store.CreateSession()
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	a := auth.New(store)

	fd := newFakeDocker()
	t.Cleanup(func() { fd.serverConn.Close() })

	// Create manager with 256 KB buffer (much larger than maxReplayBytes).
	m := NewManager(fd, 256*1024)
	t.Cleanup(func() { m.CloseAll() })

	handler := a.Middleware(http.HandlerFunc(HandleTerminalWS(m)))

	mux := http.NewServeMux()
	mux.Handle("/ws/term/", handler)

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Write 100 KB of data to the ring buffer before connecting.
	// This is less than maxReplayBytes (64 KB cap), but more demonstrates
	// the mechanism. Let's write 80 KB to exceed the cap.
	preload := make([]byte, 80*1024)
	for i := range preload {
		preload[i] = byte(i % 256)
	}
	if _, err := fd.serverConn.Write(preload); err != nil {
		t.Fatalf("write preload: %v", err)
	}

	// Wait for the output pump to process the data.
	time.Sleep(100 * time.Millisecond)

	// Verify the buffer contains the full 80 KB.
	fullBuf := m.ReplayBuffer(sess.ID)
	if len(fullBuf) != len(preload) {
		t.Errorf("buffer size = %d, want %d", len(fullBuf), len(preload))
	}

	// Connect and read the replay.
	conn, _ := dialWS(t, ts, sess.ID, token)
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	msgType, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if msgType != websocket.BinaryMessage {
		t.Errorf("message type = %d, want BinaryMessage", msgType)
	}

	// The replay should be capped to maxReplayBytes (64 KB), not the full 80 KB.
	if len(data) > maxReplayBytes {
		t.Errorf("replay size = %d, want <= %d (maxReplayBytes)", len(data), maxReplayBytes)
	}

	// Verify the replay contains the most recent data (tail of the buffer).
	expectedReplay := fullBuf[len(fullBuf)-len(data):]
	if !bytes.Equal(data, expectedReplay) {
		t.Error("replay does not match the tail of the full buffer")
	}
}
