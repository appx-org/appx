# Phase 5 Step 6: Egress CONNECT Proxy

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go HTTP CONNECT proxy that controls and logs all outbound traffic from the `opencode` user. Cooperative enforcement via `HTTPS_PROXY` env var; hard enforcement via iptables (documented for the installer, not implemented here). Expose egress log and allowlist management via REST API.

**Architecture:** The CONNECT proxy listens on `127.0.0.1:9080`. For each `CONNECT host:port` request it checks the destination against a configurable allowlist stored in the `settings` table. Allowed connections are tunnelled; blocked connections get a `403 Forbidden` response. Every connection attempt (allowed or blocked) is logged to the `egress_log` table. OpenCode's systemd service sets `HTTPS_PROXY=http://127.0.0.1:9080` so all HTTPS traffic routes through the proxy cooperatively. The iptables rules (configured by the installer in Phase 6) block all outbound from the `opencode` UID except to localhost, providing kernel-level enforcement against raw-socket bypasses.

**Tech Stack:** Go 1.26, stdlib `net/http` + `net`, SQLite, `database/sql`

**Reference:** `internal/auth/store.go` for settings table access patterns, `internal/server/settings_handlers.go` for handler conventions.

---

### Task 1: Add migration — `allowed` column on `egress_log`

The `egress_log` table exists from migration 1 but lacks an `allowed` column to distinguish permitted vs blocked connections.

**Files:**
- Create: `internal/db/migrations/000004_egress_allowed.up.sql`
- Create: `internal/db/migrations/000004_egress_allowed.down.sql`

- [ ] **Step 1: Write the up migration**

Create `internal/db/migrations/000004_egress_allowed.up.sql`:

```sql
ALTER TABLE egress_log ADD COLUMN allowed BOOLEAN NOT NULL DEFAULT 1;
```

- [ ] **Step 2: Write the down migration**

Create `internal/db/migrations/000004_egress_allowed.down.sql`:

```sql
ALTER TABLE egress_log DROP COLUMN allowed;
```

- [ ] **Step 3: Verify migration applies on a fresh database**

```bash
rm -rf /tmp/appx-test-egress && mkdir /tmp/appx-test-egress
go test ./internal/db/ -run TestMigrations -v
```

If there is no `TestMigrations` test, verify manually by checking that `db.Open` succeeds with a temp data dir.

- [ ] **Step 4: Commit**

```bash
git add internal/db/migrations/000004_egress_allowed.up.sql internal/db/migrations/000004_egress_allowed.down.sql
git commit -m "migration: add allowed column to egress_log table"
```

---

### Task 2: Egress store — `internal/egress/store.go`

**Files:**
- Create: `internal/egress/store.go`
- Create: `internal/egress/store_test.go`

- [ ] **Step 1: Write store tests first**

Create `internal/egress/store_test.go`:

```go
package egress

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// setupTestDB creates an in-memory SQLite database with the egress_log table
// schema matching the production schema after migration 4.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE egress_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id TEXT,
			destination TEXT,
			port INTEGER,
			allowed BOOLEAN NOT NULL DEFAULT 1,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE settings (
			key TEXT PRIMARY KEY,
			value TEXT
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestLogEntry(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	if err := s.LogEntry("api.anthropic.com", 443, true); err != nil {
		t.Fatalf("LogEntry: %v", err)
	}
	if err := s.LogEntry("evil.com", 443, false); err != nil {
		t.Fatalf("LogEntry blocked: %v", err)
	}

	entries, total, err := s.ListLog(50, 0)
	if err != nil {
		t.Fatalf("ListLog: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 total entries, got %d", total)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Most recent first
	if entries[0].Destination != "evil.com" {
		t.Errorf("expected evil.com first (most recent), got %s", entries[0].Destination)
	}
	if entries[0].Allowed {
		t.Error("expected evil.com to be blocked")
	}
	if !entries[1].Allowed {
		t.Error("expected api.anthropic.com to be allowed")
	}
}

func TestListLog_Pagination(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	for i := 0; i < 10; i++ {
		s.LogEntry("host.com", 443, true)
	}

	entries, total, err := s.ListLog(3, 0)
	if err != nil {
		t.Fatalf("ListLog: %v", err)
	}
	if total != 10 {
		t.Errorf("expected total 10, got %d", total)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 entries, got %d", len(entries))
	}

	entries2, _, err := s.ListLog(3, 3)
	if err != nil {
		t.Fatalf("ListLog offset: %v", err)
	}
	if len(entries2) != 3 {
		t.Errorf("expected 3 entries at offset 3, got %d", len(entries2))
	}
	// Ensure different entries (different IDs since ordered by id DESC)
	if entries[0].ID == entries2[0].ID {
		t.Error("expected different entries at different offsets")
	}
}

func TestGetAllowlist_Default(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	list := s.GetAllowlist()
	if len(list) != 3 {
		t.Fatalf("expected 3 default entries, got %d: %v", len(list), list)
	}

	expected := map[string]bool{
		"api.anthropic.com:443":  true,
		"registry.npmjs.org:443": true,
		"proxy.golang.org:443":   true,
	}
	for _, entry := range list {
		if !expected[entry] {
			t.Errorf("unexpected default entry: %s", entry)
		}
	}
}

func TestSetAllowlist(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	custom := []string{"api.anthropic.com:443", "example.com:8080"}
	if err := s.SetAllowlist(custom); err != nil {
		t.Fatalf("SetAllowlist: %v", err)
	}

	list := s.GetAllowlist()
	if len(list) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(list))
	}
}

func TestIsAllowed(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	if !s.IsAllowed("api.anthropic.com", 443) {
		t.Error("expected api.anthropic.com:443 to be allowed by default")
	}
	if s.IsAllowed("evil.com", 443) {
		t.Error("expected evil.com:443 to be blocked")
	}
	if !s.IsAllowed("registry.npmjs.org", 443) {
		t.Error("expected registry.npmjs.org:443 to be allowed")
	}
	// Wrong port
	if s.IsAllowed("api.anthropic.com", 80) {
		t.Error("expected api.anthropic.com:80 to be blocked (wrong port)")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/egress/ -v 2>&1 | head -5
```

Expected: FAIL (package does not exist)

- [ ] **Step 3: Write the store**

Create `internal/egress/store.go`:

```go
// Package egress implements the egress CONNECT proxy, allowlist management, and
// connection logging. It controls which external hosts the opencode agent can
// reach by intercepting HTTP CONNECT requests on 127.0.0.1:9080. Every
// connection attempt is logged to the egress_log table with its allow/block
// verdict. The allowlist is stored as a JSON array in the settings table under
// the "egress_allowlist" key.
package egress

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// DefaultAllowlist is the set of host:port entries permitted when no custom
// allowlist has been configured. These are the minimum destinations required
// for Claude Code (Anthropic API), npm (package installs), and Go modules.
var DefaultAllowlist = []string{
	"api.anthropic.com:443",
	"registry.npmjs.org:443",
	"proxy.golang.org:443",
}

// settingKey is the settings table key under which the allowlist JSON is stored.
const settingKey = "egress_allowlist"

// LogEntry represents a single egress connection attempt as stored in the
// egress_log table. Each entry records the destination host, port, whether the
// connection was allowed, and the timestamp.
type LogEntry struct {
	ID          int       `json:"id"`
	Destination string    `json:"destination"`
	Port        int       `json:"port"`
	Allowed     bool      `json:"allowed"`
	Timestamp   time.Time `json:"timestamp"`
}

// Store handles egress log persistence and allowlist management using the
// egress_log and settings tables in SQLite. It caches the parsed allowlist
// in memory for fast lookup on every CONNECT request, reloading from the
// database only when SetAllowlist is called.
type Store struct {
	db *sql.DB

	mu        sync.RWMutex
	allowlist map[string]bool // cached set of "host:port" strings
}

// NewStore creates an egress Store backed by the given database connection.
// It loads the allowlist from the settings table into memory. If no allowlist
// is stored, the DefaultAllowlist is used.
func NewStore(db *sql.DB) *Store {
	s := &Store{db: db}
	s.reloadAllowlist()
	return s
}

// LogEntry records a connection attempt in the egress_log table. Called by the
// CONNECT proxy for every incoming request, whether allowed or blocked.
func (s *Store) LogEntry(destination string, port int, allowed bool) error {
	_, err := s.db.Exec(
		"INSERT INTO egress_log (destination, port, allowed) VALUES (?, ?, ?)",
		destination, port, allowed,
	)
	if err != nil {
		return fmt.Errorf("insert egress log: %w", err)
	}
	return nil
}

// ListLog returns a page of egress log entries ordered by most recent first,
// along with the total count of entries. Used by the GET /api/egress/log
// endpoint for paginated display.
func (s *Store) ListLog(limit, offset int) ([]LogEntry, int, error) {
	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM egress_log").Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count egress log: %w", err)
	}

	rows, err := s.db.Query(
		"SELECT id, destination, port, allowed, timestamp FROM egress_log ORDER BY id DESC LIMIT ? OFFSET ?",
		limit, offset,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("query egress log: %w", err)
	}
	defer rows.Close()

	var entries []LogEntry
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.Destination, &e.Port, &e.Allowed, &e.Timestamp); err != nil {
			return nil, 0, fmt.Errorf("scan egress log: %w", err)
		}
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []LogEntry{}
	}
	return entries, total, rows.Err()
}

// GetAllowlist returns the current allowlist as a slice of "host:port" strings.
// If no custom allowlist has been set, returns DefaultAllowlist.
func (s *Store) GetAllowlist() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]string, 0, len(s.allowlist))
	for entry := range s.allowlist {
		result = append(result, entry)
	}
	return result
}

// SetAllowlist replaces the current allowlist with the given entries and
// persists it to the settings table as a JSON array. The in-memory cache is
// updated atomically so the CONNECT proxy sees the new list immediately.
func (s *Store) SetAllowlist(entries []string) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("marshal allowlist: %w", err)
	}

	_, err = s.db.Exec(
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?",
		settingKey, string(data), string(data),
	)
	if err != nil {
		return fmt.Errorf("save allowlist: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.allowlist = make(map[string]bool, len(entries))
	for _, e := range entries {
		s.allowlist[e] = true
	}
	return nil
}

// IsAllowed checks whether the given host:port is in the allowlist. Called by
// the CONNECT proxy on every incoming tunnel request. Uses the in-memory cache
// for O(1) lookup without hitting the database.
func (s *Store) IsAllowed(host string, port int) bool {
	key := fmt.Sprintf("%s:%d", host, port)
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allowlist[key]
}

// reloadAllowlist reads the allowlist from the settings table and populates the
// in-memory cache. Called once at construction and after each SetAllowlist.
func (s *Store) reloadAllowlist() {
	var val string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", settingKey).Scan(&val)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil || val == "" {
		// No custom allowlist — use defaults
		s.allowlist = make(map[string]bool, len(DefaultAllowlist))
		for _, e := range DefaultAllowlist {
			s.allowlist[e] = true
		}
		return
	}

	var entries []string
	if err := json.Unmarshal([]byte(val), &entries); err != nil {
		// Corrupt JSON — fall back to defaults
		s.allowlist = make(map[string]bool, len(DefaultAllowlist))
		for _, e := range DefaultAllowlist {
			s.allowlist[e] = true
		}
		return
	}

	s.allowlist = make(map[string]bool, len(entries))
	for _, e := range entries {
		s.allowlist[e] = true
	}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/egress/ -v
```

Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add internal/egress/store.go internal/egress/store_test.go
git commit -m "feat(egress): add Store for egress log and allowlist management"
```

---

### Task 3: CONNECT proxy — `internal/egress/proxy.go`

**Files:**
- Create: `internal/egress/proxy.go`
- Create: `internal/egress/proxy_test.go`

- [ ] **Step 1: Write proxy tests first**

Create `internal/egress/proxy_test.go`:

```go
package egress

import (
	"bufio"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupProxyTest creates a Store with an in-memory DB and starts a Proxy on a
// random port. Returns the proxy, its address, and a cleanup function.
func setupProxyTest(t *testing.T) (*Proxy, string, *Store) {
	t.Helper()
	db := setupTestDB(t)
	store := NewStore(db)

	p := NewProxy(store)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go p.Serve(ln)

	return p, ln.Addr().String(), store
}

// startEchoServer starts a TCP server that accepts one connection, reads a
// line, echoes it back, and closes. Returns its address.
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		conn.Write(buf[:n])
	}()

	return ln.Addr().String()
}

func TestProxy_AllowedConnect(t *testing.T) {
	_, proxyAddr, store := setupProxyTest(t)

	// Start a backend server and add it to the allowlist
	echoAddr := startEchoServer(t)
	host, port, _ := net.SplitHostPort(echoAddr)
	entry := fmt.Sprintf("%s:%s", host, port)
	store.SetAllowlist([]string{entry})

	// Send CONNECT through the proxy
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr)

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Now the tunnel is open — send data through
	fmt.Fprintf(conn, "hello")
	buf := make([]byte, 5)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Errorf("expected 'hello', got %q", string(buf[:n]))
	}
}

func TestProxy_BlockedConnect(t *testing.T) {
	_, proxyAddr, _ := setupProxyTest(t)

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// evil.com:443 is not in the default allowlist
	fmt.Fprintf(conn, "CONNECT evil.com:443 HTTP/1.1\r\nHost: evil.com:443\r\n\r\n")

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestProxy_LogsEntries(t *testing.T) {
	_, proxyAddr, store := setupProxyTest(t)

	// Make a blocked request
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "CONNECT blocked.com:443 HTTP/1.1\r\nHost: blocked.com:443\r\n\r\n")
	http.ReadResponse(bufio.NewReader(conn), nil)
	conn.Close()

	// Give the goroutine a moment to write the log
	time.Sleep(50 * time.Millisecond)

	entries, total, err := store.ListLog(50, 0)
	if err != nil {
		t.Fatalf("ListLog: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected 1 log entry, got %d", total)
	}
	if entries[0].Destination != "blocked.com" {
		t.Errorf("expected destination blocked.com, got %s", entries[0].Destination)
	}
	if entries[0].Allowed {
		t.Error("expected entry to be blocked")
	}
}

func TestProxy_NonConnectMethod(t *testing.T) {
	_, proxyAddr, _ := setupProxyTest(t)

	// Send a regular GET request — should be rejected
	resp, err := http.Get("http://" + proxyAddr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}
```

Note: the `TestProxy_AllowedConnect` test uses a local echo server to test end-to-end tunnelling. The `tls` import is included for future HTTPS tests but the echo test uses plain TCP.

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/egress/ -run TestProxy -v 2>&1 | head -5
```

Expected: FAIL (Proxy type not defined)

- [ ] **Step 3: Write the CONNECT proxy**

Create `internal/egress/proxy.go`:

```go
package egress

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ProxyAddr is the default listen address for the egress CONNECT proxy. It
// binds to localhost only — external clients cannot reach it. OpenCode's
// systemd service sets HTTPS_PROXY=http://127.0.0.1:9080 to route traffic
// through this proxy.
const ProxyAddr = "127.0.0.1:9080"

// tunnelTimeout is the maximum duration for a single tunnelled connection. This
// prevents leaked goroutines from connections that are never closed. 30 minutes
// is generous — most API calls complete in seconds.
const tunnelTimeout = 30 * time.Minute

// dialTimeout is how long the proxy waits when connecting to the upstream
// destination. If the destination is unreachable, the client gets a 502.
const dialTimeout = 10 * time.Second

// Proxy is an HTTP CONNECT proxy that enforces an allowlist on outbound
// connections and logs every attempt. It is the cooperative enforcement layer
// for egress control — OpenCode's HTTPS_PROXY env var points here. The hard
// enforcement layer (iptables UID-based rules) is configured by the installer
// in Phase 6.
type Proxy struct {
	store *Store
}

// NewProxy creates a CONNECT proxy backed by the given egress store for
// allowlist lookups and connection logging.
func NewProxy(store *Store) *Proxy {
	return &Proxy{store: store}
}

// ListenAndServe starts the CONNECT proxy on the given address. It blocks
// until the listener is closed or a fatal error occurs. Called from main.go
// in a goroutine during server startup.
func (p *Proxy) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("egress proxy listen: %w", err)
	}
	log.Printf("Egress CONNECT proxy listening on %s", addr)
	return p.Serve(ln)
}

// Serve accepts connections on the given listener and handles each one in a
// goroutine. Factored out of ListenAndServe for testability — tests pass a
// net.Listener on a random port.
func (p *Proxy) Serve(ln net.Listener) error {
	srv := &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return srv.Serve(ln)
}

// ServeHTTP implements http.Handler. It only accepts CONNECT requests — all
// other methods receive 405 Method Not Allowed. For each CONNECT request it
// extracts the destination host:port, checks the allowlist, logs the attempt,
// and either tunnels the connection or returns 403 Forbidden.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT is supported", http.StatusMethodNotAllowed)
		return
	}

	host, portStr, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "invalid host:port", http.StatusBadRequest)
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	allowed := p.store.IsAllowed(host, port)

	// Log asynchronously to avoid slowing down the tunnel setup. Errors are
	// logged but do not block the connection decision.
	go func() {
		if err := p.store.LogEntry(host, port, allowed); err != nil {
			log.Printf("egress: failed to log entry for %s:%d: %v", host, port, err)
		}
	}()

	if !allowed {
		log.Printf("egress: BLOCKED %s:%d", host, port)
		http.Error(w, "destination not in allowlist", http.StatusForbidden)
		return
	}

	log.Printf("egress: ALLOWED %s:%d", host, port)

	// Connect to the upstream destination
	destConn, err := net.DialTimeout("tcp", r.Host, dialTimeout)
	if err != nil {
		http.Error(w, "failed to connect to destination", http.StatusBadGateway)
		return
	}

	// Hijack the client connection to get the raw TCP socket
	hj, ok := w.(http.Hijacker)
	if !ok {
		destConn.Close()
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hj.Hijack()
	if err != nil {
		destConn.Close()
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}

	// Send 200 Connection Established to the client
	fmt.Fprintf(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	// Set deadlines to prevent goroutine leaks
	deadline := time.Now().Add(tunnelTimeout)
	clientConn.SetDeadline(deadline)
	destConn.SetDeadline(deadline)

	// Bidirectional tunnel
	go func() {
		io.Copy(destConn, clientConn)
		destConn.Close()
	}()
	go func() {
		io.Copy(clientConn, destConn)
		clientConn.Close()
	}()
}

// parseHostPort splits a "host:port" string, handling edge cases like IPv6
// addresses. This is a convenience wrapper around net.SplitHostPort.
func parseHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// Maybe no port — try treating the whole thing as a host
		if !strings.Contains(addr, ":") {
			return addr, 443, nil // default HTTPS port
		}
		return "", 0, fmt.Errorf("parse host:port %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("parse port %q: %w", portStr, err)
	}
	return host, port, nil
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./internal/egress/ -v -count=1
```

Expected: ALL PASS

- [ ] **Step 5: Remove unused import if needed**

If `tls` or `crypto/tls` is unused in proxy_test.go, remove it. Check:

```bash
go vet ./internal/egress/
```

- [ ] **Step 6: Commit**

```bash
git add internal/egress/proxy.go internal/egress/proxy_test.go
git commit -m "feat(egress): add HTTP CONNECT proxy with allowlist enforcement"
```

---

### Task 4: Egress API handlers — `internal/server/egress_handlers.go`

**Files:**
- Create: `internal/server/egress_handlers.go`

- [ ] **Step 1: Write tests first in `router_test.go`**

Add the following tests to `internal/server/router_test.go`. First, update `setupTest` to create the `egress_log` table and pass the egress store to the router. The exact changes depend on the current state of `setupTest` after Step 1 runs, but the additions are:

In the `setupTest` SQL schema, add:

```sql
CREATE TABLE egress_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT,
    destination TEXT,
    port INTEGER,
    allowed BOOLEAN NOT NULL DEFAULT 1,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

Then create the egress store and pass it to `NewRouter`:

```go
egressStore := egress.NewStore(db)
return NewRouter(a, pm, webFS, egressStore), store, db
```

(Add `"github.com/neuromaxer/appx/internal/egress"` to imports.)

Add these tests:

```go
func TestGetEgressLog_Empty(t *testing.T) {
	handler, store, _ := setupTest(t)
	req := authedRequest(t, store, "GET", "/api/egress/log", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Entries []any `json:"entries"`
		Total   int   `json:"total"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Total != 0 {
		t.Errorf("expected total 0, got %d", resp.Total)
	}
	if len(resp.Entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(resp.Entries))
	}
}

func TestGetEgressLog_Unauthenticated(t *testing.T) {
	handler, _, _ := setupTest(t)
	req := httptest.NewRequest("GET", "/api/egress/log", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestGetAllowlist(t *testing.T) {
	handler, store, _ := setupTest(t)
	req := authedRequest(t, store, "GET", "/api/egress/allowlist", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Entries []string `json:"entries"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Entries) != 3 {
		t.Errorf("expected 3 default entries, got %d", len(resp.Entries))
	}
}

func TestPutAllowlist(t *testing.T) {
	handler, store, _ := setupTest(t)
	req := authedRequest(t, store, "PUT", "/api/egress/allowlist",
		`{"entries":["api.anthropic.com:443","custom.example.com:8080"]}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the change took effect
	req2 := authedRequest(t, store, "GET", "/api/egress/allowlist", "")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	var resp struct {
		Entries []string `json:"entries"`
	}
	json.NewDecoder(w2.Body).Decode(&resp)
	if len(resp.Entries) != 2 {
		t.Errorf("expected 2 entries after update, got %d", len(resp.Entries))
	}
}

func TestPutAllowlist_EmptyArray(t *testing.T) {
	handler, store, _ := setupTest(t)
	req := authedRequest(t, store, "PUT", "/api/egress/allowlist", `{"entries":[]}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty allowlist, got %d", w.Code)
	}
}

func TestPutAllowlist_InvalidFormat(t *testing.T) {
	handler, store, _ := setupTest(t)
	// Missing port
	req := authedRequest(t, store, "PUT", "/api/egress/allowlist",
		`{"entries":["api.anthropic.com"]}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing port, got %d: %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 2: Write the handler file**

Create `internal/server/egress_handlers.go`:

```go
package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"

	"github.com/neuromaxer/appx/internal/egress"
)

// handleGetEgressLog returns the handler for GET /api/egress/log. It accepts
// optional query parameters "limit" (default 50, max 200) and "offset" (default
// 0) for pagination. Returns a JSON object with "entries" (array of log entries)
// and "total" (total count for pagination). This route is behind auth middleware.
func handleGetEgressLog(es *egress.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		offset := 0

		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				offset = n
			}
		}

		entries, total, err := es.ListLog(limit, offset)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]any{
			"entries": entries,
			"total":   total,
		})
	}
}

// handleGetAllowlist returns the handler for GET /api/egress/allowlist. It
// returns the current egress allowlist as a JSON object with an "entries" array
// of "host:port" strings. This route is behind auth middleware.
func handleGetAllowlist(es *egress.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"entries": es.GetAllowlist(),
		})
	}
}

// handleSetAllowlist returns the handler for PUT /api/egress/allowlist. It
// expects a JSON body with {"entries": ["host:port", ...]}. Each entry must be
// a valid host:port pair. The list must not be empty (to prevent accidentally
// blocking all traffic). Returns 200 on success with {"status": "ok"}.
// This route is behind auth middleware.
func handleSetAllowlist(es *egress.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Entries []string `json:"entries"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		if len(req.Entries) == 0 {
			http.Error(w, "allowlist must not be empty", http.StatusBadRequest)
			return
		}

		// Validate each entry is a valid host:port pair
		for _, entry := range req.Entries {
			host, portStr, err := net.SplitHostPort(entry)
			if err != nil {
				http.Error(w, "invalid entry (must be host:port): "+entry, http.StatusBadRequest)
				return
			}
			if host == "" {
				http.Error(w, "empty host in entry: "+entry, http.StatusBadRequest)
				return
			}
			if _, err := strconv.Atoi(portStr); err != nil {
				http.Error(w, "invalid port in entry: "+entry, http.StatusBadRequest)
				return
			}
		}

		if err := es.SetAllowlist(req.Entries); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]string{"status": "ok"})
	}
}
```

- [ ] **Step 3: Register routes in `router.go`**

In `internal/server/router.go`, update the `NewRouter` function signature to accept `*egress.Store` and register the three new routes on the `api` mux.

Update the signature:

```go
func NewRouter(a *auth.Auth, pm *project.Manager, tm *terminal.Manager, webFS fs.FS, cache *proxy.AssetCache, es *egress.Store) http.Handler {
```

Add the import `"github.com/neuromaxer/appx/internal/egress"` and register these routes in the `api` mux block:

```go
api.HandleFunc("GET /api/egress/log", handleGetEgressLog(es))
api.HandleFunc("GET /api/egress/allowlist", handleGetAllowlist(es))
api.HandleFunc("PUT /api/egress/allowlist", handleSetAllowlist(es))
```

**Important:** The exact `NewRouter` signature depends on whether Step 1 (de-Docker) has been applied. If the current signature is `NewRouter(a *auth.Auth, pm *project.Manager, tm *terminal.Manager, webFS fs.FS, cache *proxy.AssetCache)`, add `es *egress.Store` as the last parameter. If it has been simplified to `NewRouter(a *auth.Auth, pm *project.Manager, webFS fs.FS)`, add `es *egress.Store` after `webFS`.

- [ ] **Step 4: Update all `NewRouter` call sites**

Update `server.go` where `NewRouter` is called in the `Run` function. Add `EgressStore *egress.Store` to the `Config` struct and pass it through:

In the `Config` struct add:

```go
EgressStore *egress.Store
```

In the `Run` function, update the `NewRouter` call to pass `cfg.EgressStore`.

- [ ] **Step 5: Update `setupTest` in `router_test.go`**

Add the `egress_log` table to the test schema and create an egress store:

```go
import "github.com/neuromaxer/appx/internal/egress"
```

In the SQL schema block add:

```sql
CREATE TABLE egress_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id TEXT,
    destination TEXT,
    port INTEGER,
    allowed BOOLEAN NOT NULL DEFAULT 1,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP
);
```

After creating the auth store, add:

```go
es := egress.NewStore(db)
```

Update the `NewRouter` call to pass `es`.

- [ ] **Step 6: Run tests**

```bash
go test ./internal/server/ -v -count=1
```

Expected: ALL PASS (including new egress tests)

- [ ] **Step 7: Commit**

```bash
git add internal/server/egress_handlers.go internal/server/router.go internal/server/server.go internal/server/router_test.go
git commit -m "feat(egress): add API handlers for egress log and allowlist management"
```

---

### Task 5: Wire proxy into `cmd/appx/main.go`

**Files:**
- Modify: `cmd/appx/main.go`

- [ ] **Step 1: Import egress package and start the proxy**

Add `"github.com/neuromaxer/appx/internal/egress"` to imports.

After the `authStore` setup and before `server.Run`, add:

```go
// Start egress CONNECT proxy for outbound traffic control.
egressStore := egress.NewStore(database)
egressProxy := egress.NewProxy(egressStore)
go func() {
	if err := egressProxy.ListenAndServe(egress.ProxyAddr); err != nil {
		log.Printf("egress proxy error: %v", err)
	}
}()
```

Pass `egressStore` to the server config:

```go
EgressStore: egressStore,
```

- [ ] **Step 2: Verify compilation**

```bash
go build ./cmd/appx/
```

Expected: compiles cleanly

- [ ] **Step 3: Commit**

```bash
git add cmd/appx/main.go
git commit -m "feat(egress): wire CONNECT proxy and egress store into main startup"
```

---

### Task 6: Frontend API client — egress endpoints

**Files:**
- Modify: `web/src/api/client.ts`

- [ ] **Step 1: Add egress types and API functions**

Add the following to `web/src/api/client.ts`:

```typescript
/** A single entry in the egress connection log. */
export interface EgressLogEntry {
  id: number;
  destination: string;
  port: number;
  allowed: boolean;
  timestamp: string;
}

/** Response shape for GET /api/egress/log. */
export interface EgressLogResponse {
  entries: EgressLogEntry[];
  total: number;
}

/** Response shape for GET /api/egress/allowlist. */
export interface AllowlistResponse {
  entries: string[];
}

/**
 * Fetches the paginated egress connection log.
 * GET /api/egress/log?limit=N&offset=N
 */
export async function getEgressLog(
  limit = 50,
  offset = 0
): Promise<EgressLogResponse> {
  return request<EgressLogResponse>(
    `/api/egress/log?limit=${limit}&offset=${offset}`
  );
}

/**
 * Fetches the current egress allowlist.
 * GET /api/egress/allowlist
 */
export async function getAllowlist(): Promise<AllowlistResponse> {
  return request<AllowlistResponse>("/api/egress/allowlist");
}

/**
 * Replaces the egress allowlist with the given entries.
 * PUT /api/egress/allowlist
 */
export async function setAllowlist(
  entries: string[]
): Promise<{ status: string }> {
  return request<{ status: string }>("/api/egress/allowlist", {
    method: "PUT",
    body: JSON.stringify({ entries }),
  });
}
```

- [ ] **Step 2: Build frontend**

```bash
task lint && task web
```

Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add web/src/api/client.ts
git commit -m "feat(egress): add frontend API client for egress log and allowlist"
```

---

### Task 7: Document iptables rules for Phase 6 installer

**Files:**
- Create: `docs/architecture/egress_iptables.md`

- [ ] **Step 1: Write the iptables documentation**

Create `docs/architecture/egress_iptables.md`:

```markdown
# Egress iptables Rules (Phase 6 Installer)

## Overview

The egress CONNECT proxy (Phase 5 Step 6) provides cooperative enforcement via
`HTTPS_PROXY`. However, a compromised agent process can bypass the proxy by
making raw TCP connections. iptables rules provide kernel-level enforcement as
a second defence layer.

## Design

All outbound traffic from the `opencode` UID is blocked except to localhost.
The appx server runs as the `appx` user and is NOT affected by these rules.

## Rules

The installer (Phase 6) must apply these iptables rules:

```bash
# Get the opencode user's UID
OPENCODE_UID=$(id -u opencode)

# Allow loopback traffic (proxy is on 127.0.0.1:9080)
iptables -A OUTPUT -o lo -m owner --uid-owner $OPENCODE_UID -j ACCEPT

# Allow established connections (responses to allowed outbound)
iptables -A OUTPUT -m owner --uid-owner $OPENCODE_UID -m state --state ESTABLISHED,RELATED -j ACCEPT

# Block everything else from the opencode user
iptables -A OUTPUT -m owner --uid-owner $OPENCODE_UID -j REJECT --reject-with icmp-port-unreachable
```

## How it works

1. OpenCode runs as the `opencode` user
2. `HTTPS_PROXY=http://127.0.0.1:9080` routes HTTPS traffic to the proxy
3. The proxy runs as the `appx` user — its outbound traffic is unrestricted
4. If a compromised agent tries to connect directly to the internet:
   - The connection goes through the kernel's OUTPUT chain
   - iptables matches the `opencode` UID
   - The connection is rejected (only loopback is allowed)
5. The agent CAN connect to `127.0.0.1:9080` (loopback is allowed)
6. The proxy checks the allowlist and either tunnels or blocks

## Verification

After the installer applies the rules, verify:

```bash
# As opencode user — should succeed (goes through proxy)
sudo -u opencode curl -x http://127.0.0.1:9080 https://api.anthropic.com/health

# As opencode user — should fail (direct connection blocked by iptables)
sudo -u opencode curl --noproxy '*' https://api.anthropic.com/health

# As appx user — should succeed (not affected by rules)
sudo -u appx curl https://api.anthropic.com/health
```

## Persistence

The installer must persist these rules across reboots using `iptables-save`/`iptables-restore`
or the distribution's equivalent (e.g., `netfilter-persistent` on Debian/Ubuntu).
```

- [ ] **Step 2: Commit**

```bash
git add docs/architecture/egress_iptables.md
git commit -m "docs: add iptables egress rules reference for Phase 6 installer"
```

---

### Task 8: Full verification

- [ ] **Step 1: Run full test suite**

```bash
task test
```

Expected: ALL tests pass

- [ ] **Step 2: Run linter**

```bash
task lint
```

Expected: no errors

- [ ] **Step 3: Build**

```bash
task build
```

Expected: compiles cleanly

- [ ] **Step 4: Manual verification — start server and test proxy**

```bash
./appx -port 8443 &
sleep 2

# Test egress log endpoint (empty)
curl -k https://localhost:8443/api/egress/log -H "Cookie: appx_session=$SESSION"

# Test allowlist endpoint (defaults)
curl -k https://localhost:8443/api/egress/allowlist -H "Cookie: appx_session=$SESSION"

# Test the CONNECT proxy directly
curl -x http://127.0.0.1:9080 -I https://api.anthropic.com 2>&1 | head -5
# Expected: tunnel established (or connection refused if no internet, but 200 from proxy)

# Test blocked destination
curl -x http://127.0.0.1:9080 -I https://evil.com 2>&1 | head -5
# Expected: 403 Forbidden

kill %1
```

- [ ] **Step 5: Run full test suite one final time**

```bash
task test
```

Expected: ALL tests pass

- [ ] **Step 6: Final commit if any fixes needed**

```bash
git add -A
git commit -m "feat(egress): Phase 5 Step 6 complete — CONNECT proxy with allowlist and logging"
```
