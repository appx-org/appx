package terminal

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeDocker implements the Execer interface for testing. It uses net.Pipe to
// create connected reader/writer pairs. The server side simulates the
// container's stdin/stdout. The client side is handed back via ExecAttach as
// the HijackedResponse.
type fakeDocker struct {
	mu sync.Mutex

	// serverConn is the "container side" of the pipe — writing to it simulates
	// container stdout; reading from it captures what was written to container stdin.
	serverConn net.Conn

	// clientConn is handed back via ExecAttach as the HijackedResponse.
	clientConn net.Conn

	execCreateCalled bool
	execAttachCalled bool
	execResizeCalled  bool
	execInspectCalled bool

	lastResizeCols uint
	lastResizeRows uint

	// If set, ExecCreate returns this error.
	execCreateErr error
}

// newFakeDocker creates a fakeDocker with a connected net.Pipe pair.
func newFakeDocker() *fakeDocker {
	server, client := net.Pipe()
	return &fakeDocker{
		serverConn: server,
		clientConn: client,
	}
}

// ExecCreate records that it was called and returns a fake exec ID.
func (f *fakeDocker) ExecCreate(_ context.Context, _ string, _ *ExecCreateOptions) (*ExecCreateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execCreateCalled = true
	if f.execCreateErr != nil {
		return nil, f.execCreateErr
	}
	return &ExecCreateResult{ID: "fake-exec-id"}, nil
}

// ExecAttach returns an ExecAttachResult wrapping the client side of the pipe.
func (f *fakeDocker) ExecAttach(_ context.Context, _ string, _ *ExecAttachOptions) (*ExecAttachResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execAttachCalled = true
	return &ExecAttachResult{Conn: f.clientConn}, nil
}

// ExecResize records the resize dimensions.
func (f *fakeDocker) ExecResize(_ context.Context, _ string, opts *ExecResizeOptions) (*ExecResizeResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execResizeCalled = true
	f.lastResizeCols = opts.Width
	f.lastResizeRows = opts.Height
	return &ExecResizeResult{}, nil
}

// ExecInspect returns a fake result indicating the process is running.
func (f *fakeDocker) ExecInspect(_ context.Context, _ string, _ *ExecInspectOptions) (*ExecInspectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execInspectCalled = true
	return &ExecInspectResult{Running: true}, nil
}

// TestCreateSession_Success verifies that CreateSession creates a new session,
// stores it in the registry, and starts the exec process.
func TestCreateSession_Success(t *testing.T) {
	fd := newFakeDocker()
	defer fd.serverConn.Close()
	m := NewManager(fd, 4096)
	defer m.CloseAll()

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if sess == nil {
		t.Fatal("expected non-nil session")
	}
	if sess.ID == "" {
		t.Fatal("expected non-empty session ID")
	}
	if sess.ProjectID != "proj-1" {
		t.Errorf("ProjectID = %q, want %q", sess.ProjectID, "proj-1")
	}

	// Verify session is retrievable.
	got := m.GetSession(sess.ID)
	if got != sess {
		t.Error("GetSession did not return the created session")
	}

	if !fd.execCreateCalled {
		t.Error("ExecCreate was not called")
	}
	if !fd.execAttachCalled {
		t.Error("ExecAttach was not called")
	}
}

// TestCreateSession_ReturnsExisting verifies the Phase 3 single-session cap:
// calling CreateSession twice for the same project returns the same session.
func TestCreateSession_ReturnsExisting(t *testing.T) {
	fd := newFakeDocker()
	defer fd.serverConn.Close()
	m := NewManager(fd, 4096)
	defer m.CloseAll()

	sess1, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("first CreateSession failed: %v", err)
	}
	sess2, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("second CreateSession failed: %v", err)
	}
	if sess1.ID != sess2.ID {
		t.Errorf("expected same session ID, got %q and %q", sess1.ID, sess2.ID)
	}
}

// TestCloseSession verifies that closing a session removes it from the
// registry and closes the underlying connection.
func TestCloseSession(t *testing.T) {
	fd := newFakeDocker()
	defer fd.serverConn.Close()
	m := NewManager(fd, 4096)
	defer m.CloseAll()

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	m.CloseSession(sess.ID)

	if got := m.GetSession(sess.ID); got != nil {
		t.Error("session still in registry after CloseSession")
	}

	// The session's done channel should be closed.
	select {
	case <-sess.done:
		// OK — closed as expected.
	default:
		t.Error("session done channel not closed after CloseSession")
	}
}

// TestCloseProjectSessions verifies that CloseProjectSessions closes all
// sessions belonging to a specific project.
func TestCloseProjectSessions(t *testing.T) {
	// Each session needs its own pipe pair.
	fd1 := newFakeDocker()
	defer fd1.serverConn.Close()
	fd2 := newFakeDocker()
	defer fd2.serverConn.Close()

	// We need a fake that returns different pipes for each call.
	mfd := &multiFakeDocker{fakes: []*fakeDocker{fd1, fd2}}
	m := NewManager(mfd, 4096)
	defer m.CloseAll()

	sess1, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("first CreateSession failed: %v", err)
	}
	// To create a second session for the same project, we need to close the first cap.
	// Phase 3 enforces 1 session per project, so we test with different projects.
	sess2, err := m.CreateSession("proj-2", "container-2")
	if err != nil {
		t.Fatalf("second CreateSession failed: %v", err)
	}

	m.CloseProjectSessions("proj-1")

	if got := m.GetSession(sess1.ID); got != nil {
		t.Error("proj-1 session still in registry")
	}
	if got := m.GetSession(sess2.ID); got == nil {
		t.Error("proj-2 session should still be in registry")
	}
}

// TestListSessions verifies that ListSessions returns all sessions for a given
// project.
func TestListSessions(t *testing.T) {
	fd := newFakeDocker()
	defer fd.serverConn.Close()
	m := NewManager(fd, 4096)
	defer m.CloseAll()

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	sessions := m.ListSessions("proj-1")
	if len(sessions) != 1 {
		t.Fatalf("ListSessions returned %d sessions, want 1", len(sessions))
	}
	if sessions[0].ID != sess.ID {
		t.Errorf("session ID = %q, want %q", sessions[0].ID, sess.ID)
	}

	// Non-existent project returns empty slice.
	empty := m.ListSessions("nonexistent")
	if len(empty) != 0 {
		t.Errorf("ListSessions for nonexistent project returned %d sessions", len(empty))
	}
}

// TestPumpOutput verifies that data written to the exec stdout (the server
// side of the pipe) is pumped into the ring buffer and broadcast to
// subscribers.
func TestPumpOutput(t *testing.T) {
	fd := newFakeDocker()
	m := NewManager(fd, 4096)
	defer m.CloseAll()

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	ch := m.Subscribe(sess.ID)
	if ch == nil {
		t.Fatal("Subscribe returned nil channel")
	}

	// Write data from the "container" side.
	msg := []byte("hello from container")
	if _, err := fd.serverConn.Write(msg); err != nil {
		t.Fatalf("failed to write to server conn: %v", err)
	}

	// Wait for the data to arrive on the subscriber channel.
	select {
	case data := <-ch:
		if string(data) != string(msg) {
			t.Errorf("subscriber got %q, want %q", data, msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for subscriber data")
	}

	// Also check ring buffer.
	buf := m.ReplayBuffer(sess.ID)
	if string(buf) != string(msg) {
		t.Errorf("ring buffer has %q, want %q", buf, msg)
	}

	fd.serverConn.Close()
}

// TestSessionExitClosesSession verifies that when the exec stdout (server side
// of pipe) sends EOF, the session is automatically cleaned up.
func TestSessionExitClosesSession(t *testing.T) {
	fd := newFakeDocker()
	m := NewManager(fd, 4096)
	defer m.CloseAll()

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Close the server side to simulate exec exit (EOF).
	fd.serverConn.Close()

	// Wait for the session to be cleaned up.
	select {
	case <-sess.done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session cleanup after EOF")
	}

	// Give a moment for registry cleanup.
	time.Sleep(50 * time.Millisecond)

	if got := m.GetSession(sess.ID); got != nil {
		t.Error("session still in registry after exec EOF")
	}
}

// TestCloseAll verifies that CloseAll shuts down every session in the manager.
func TestCloseAll(t *testing.T) {
	fd1 := newFakeDocker()
	defer fd1.serverConn.Close()
	fd2 := newFakeDocker()
	defer fd2.serverConn.Close()

	mfd := &multiFakeDocker{fakes: []*fakeDocker{fd1, fd2}}
	m := NewManager(mfd, 4096)

	sess1, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("CreateSession 1 failed: %v", err)
	}
	sess2, err := m.CreateSession("proj-2", "container-2")
	if err != nil {
		t.Fatalf("CreateSession 2 failed: %v", err)
	}

	m.CloseAll()

	select {
	case <-sess1.done:
	default:
		t.Error("sess1 done channel not closed after CloseAll")
	}
	select {
	case <-sess2.done:
	default:
		t.Error("sess2 done channel not closed after CloseAll")
	}

	if got := m.GetSession(sess1.ID); got != nil {
		t.Error("sess1 still in registry after CloseAll")
	}
	if got := m.GetSession(sess2.ID); got != nil {
		t.Error("sess2 still in registry after CloseAll")
	}
}

// multiFakeDocker delegates Exec calls to successive fakeDocker instances.
// Each call to ExecCreate/ExecAttach advances to the next fake in the list.
type multiFakeDocker struct {
	mu    sync.Mutex
	fakes []*fakeDocker
	idx   int
}

// current returns the current fakeDocker and advances the index on each
// ExecCreate call.
func (m *multiFakeDocker) current() *fakeDocker {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.fakes) {
		return m.fakes[len(m.fakes)-1]
	}
	return m.fakes[m.idx]
}

// advance moves to the next fakeDocker.
func (m *multiFakeDocker) advance() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idx++
}

// ExecCreate delegates to the current fake and advances.
func (m *multiFakeDocker) ExecCreate(ctx context.Context, containerID string, opts *ExecCreateOptions) (*ExecCreateResult, error) {
	f := m.current()
	m.advance()
	return f.ExecCreate(ctx, containerID, opts)
}

// ExecAttach delegates to the appropriate fake. It uses a counter offset by 1
// since advance was already called after ExecCreate.
func (m *multiFakeDocker) ExecAttach(ctx context.Context, execID string, opts *ExecAttachOptions) (*ExecAttachResult, error) {
	m.mu.Lock()
	idx := m.idx - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.fakes) {
		idx = len(m.fakes) - 1
	}
	f := m.fakes[idx]
	m.mu.Unlock()
	return f.ExecAttach(ctx, execID, opts)
}

// ExecResize delegates to the current fake.
func (m *multiFakeDocker) ExecResize(ctx context.Context, execID string, opts *ExecResizeOptions) (*ExecResizeResult, error) {
	return m.current().ExecResize(ctx, execID, opts)
}

// ExecInspect delegates to the current fake.
func (m *multiFakeDocker) ExecInspect(ctx context.Context, execID string, opts *ExecInspectOptions) (*ExecInspectResult, error) {
	return m.current().ExecInspect(ctx, execID, opts)
}

// TestWriteInput verifies that WriteInput sends data to the exec process stdin.
func TestWriteInput(t *testing.T) {
	fd := newFakeDocker()
	m := NewManager(fd, 4096)
	defer m.CloseAll()
	defer fd.serverConn.Close()

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	input := []byte("ls -la\n")

	// Read from the server side concurrently because net.Pipe is synchronous
	// — writes block until the other side reads.
	done := make(chan struct{})
	var readBuf []byte
	var readErr error
	go func() {
		defer close(done)
		buf := make([]byte, 256)
		n, err := fd.serverConn.Read(buf)
		readBuf = buf[:n]
		readErr = err
	}()

	if err := m.WriteInput(sess.ID, input); err != nil {
		t.Fatalf("WriteInput failed: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for server read")
	}

	if readErr != nil {
		t.Fatalf("failed to read from server conn: %v", readErr)
	}
	if string(readBuf) != string(input) {
		t.Errorf("server received %q, want %q", readBuf, input)
	}
}

// TestResize verifies that Resize calls ExecResize with the correct dimensions.
func TestResize(t *testing.T) {
	fd := newFakeDocker()
	defer fd.serverConn.Close()
	m := NewManager(fd, 4096)
	defer m.CloseAll()

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if err := m.Resize(sess.ID, 120, 40); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}

	fd.mu.Lock()
	defer fd.mu.Unlock()
	if !fd.execResizeCalled {
		t.Error("ExecResize was not called")
	}
	if fd.lastResizeCols != 120 {
		t.Errorf("resize cols = %d, want 120", fd.lastResizeCols)
	}
	if fd.lastResizeRows != 40 {
		t.Errorf("resize rows = %d, want 40", fd.lastResizeRows)
	}
}

// TestUnsubscribe verifies that Unsubscribe removes a subscriber channel from
// the session.
func TestUnsubscribe(t *testing.T) {
	fd := newFakeDocker()
	m := NewManager(fd, 4096)
	defer m.CloseAll()
	defer fd.serverConn.Close()

	sess, err := m.CreateSession("proj-1", "container-1")
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	ch := m.Subscribe(sess.ID)
	if ch == nil {
		t.Fatal("Subscribe returned nil")
	}

	m.Unsubscribe(sess.ID, ch)

	// Verify the subscriber count is 0 by checking the session directly.
	sess.mu.Lock()
	count := len(sess.subs)
	sess.mu.Unlock()
	if count != 0 {
		t.Errorf("subscriber count = %d after Unsubscribe, want 0", count)
	}
}

// Compile-time interface checks: verify fakeDocker and multiFakeDocker
// satisfy the Execer interface.
var _ Execer = (*fakeDocker)(nil)
var _ Execer = (*multiFakeDocker)(nil)
