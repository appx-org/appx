package project

import (
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

// setupTestDB creates an in-memory SQLite database with the full project schema.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)

	_, err = db.Exec(`
		CREATE TABLE projects (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE NOT NULL,
			status TEXT DEFAULT 'stopped',
			container_id TEXT,
			internal_port INTEGER,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			network_id TEXT,
			image_name TEXT,
			last_error TEXT,
			resources TEXT,
			container_secret TEXT,
			assigned_port INTEGER
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_assigned_port ON projects(assigned_port) WHERE assigned_port IS NOT NULL;
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestCreate_ValidName(t *testing.T) {
	store := NewStore(setupTestDB(t))

	p, err := store.Create("my-app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "my-app" {
		t.Errorf("expected name my-app, got %s", p.Name)
	}
	if p.AssignedPort != PortRangeStart {
		t.Errorf("expected port %d, got %d", PortRangeStart, p.AssignedPort)
	}
	if p.Status != StatusStopped {
		t.Errorf("expected status stopped, got %s", p.Status)
	}
	if p.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestCreate_AutoAssignsPorts(t *testing.T) {
	store := NewStore(setupTestDB(t))

	p1, err := store.Create("app-a")
	if err != nil {
		t.Fatalf("create app-a: %v", err)
	}
	if p1.AssignedPort != 10000 {
		t.Errorf("expected 10000, got %d", p1.AssignedPort)
	}

	p2, err := store.Create("app-b")
	if err != nil {
		t.Fatalf("create app-b: %v", err)
	}
	if p2.AssignedPort != 10001 {
		t.Errorf("expected 10001, got %d", p2.AssignedPort)
	}
}

func TestCreate_DuplicateName(t *testing.T) {
	store := NewStore(setupTestDB(t))

	_, err := store.Create("my-app")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err = store.Create("my-app")
	if err != ErrDuplicateName {
		t.Errorf("expected ErrDuplicateName, got %v", err)
	}
}

func TestCreate_InvalidNames(t *testing.T) {
	store := NewStore(setupTestDB(t))

	invalid := []string{"", "a", "A", "My-App", "123-app", "-app", "app_test", "app.test", "app test"}
	for _, name := range invalid {
		_, err := store.Create(name)
		if err != ErrInvalidName {
			t.Errorf("name %q: expected ErrInvalidName, got %v", name, err)
		}
	}
}

func TestCreate_ValidNames(t *testing.T) {
	store := NewStore(setupTestDB(t))

	valid := []string{"ab", "my-app", "test-project-123", "a0"}
	for _, name := range valid {
		_, err := store.Create(name)
		if err != nil {
			t.Errorf("name %q: unexpected error: %v", name, err)
		}
	}
}

func TestList_Empty(t *testing.T) {
	store := NewStore(setupTestDB(t))

	projects, err := store.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected 0, got %d", len(projects))
	}
	if projects == nil {
		t.Error("expected non-nil slice")
	}
}

func TestList_Multiple(t *testing.T) {
	store := NewStore(setupTestDB(t))
	store.Create("app-a")
	store.Create("app-b")

	projects, err := store.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 2 {
		t.Errorf("expected 2, got %d", len(projects))
	}
}

func TestGet_NotFound(t *testing.T) {
	store := NewStore(setupTestDB(t))
	_, err := store.Get("nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGet_Found(t *testing.T) {
	store := NewStore(setupTestDB(t))
	created, _ := store.Create("my-app")
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AssignedPort != PortRangeStart {
		t.Errorf("expected port %d, got %d", PortRangeStart, got.AssignedPort)
	}
}

func TestDelete(t *testing.T) {
	store := NewStore(setupTestDB(t))
	p, _ := store.Create("my-app")
	if err := store.Delete(p.ID); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err := store.Get(p.ID)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestDelete_NotFound(t *testing.T) {
	store := NewStore(setupTestDB(t))
	if err := store.Delete("nonexistent"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete_FreesPort(t *testing.T) {
	store := NewStore(setupTestDB(t))
	p1, _ := store.Create("app-a")
	store.Create("app-b")
	store.Delete(p1.ID)

	p3, err := store.Create("app-c")
	if err != nil {
		t.Fatalf("create app-c: %v", err)
	}
	if p3.AssignedPort != PortRangeStart {
		t.Errorf("expected port %d (reused), got %d", PortRangeStart, p3.AssignedPort)
	}
}

func TestSetError(t *testing.T) {
	store := NewStore(setupTestDB(t))
	p, _ := store.Create("my-app")
	store.SetError(p.ID, "build failed")

	got, _ := store.Get(p.ID)
	if got.Status != StatusError {
		t.Errorf("expected error, got %s", got.Status)
	}
	if got.LastError != "build failed" {
		t.Errorf("expected 'build failed', got %q", got.LastError)
	}
}

func TestGetByName(t *testing.T) {
	store := NewStore(setupTestDB(t))
	_, err := store.Create("my-app")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	p, err := store.GetByName("my-app")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if p.Name != "my-app" {
		t.Errorf("got name %q, want %q", p.Name, "my-app")
	}

	_, err = store.GetByName("nonexistent")
	if err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestNextAvailablePort_Empty(t *testing.T) {
	store := NewStore(setupTestDB(t))
	port, err := store.nextAvailablePort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != PortRangeStart {
		t.Errorf("expected %d, got %d", PortRangeStart, port)
	}
}

func TestNextAvailablePort_GapFilling(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('a', 'app-a', 10000)")
	db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('b', 'app-b', 10002)")

	port, err := store.nextAvailablePort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 10001 {
		t.Errorf("expected 10001, got %d", port)
	}
}

func TestNextAvailablePort_Sequential(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('a', 'app-a', 10000)")
	db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('b', 'app-b', 10001)")

	port, err := store.nextAvailablePort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 10002 {
		t.Errorf("expected 10002, got %d", port)
	}
}

func TestNextAvailablePort_RangeExhausted(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	for i := PortRangeStart; i <= PortRangeEnd; i++ {
		name := fmt.Sprintf("app-%d", i)
		id := fmt.Sprintf("id-%d", i)
		_, err := db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES (?, ?, ?)", id, name, i)
		if err != nil {
			t.Fatalf("insert port %d: %v", i, err)
		}
	}

	_, err := store.nextAvailablePort()
	if err != ErrNoPortAvailable {
		t.Errorf("expected ErrNoPortAvailable, got %v", err)
	}
}

func TestCreate_ConcurrentCreates_AllGetDistinctPorts(t *testing.T) {
	db := setupTestDB(t)
	// Single connection to serialize SQLite writes like production does.
	db.SetMaxOpenConns(1)
	store := NewStore(db)

	const n = 10
	type result struct {
		p   *Project
		err error
	}
	results := make(chan result, n)

	for i := 0; i < n; i++ {
		name := fmt.Sprintf("app-%02d", i)
		go func(name string) {
			p, err := store.Create(name)
			results <- result{p, err}
		}(name)
	}

	ports := map[int]bool{}
	for i := 0; i < n; i++ {
		r := <-results
		if r.err != nil {
			t.Errorf("Create: unexpected error: %v", r.err)
			continue
		}
		if ports[r.p.AssignedPort] {
			t.Errorf("port %d assigned to multiple projects", r.p.AssignedPort)
		}
		ports[r.p.AssignedPort] = true
		if r.p.AssignedPort < PortRangeStart || r.p.AssignedPort > PortRangeEnd {
			t.Errorf("port %d out of range", r.p.AssignedPort)
		}
	}
}
