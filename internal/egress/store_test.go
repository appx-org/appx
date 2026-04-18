package egress

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

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
		t.Fatalf("expected 2 total, got %d", total)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	// Most recent first
	if entries[0].Destination != "evil.com" {
		t.Errorf("expected evil.com first, got %s", entries[0].Destination)
	}
	if entries[0].Allowed {
		t.Error("expected evil.com blocked")
	}
	if !entries[1].Allowed {
		t.Error("expected api.anthropic.com allowed")
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
		t.Errorf("expected 3 at offset 3, got %d", len(entries2))
	}
	if entries[0].ID == entries2[0].ID {
		t.Error("expected different entries at different offsets")
	}
}

func TestGetAllowlist_Default(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	list := s.GetAllowlist()
	if len(list) != len(DefaultAllowlist) {
		t.Fatalf("expected %d default entries, got %d: %v", len(DefaultAllowlist), len(list), list)
	}
	expected := map[string]bool{}
	for _, e := range DefaultAllowlist {
		expected[e] = true
	}
	for _, e := range list {
		if !expected[e] {
			t.Errorf("unexpected default entry: %s", e)
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
		t.Fatalf("expected 2, got %d", len(list))
	}
}

func TestPruneLog(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	// Insert an old entry directly with a timestamp 60 days ago.
	db.Exec("INSERT INTO egress_log (destination, port, allowed, timestamp) VALUES (?, ?, ?, datetime('now', '-60 days'))",
		"old.example.com", 443, true)
	// Insert a recent entry.
	s.LogEntry("new.example.com", 443, true)

	// Prune entries older than 30 days.
	if err := s.PruneLog(30 * 24 * time.Hour); err != nil {
		t.Fatalf("PruneLog: %v", err)
	}

	entries, total, err := s.ListLog(50, 0)
	if err != nil {
		t.Fatalf("ListLog: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 entry after prune, got %d", total)
	}
	if len(entries) != 1 || entries[0].Destination != "new.example.com" {
		t.Errorf("expected only new.example.com to remain, got %v", entries)
	}
}

func TestIsAllowed(t *testing.T) {
	db := setupTestDB(t)
	s := NewStore(db)

	if !s.IsAllowed("api.anthropic.com", 443) {
		t.Error("expected api.anthropic.com:443 allowed by default")
	}
	if s.IsAllowed("evil.com", 443) {
		t.Error("expected evil.com:443 blocked")
	}
	if s.IsAllowed("api.anthropic.com", 80) {
		t.Error("expected api.anthropic.com:80 blocked (wrong port)")
	}
}
