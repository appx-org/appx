// Package terminal provides the local PTY manager used by the shell endpoint.
// This file implements LocalManager, which spawns real OS-level PTY processes
// using creack/pty. It shares the ring buffer and pub/sub fan-out patterns from
// the existing Manager but replaces the Docker exec backend with a direct
// os/exec + PTY attach.
package terminal

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

// LocalSession holds a running shell process attached to a PTY. It mirrors
// the ring buffer and subscriber fan-out design of Session, but the underlying
// I/O comes from the PTY master file descriptor rather than a Docker exec stream.
type LocalSession struct {
	// ID is the globally unique session identifier returned to the client.
	ID string
	// CreatedAt is when the session was started.
	CreatedAt time.Time

	ptmx      *os.File                 // PTY master fd — read=output, write=input
	cmd       *exec.Cmd                // underlying shell process
	buf       *RingBuffer              // ring buffer for output replay on reconnect
	mu        sync.Mutex               // guards subs
	subs      map[chan []byte]struct{} // active WebSocket subscribers
	done      chan struct{}            // closed when the session ends
	closeOnce sync.Once
}

// LocalManager manages a set of LocalSession instances. Each session is an
// independent PTY process (typically /bin/sh or $SHELL). The manager is
// intentionally simple — one session per ID, no project grouping — because
// it is used for the appx server terminal experiment, not per-project
// agent terminals.
type LocalManager struct {
	mu       sync.Mutex
	sessions map[string]*LocalSession
	bufSize  int
}

// NewLocalManager creates a LocalManager with the given ring buffer size in bytes.
func NewLocalManager(bufSize int) *LocalManager {
	return &LocalManager{
		sessions: make(map[string]*LocalSession),
		bufSize:  bufSize,
	}
}

// Create spawns a new shell process with a PTY attached and registers the
// session. cwd is the working directory for the shell; if empty it defaults
// to the current process's working directory. Returns the new session on
// success. The caller must eventually call Close to release resources.
func (m *LocalManager) Create(cwd string) (*LocalSession, error) {
	// Resolve the current user's login shell and home directory so the PTY
	// gets a full interactive environment even when appx runs as a systemd
	// service (which has a minimal environment without ~/.profile paths).
	shell := "/bin/sh"
	home := os.Getenv("HOME")
	if u, err := user.Current(); err == nil {
		home = u.HomeDir
		// On Linux/macOS the Shell field comes from /etc/passwd.
		if u.HomeDir != "" && home == "" {
			home = u.HomeDir
		}
	}
	// Prefer SHELL env var (user override), fall back to /etc/passwd lookup.
	if s := os.Getenv("SHELL"); s != "" {
		shell = s
	} else if u, err := user.Current(); err == nil {
		// user.Current().Shell is not available in Go stdlib; look it up
		// from /etc/passwd via the username.
		if out, err := exec.Command("getent", "passwd", u.Username).Output(); err == nil {
			// getent returns: username:x:uid:gid:gecos:home:shell
			fields := splitColon(string(out))
			if len(fields) >= 7 && fields[6] != "" {
				shell = fields[6]
			}
		}
	}

	cmd := exec.Command(shell, "-l")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "HOME="+home)
	if cwd != "" {
		cmd.Dir = cwd
	} else {
		cmd.Dir = home
	}

	// Start the command with a PTY. pty.Start both forks the process and
	// attaches it to a new pseudo-terminal, returning the master fd.
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}

	sess := &LocalSession{
		ID:        uuid.New().String(),
		CreatedAt: time.Now().UTC(),
		ptmx:      ptmx,
		cmd:       cmd,
		buf:       NewRingBuffer(m.bufSize),
		subs:      make(map[chan []byte]struct{}),
		done:      make(chan struct{}),
	}

	m.mu.Lock()
	m.sessions[sess.ID] = sess
	m.mu.Unlock()

	go m.pumpOutput(sess)

	return sess, nil
}

// GetSession returns the session with the given ID, or nil if not found.
func (m *LocalManager) GetSession(id string) *LocalSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// Write sends data to the PTY's stdin (i.e. the shell). It is safe to call
// concurrently from multiple WebSocket pumps.
func (m *LocalManager) Write(id string, data []byte) error {
	m.mu.Lock()
	sess := m.sessions[id]
	m.mu.Unlock()
	if sess == nil {
		return ErrSessionNotFound
	}
	_, err := sess.ptmx.Write(data)
	return err
}

// Resize sends a SIGWINCH to the PTY process with the new terminal dimensions.
// Called when the browser xterm.js reports a resize event.
func (m *LocalManager) Resize(id string, cols, rows uint16) error {
	m.mu.Lock()
	sess := m.sessions[id]
	m.mu.Unlock()
	if sess == nil {
		return ErrSessionNotFound
	}
	return pty.Setsize(sess.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

// Subscribe returns a buffered channel that receives output bytes from the
// session. The caller must call Unsubscribe when done. If the session does not
// exist, a nil channel is returned.
func (m *LocalManager) Subscribe(id string) chan []byte {
	m.mu.Lock()
	sess := m.sessions[id]
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

// Unsubscribe removes a subscriber channel registered with Subscribe.
func (m *LocalManager) Unsubscribe(id string, ch chan []byte) {
	m.mu.Lock()
	sess := m.sessions[id]
	m.mu.Unlock()
	if sess == nil {
		return
	}
	sess.mu.Lock()
	delete(sess.subs, ch)
	sess.mu.Unlock()
}

// ReplayBuffer returns a copy of the ring buffer contents for the session,
// used to replay past output when a client reconnects.
func (m *LocalManager) ReplayBuffer(id string) []byte {
	m.mu.Lock()
	sess := m.sessions[id]
	m.mu.Unlock()
	if sess == nil {
		return nil
	}
	return sess.buf.Bytes()
}

// Done returns the channel that is closed when the session ends (shell exits).
func (m *LocalManager) Done(id string) <-chan struct{} {
	m.mu.Lock()
	sess := m.sessions[id]
	m.mu.Unlock()
	if sess == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return sess.done
}

// Close terminates the shell process and releases PTY resources for the session.
func (m *LocalManager) Close(id string) {
	m.mu.Lock()
	sess := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if sess == nil {
		return
	}
	sess.closeOnce.Do(func() {
		sess.ptmx.Close()
		sess.cmd.Process.Kill()
		close(sess.done)
	})
}

// pumpOutput reads from the PTY master in a goroutine, writes to the ring
// buffer, and broadcasts to all subscribers. It runs until the PTY closes
// (shell exits). Mirrors pumpOutput from manager.go.
func (m *LocalManager) pumpOutput(sess *LocalSession) {
	buf := make([]byte, 4096)
	for {
		n, err := sess.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			sess.buf.Write(chunk)

			sess.mu.Lock()
			for ch := range sess.subs {
				select {
				case ch <- chunk:
				default:
					// Slow subscriber — evict rather than stall.
					delete(sess.subs, ch)
					close(ch)
				}
			}
			sess.mu.Unlock()
		}
		if err != nil {
			// PTY closed — shell exited.
			if err != io.EOF {
				_ = err // normal on process exit
			}
			break
		}
	}
	// Clean up: close all subscriber channels and mark session done.
	sess.mu.Lock()
	for ch := range sess.subs {
		close(ch)
	}
	sess.subs = make(map[chan []byte]struct{})
	sess.mu.Unlock()

	sess.closeOnce.Do(func() {
		sess.ptmx.Close()
		sess.cmd.Wait()
		close(sess.done)
	})

	m.mu.Lock()
	delete(m.sessions, sess.ID)
	m.mu.Unlock()
}

// splitColon splits a string by ":" and trims trailing newlines from the last
// field. Used to parse /etc/passwd lines from getent output.
func splitColon(s string) []string {
	var fields []string
	for _, f := range strings.Split(strings.TrimRight(s, "\n"), ":") {
		fields = append(fields, f)
	}
	return fields
}
