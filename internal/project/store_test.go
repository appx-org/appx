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
			assigned_port INTEGER,
			dev_port INTEGER
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_assigned_port ON projects(assigned_port) WHERE assigned_port IS NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_dev_port ON projects(dev_port) WHERE dev_port IS NOT NULL;
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
	// PROD = lower of the pair, DEV = next.
	if p.AssignedPort != PortRangeStart {
		t.Errorf("expected prod port %d, got %d", PortRangeStart, p.AssignedPort)
	}
	if p.DevPort != PortRangeStart+1 {
		t.Errorf("expected dev port %d, got %d", PortRangeStart+1, p.DevPort)
	}
	if p.Status != StatusStopped {
		t.Errorf("expected status stopped, got %s", p.Status)
	}
	if p.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestCreate_AutoAssignsPortPairs(t *testing.T) {
	store := NewStore(setupTestDB(t))

	p1, err := store.Create("app-a")
	if err != nil {
		t.Fatalf("create app-a: %v", err)
	}
	if p1.AssignedPort != 10000 || p1.DevPort != 10001 {
		t.Errorf("app-a: expected prod=10000 dev=10001, got prod=%d dev=%d", p1.AssignedPort, p1.DevPort)
	}

	p2, err := store.Create("app-b")
	if err != nil {
		t.Fatalf("create app-b: %v", err)
	}
	if p2.AssignedPort != 10002 || p2.DevPort != 10003 {
		t.Errorf("app-b: expected prod=10002 dev=10003, got prod=%d dev=%d", p2.AssignedPort, p2.DevPort)
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

func TestValidateName_RejectsReservedDevSuffix(t *testing.T) {
	if err := ValidateName("foo-dev"); err != ErrReservedNameSuffix {
		t.Errorf("ValidateName(\"foo-dev\"): expected ErrReservedNameSuffix, got %v", err)
	}
	// A name merely containing "dev" is fine.
	if err := ValidateName("developer"); err != nil {
		t.Errorf("ValidateName(\"developer\"): unexpected %v", err)
	}
	// Create surfaces the same rejection.
	store := NewStore(setupTestDB(t))
	if _, err := store.Create("myapp-dev"); err != ErrReservedNameSuffix {
		t.Errorf("Create(\"myapp-dev\"): expected ErrReservedNameSuffix, got %v", err)
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
		t.Errorf("expected prod port %d, got %d", PortRangeStart, got.AssignedPort)
	}
	if got.DevPort != PortRangeStart+1 {
		t.Errorf("expected dev port %d, got %d", PortRangeStart+1, got.DevPort)
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

func TestDelete_FreesPortPair(t *testing.T) {
	store := NewStore(setupTestDB(t))
	p1, _ := store.Create("app-a") // 10000/10001
	store.Create("app-b")          // 10002/10003
	store.Delete(p1.ID)

	p3, err := store.Create("app-c")
	if err != nil {
		t.Fatalf("create app-c: %v", err)
	}
	// The freed pair (lowest gap) is reused.
	if p3.AssignedPort != 10000 || p3.DevPort != 10001 {
		t.Errorf("expected reused pair 10000/10001, got %d/%d", p3.AssignedPort, p3.DevPort)
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

func TestPairAllocation_GapFilling(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	// Occupy 10000 (prod) and 10003 (dev) in one row, leaving 10001/10002 free.
	db.Exec("INSERT INTO projects (id, name, assigned_port, dev_port) VALUES ('a', 'app-a', 10000, 10003)")

	p, err := store.Create("app-b")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.AssignedPort != 10001 || p.DevPort != 10002 {
		t.Errorf("expected gap-filled pair 10001/10002, got %d/%d", p.AssignedPort, p.DevPort)
	}
}

func TestPairAllocation_CapBoundary(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	// Fill 49 pairs (10000..10097), then occupy 10098 alone, leaving only the
	// single port 10199 free — not enough for a pair, so Create must fail.
	id := 0
	for port := PortRangeStart; port <= PublishedPortRangeEnd-2; port += 2 {
		_, err := db.Exec(
			"INSERT INTO projects (id, name, assigned_port, dev_port) VALUES (?, ?, ?, ?)",
			fmt.Sprintf("id-%d", id), fmt.Sprintf("app-%d", id), port, port+1,
		)
		if err != nil {
			t.Fatalf("seed pair at %d: %v", port, err)
		}
		id++
	}
	if _, err := db.Exec(
		"INSERT INTO projects (id, name, assigned_port) VALUES ('solo', 'app-solo', ?)",
		PublishedPortRangeEnd-1,
	); err != nil {
		t.Fatalf("seed solo port: %v", err)
	}
	if _, err := store.Create("last"); err != ErrNoPortAvailable {
		t.Errorf("expected ErrNoPortAvailable with one free port, got %v", err)
	}
}

func TestPairAllocation_RespectsCapNotFullRange(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	// Fill the entire published range with pairs (10000..10199 = 100 projects).
	id := 0
	for port := PortRangeStart; port <= PublishedPortRangeEnd; port += 2 {
		_, err := db.Exec(
			"INSERT INTO projects (id, name, assigned_port, dev_port) VALUES (?, ?, ?, ?)",
			fmt.Sprintf("id-%d", id), fmt.Sprintf("app-%d", id), port, port+1,
		)
		if err != nil {
			t.Fatalf("seed pair at %d: %v", port, err)
		}
		id++
	}
	// Even though the DB range extends to PortRangeEnd, allocation is capped.
	if _, err := store.Create("overflow"); err != ErrNoPortAvailable {
		t.Errorf("expected ErrNoPortAvailable beyond published cap, got %v", err)
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
		// Both the PROD and DEV port of every pair must be globally distinct.
		for _, port := range []int{r.p.AssignedPort, r.p.DevPort} {
			if ports[port] {
				t.Errorf("port %d assigned to multiple projects", port)
			}
			ports[port] = true
			if port < PortRangeStart || port > PublishedPortRangeEnd {
				t.Errorf("port %d out of published range", port)
			}
		}
	}
}
