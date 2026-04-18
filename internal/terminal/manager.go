package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrSessionNotFound is returned when an operation references a session ID
// that does not exist in the registry.
var ErrSessionNotFound = errors.New("session not found")

// subscriberBufSize is the capacity of each subscriber channel. When a
// subscriber's channel is full, the slow subscriber is evicted to prevent
// stalling the output pump for other subscribers.
const subscriberBufSize = 256

// Execer is the subset of container runtime API used by the terminal Manager. It
// contains only the exec methods needed for terminal sessions so the terminal
// package does not import the project package. Exported so that other packages
// (e.g. server tests) can provide their own implementations.
type Execer interface {
	ExecCreate(ctx context.Context, containerID string, options *ExecCreateOptions) (*ExecCreateResult, error)
	ExecAttach(ctx context.Context, execID string, options *ExecAttachOptions) (*ExecAttachResult, error)
	ExecResize(ctx context.Context, execID string, options *ExecResizeOptions) (*ExecResizeResult, error)
	ExecInspect(ctx context.Context, execID string, options *ExecInspectOptions) (*ExecInspectResult, error)
}

// ExecCreateOptions mirrors Docker's exec create options.
type ExecCreateOptions struct {
	Cmd            []string
	AttachStdin    bool
	AttachStdout   bool
	AttachStderr   bool
	Tty            bool
	WorkingDir     string
	Env            []string
	User           string
	Privileged     bool
	ContainerID    string
}

// ExecCreateResult mirrors Docker's exec create result.
type ExecCreateResult struct {
	ID string
}

// ExecAttachOptions mirrors Docker's exec attach options.
type ExecAttachOptions struct {
	Stream bool
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Logs   bool
}

// ExecAttachResult mirrors Docker's exec attach result with a hijacked connection.
type ExecAttachResult struct {
	Conn io.ReadWriteCloser
}

// ExecResizeOptions mirrors Docker's exec resize options.
type ExecResizeOptions struct {
	Height uint
	Width  uint
}

// ExecResizeResult mirrors Docker's exec resize result.
type ExecResizeResult struct {
}

// ExecInspectOptions mirrors Docker's exec inspect options.
type ExecInspectOptions struct {
}

// ExecInspectResult mirrors Docker's exec inspect result.
type ExecInspectResult struct {
	Running bool
	ExitCode int
}

// Session represents a running terminal session backed by a Docker exec
// process. It holds the exec process's hijacked connection, a ring buffer for
// output replay on reconnect, and a set of subscriber channels for
// broadcasting output to WebSocket connections.
type Session struct {
	// ID is the unique session identifier (UUID v4).
	ID string

	// ProjectID is the project this session belongs to.
	ProjectID string

	// CreatedAt is the time the session was created.
	CreatedAt time.Time

	// ExecID is the Docker exec process ID.
	ExecID string

	// conn is the connection to the exec process's stdin/stdout.
	conn io.ReadWriteCloser

	// buf is the circular output buffer for replay on reconnect.
	buf *RingBuffer

	// subs is the set of active subscriber channels. Each WebSocket handler
	// registers a channel here to receive output.
	subs map[chan []byte]struct{}

	// mu protects buf and subs.
	mu sync.Mutex

	// done is closed when the session is torn down. All goroutines associated
	// with this session select on done and exit when it closes.
	done chan struct{}

	// closeOnce ensures the session is closed exactly once.
	closeOnce sync.Once
}

// Manager owns the session registry: a map of session ID to Session, plus a
// project-to-sessions index for looking up all sessions belonging to a
// project. It uses the Docker SDK's exec methods to create and manage terminal
// sessions inside containers.
type Manager struct {
	docker  Execer
	bufSize int

	mu        sync.Mutex
	sessions  map[string]*Session
	byProject map[string]map[string]*Session // projectID → sessionID → Session
}

// NewManager creates a terminal Manager that uses the given Docker exec client
// and allocates ring buffers of bufSize bytes for each session.
func NewManager(docker Execer, bufSize int) *Manager {
	return &Manager{
		docker:    docker,
		bufSize:   bufSize,
		sessions:  make(map[string]*Session),
		byProject: make(map[string]map[string]*Session),
	}
}

// CreateSession creates a new terminal session for the given project and
// container. If a session already exists for the project (Phase 3 single-
// session cap), the existing session is returned. CreateSession starts a
// docker exec process with an interactive bash shell and launches the output
// pump goroutine.
func (m *Manager) CreateSession(projectID, containerID string) (*Session, error) {
	m.mu.Lock()

	// Phase 3 cap: return existing session if one exists for this project.
	if projSessions, ok := m.byProject[projectID]; ok {
		for _, sess := range projSessions {
			m.mu.Unlock()
			return sess, nil
		}
	}

	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create the exec process. TERM=xterm-256color is required for TUI
	// TUI applications (vim, tmux, etc.) that need color and cursor control.
	// The real PTY size arrives via ExecResize once the WebSocket connects.
	createResult, err := m.docker.ExecCreate(ctx, containerID, &ExecCreateOptions{
		Cmd:          []string{"/bin/bash"},
		Tty:          true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Env:          []string{"TERM=xterm-256color"},
	})
	if err != nil {
		return nil, fmt.Errorf("exec create: %w", err)
	}

	// Attach to the exec process — this hijacks the connection and
	// implicitly starts the exec. Do NOT call ExecStart separately;
	// ExecAttach both attaches and starts the process in one call.
	attachResult, err := m.docker.ExecAttach(ctx, createResult.ID, &ExecAttachOptions{
		Stream: true,
		Stdin:  nil,
		Stdout: nil,
		Stderr: nil,
	})
	if err != nil {
		return nil, fmt.Errorf("exec attach: %w", err)
	}

	sess := &Session{
		ID:        uuid.New().String(),
		ProjectID: projectID,
		CreatedAt: time.Now().UTC(),
		ExecID:    createResult.ID,
		conn:      attachResult.Conn,
		buf:       NewRingBuffer(m.bufSize),
		subs:      make(map[chan []byte]struct{}),
		done:      make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[sess.ID] = sess
	if m.byProject[projectID] == nil {
		m.byProject[projectID] = make(map[string]*Session)
	}
	m.byProject[projectID][sess.ID] = sess
	m.mu.Unlock()

	go m.pumpOutput(sess)

	return sess, nil
}

// GetSession returns the session with the given ID, or nil if not found.
func (m *Manager) GetSession(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// ListSessions returns all sessions belonging to the given project. Returns an
// empty slice if the project has no sessions.
func (m *Manager) ListSessions(projectID string) []*Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	projSessions := m.byProject[projectID]
	result := make([]*Session, 0, len(projSessions))
	for _, sess := range projSessions {
		result = append(result, sess)
	}
	return result
}

// Subscribe creates and returns a buffered channel that receives copies of all
// output from the session. Returns nil if the session does not exist.
func (m *Manager) Subscribe(sessionID string) chan []byte {
	m.mu.Lock()
	sess := m.sessions[sessionID]
	m.mu.Unlock()

	if sess == nil {
		return nil
	}

	ch := make(chan []byte, subscriberBufSize)
	sess.mu.Lock()
	sess.subs[ch] = struct{}{}
	sess.mu.Unlock()
	return ch
}

// Unsubscribe removes the given channel from the session's subscriber set.
// It is a no-op if the session does not exist or the channel is not registered.
func (m *Manager) Unsubscribe(sessionID string, ch chan []byte) {
	m.mu.Lock()
	sess := m.sessions[sessionID]
	m.mu.Unlock()

	if sess == nil {
		return
	}

	sess.mu.Lock()
	delete(sess.subs, ch)
	sess.mu.Unlock()
}

// CloseSession tears down the session with the given ID: closes the exec
// connection, signals all goroutines via the done channel, closes all
// subscriber channels, and removes the session from the registry.
func (m *Manager) CloseSession(id string) {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	delete(m.sessions, id)
	if projSessions, ok := m.byProject[sess.ProjectID]; ok {
		delete(projSessions, id)
		if len(projSessions) == 0 {
			delete(m.byProject, sess.ProjectID)
		}
	}
	m.mu.Unlock()

	sess.closeOnce.Do(func() {
		close(sess.done)
		sess.conn.Close()

		sess.mu.Lock()
		for ch := range sess.subs {
			close(ch)
			delete(sess.subs, ch)
		}
		sess.mu.Unlock()
	})
}

// CloseProjectSessions closes all sessions belonging to the given project.
// Called by the project Manager before stopping or deleting a container.
func (m *Manager) CloseProjectSessions(projectID string) {
	m.mu.Lock()
	projSessions := m.byProject[projectID]
	ids := make([]string, 0, len(projSessions))
	for id := range projSessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.CloseSession(id)
	}
}

// CloseAll tears down every session in the manager. Called during graceful
// server shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()

	for _, id := range ids {
		m.CloseSession(id)
	}
}

// WriteInput sends the given data to the exec process's stdin. Returns an
// error if the session does not exist or the write fails.
func (m *Manager) WriteInput(sessionID string, data []byte) error {
	m.mu.Lock()
	sess := m.sessions[sessionID]
	m.mu.Unlock()

	if sess == nil {
		return ErrSessionNotFound
	}

	if _, err := sess.conn.Write(data); err != nil {
		return fmt.Errorf("write to exec stdin: %w", err)
	}
	return nil
}

// Resize changes the terminal dimensions of the exec process. Returns an error
// if the session does not exist or the resize fails.
func (m *Manager) Resize(sessionID string, cols, rows uint) error {
	m.mu.Lock()
	sess := m.sessions[sessionID]
	m.mu.Unlock()

	if sess == nil {
		return ErrSessionNotFound
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := m.docker.ExecResize(ctx, sess.ExecID, &ExecResizeOptions{
		Width:  cols,
		Height: rows,
	}); err != nil {
		return fmt.Errorf("exec resize: %w", err)
	}
	return nil
}

// ReplayBuffer returns a copy of the session's ring buffer contents in
// chronological order. Returns nil if the session does not exist.
func (m *Manager) ReplayBuffer(sessionID string) []byte {
	m.mu.Lock()
	sess := m.sessions[sessionID]
	m.mu.Unlock()

	if sess == nil {
		return nil
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.buf.Bytes()
}

// pumpOutput reads from the exec process's stdout and writes to the ring
// buffer and all subscriber channels. When the exec process exits (EOF on
// stdout), pumpOutput calls CloseSession to clean up. This goroutine runs for
// the lifetime of the session.
func (m *Manager) pumpOutput(sess *Session) {
	buf := make([]byte, 32*1024) // 32KB read buffer

	for {
		n, err := sess.conn.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])

			sess.mu.Lock()
			sess.buf.Write(data)

			// Broadcast to all subscribers. Evict slow subscribers whose
			// channels are full to prevent stalling.
			for ch := range sess.subs {
				select {
				case ch <- data:
				default:
					// Slow subscriber — evict it.
					close(ch)
					delete(sess.subs, ch)
				}
			}
			sess.mu.Unlock()
		}

		if err != nil {
			if err != io.EOF {
				// Unexpected error — still close the session.
				_ = err
			}
			// EOF or error — exec process exited. Clean up.
			m.CloseSession(sess.ID)
			return
		}
	}
}
