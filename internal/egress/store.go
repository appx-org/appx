// Package egress implements the egress CONNECT proxy, allowlist management, and
// connection logging. It controls which external hosts the Pi agent can reach by
// intercepting HTTP CONNECT requests on 127.0.0.1:9080.
package egress

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DefaultAllowlist is the set of host:port entries permitted when no custom
// allowlist has been configured. Contains the minimum set required for basic
// agent functionality: model provider APIs and common package registries agents
// use to build projects.
var DefaultAllowlist = []string{
	// AI / agent infrastructure
	"api.anthropic.com:443",
	"api.openai.com:443",
	// Go modules
	"proxy.golang.org:443",
	"sum.golang.org:443",
	// Node / Python packages
	"registry.npmjs.org:443",
	// AWS Bedrock (model inference). Wildcard matches per-region runtime
	// endpoints, e.g. bedrock-runtime.us-east-1.amazonaws.com.
	"bedrock-runtime.*.amazonaws.com:443",
}

const settingKey = "egress_allowlist"

// LogEntry represents a single egress connection attempt.
type LogEntry struct {
	ID          int       `json:"id"`
	Destination string    `json:"destination"`
	Port        int       `json:"port"`
	Allowed     bool      `json:"allowed"`
	Timestamp   time.Time `json:"timestamp"`
}

// Store handles egress log persistence and allowlist management.
type Store struct {
	db        *sql.DB
	mu        sync.RWMutex
	allowlist map[string]bool
}

// NewStore creates an egress Store backed by the given database. Loads the
// allowlist from settings on creation; falls back to DefaultAllowlist.
func NewStore(db *sql.DB) *Store {
	s := &Store{db: db}
	s.reloadAllowlist()
	return s
}

// LogEntry records a connection attempt in the egress_log table.
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
// along with the total count.
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

	entries := []LogEntry{}
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.ID, &e.Destination, &e.Port, &e.Allowed, &e.Timestamp); err != nil {
			return nil, 0, fmt.Errorf("scan egress log: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, total, rows.Err()
}

// GetAllowlist returns the current allowlist as a slice of "host:port" strings.
func (s *Store) GetAllowlist() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]string, 0, len(s.allowlist))
	for entry := range s.allowlist {
		result = append(result, entry)
	}
	return result
}

// SetAllowlist replaces the allowlist and persists it to the settings table.
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

// AddToAllowlist adds a single host:port entry to the allowlist and persists
// it. No-op if the entry already exists.
func (s *Store) AddToAllowlist(host string, port int) error {
	entry := fmt.Sprintf("%s:%d", host, port)
	s.mu.Lock()
	if s.allowlist[entry] {
		s.mu.Unlock()
		return nil
	}
	s.allowlist[entry] = true
	s.mu.Unlock()

	current := s.GetAllowlist()
	return s.SetAllowlist(current)
}

// IsAllowed checks whether host:port is in the allowlist. Exact entries are an
// O(1) map lookup; entries containing a '*' are matched as DNS wildcards where
// each '*' matches a single label (a run of non-dot characters), like a
// wildcard TLS cert (e.g. "bedrock-runtime.*.amazonaws.com" matches
// "bedrock-runtime.us-east-1.amazonaws.com" but not "...a.b.amazonaws.com").
func (s *Store) IsAllowed(host string, port int) bool {
	key := fmt.Sprintf("%s:%d", host, port)
	portSuffix := fmt.Sprintf(":%d", port)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.allowlist[key] {
		return true
	}
	// Fall back to wildcard entries (same port).
	for entry := range s.allowlist {
		if !strings.Contains(entry, "*") || !strings.HasSuffix(entry, portSuffix) {
			continue
		}
		pattern := strings.TrimSuffix(entry, portSuffix)
		if wildcardHostMatch(pattern, host) {
			return true
		}
	}
	return false
}

// wildcardHostMatch reports whether host matches a DNS pattern where '*' within
// a label matches any run of non-dot characters. Pattern and host must have the
// same number of labels, so a wildcard can never span a dot (this keeps
// "bedrock-runtime.*.amazonaws.com" from matching "x.amazonaws.com.evil.com").
func wildcardHostMatch(pattern, host string) bool {
	pp := strings.Split(pattern, ".")
	hp := strings.Split(host, ".")
	if len(pp) != len(hp) {
		return false
	}
	for i := range pp {
		if !labelGlob(pp[i], hp[i]) {
			return false
		}
	}
	return true
}

// labelGlob matches a single DNS label against a pattern label in which '*'
// matches zero or more characters (no dots — labels never contain dots).
func labelGlob(pattern, label string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == label
	}
	parts := strings.Split(pattern, "*")
	// Anchor the first and last parts; everything between must appear in order.
	if !strings.HasPrefix(label, parts[0]) {
		return false
	}
	if !strings.HasSuffix(label, parts[len(parts)-1]) {
		return false
	}
	rest := label[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		idx := strings.Index(rest, part)
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(part):]
	}
	return true
}

// PruneLog deletes egress log entries older than maxAge. Called periodically
// by the server to prevent unbounded table growth.
func (s *Store) PruneLog(maxAge time.Duration) error {
	cutoff := time.Now().Add(-maxAge)
	_, err := s.db.Exec("DELETE FROM egress_log WHERE timestamp < ?", cutoff)
	if err != nil {
		return fmt.Errorf("prune egress log: %w", err)
	}
	return nil
}

// reloadAllowlist reads the allowlist from settings, falling back to defaults.
func (s *Store) reloadAllowlist() {
	var val string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", settingKey).Scan(&val)

	s.mu.Lock()
	defer s.mu.Unlock()

	load := func(entries []string) {
		s.allowlist = make(map[string]bool, len(entries))
		for _, e := range entries {
			s.allowlist[e] = true
		}
	}

	if err != nil || val == "" {
		load(DefaultAllowlist)
		return
	}
	var entries []string
	if err := json.Unmarshal([]byte(val), &entries); err != nil {
		load(DefaultAllowlist)
		return
	}
	load(entries)
}
