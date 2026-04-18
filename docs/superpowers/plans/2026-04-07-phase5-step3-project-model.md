# Phase 5 Step 3: Adapted Project Model

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rework the project model for the de-Docker architecture. Projects become directories on disk with auto-assigned ports, scaffolded `AGENTS.md`, and `git init`. Remove Docker-specific struct fields and store methods. Add DB migration 4 with `assigned_port` and `opencode_project_id` columns.

**Architecture:** After Steps 1-2, the Manager is a stub with only a `Store` field. This step makes `Store.Create()` allocate a port from range 10000-10999 (no user-provided port), scaffold a project directory with `AGENTS.md` + `git init`, and store the assigned port. Docker columns (`container_id`, `network_id`, `image_name`, `container_secret`, `resources`) remain in the schema for backward compatibility but are ignored by new code. The Project struct drops Docker fields and adds `AssignedPort` and `OpenCodeProjectID`.

**Tech Stack:** Go 1.26, SQLite (modernc.org/sqlite), golang-migrate (embedded SQL files)

**Reference:** `docs/plans/phase_5_plan.md` (Step 3), `docs/analysis/refactors/de-docker-refactor.md`

---

### Task 1: DB migration 4 — add `assigned_port` and `opencode_project_id`

**Files:**
- Create: `internal/db/migrations/000004_project_model.up.sql`
- Create: `internal/db/migrations/000004_project_model.down.sql`

- [ ] **Step 1: Create the up migration**

Create `internal/db/migrations/000004_project_model.up.sql`:

```sql
ALTER TABLE projects ADD COLUMN assigned_port INTEGER UNIQUE;
ALTER TABLE projects ADD COLUMN opencode_project_id TEXT;
```

These columns are nullable. Existing rows get NULL for both — they are legacy Docker projects that will not work in the new architecture. New projects created via the updated `Store.Create()` will always have `assigned_port` set.

- [ ] **Step 2: Create the down migration**

Create `internal/db/migrations/000004_project_model.down.sql`:

```sql
ALTER TABLE projects DROP COLUMN assigned_port;
ALTER TABLE projects DROP COLUMN opencode_project_id;
```

- [ ] **Step 3: Verify migration compiles into the binary**

The existing `//go:embed migrations/*.sql` in `internal/db/db.go` automatically picks up new files. Verify:

Run: `go build ./internal/db/`
Expected: compiles cleanly

- [ ] **Step 4: Write migration test**

Add to `internal/db/db_test.go` (or create if it doesn't exist). The test opens a fresh DB, runs all migrations, and verifies the new columns exist:

```go
func TestMigration4_ProjectModelColumns(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Verify assigned_port column exists and accepts values
	_, err = db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('test-1', 'port-test', 10000)")
	if err != nil {
		t.Fatalf("insert with assigned_port: %v", err)
	}

	// Verify UNIQUE constraint on assigned_port
	_, err = db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('test-2', 'port-dup', 10000)")
	if err == nil {
		t.Fatal("expected UNIQUE violation on assigned_port, got nil")
	}

	// Verify opencode_project_id column exists
	_, err = db.Exec("UPDATE projects SET opencode_project_id = 'oc-abc123' WHERE id = 'test-1'")
	if err != nil {
		t.Fatalf("update opencode_project_id: %v", err)
	}

	var ocID sql.NullString
	err = db.QueryRow("SELECT opencode_project_id FROM projects WHERE id = 'test-1'").Scan(&ocID)
	if err != nil {
		t.Fatalf("select opencode_project_id: %v", err)
	}
	if !ocID.Valid || ocID.String != "oc-abc123" {
		t.Errorf("expected 'oc-abc123', got %v", ocID)
	}
}
```

- [ ] **Step 5: Run test**

Run: `go test ./internal/db/ -run TestMigration4 -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/db/migrations/000004_project_model.up.sql internal/db/migrations/000004_project_model.down.sql internal/db/db_test.go
git commit -m "feat(db): migration 4 — add assigned_port and opencode_project_id columns"
```

---

### Task 2: Update Project struct — remove Docker fields, add new fields

**Files:**
- Modify: `internal/project/project.go`

- [ ] **Step 1: Rewrite the Project struct and clean up Docker helpers**

Replace `internal/project/project.go` with:

```go
package project

import (
	"errors"
	"regexp"
)

// ProjectStatus represents the current lifecycle state of a project.
// In the de-Docker architecture, projects are always "stopped" (directory exists
// but no dev server running) or "running" (dev server active on assigned port).
// The transitional states (starting, stopping) and error state are retained for
// compatibility with existing DB rows but not used by new code paths.
type ProjectStatus string

const (
	// StatusStopped indicates the project directory exists but no dev server is
	// running on the assigned port.
	StatusStopped ProjectStatus = "stopped"

	// StatusStarting is retained for backward compatibility with existing DB rows.
	StatusStarting ProjectStatus = "starting"

	// StatusRunning indicates a dev server is active on the project's assigned port.
	StatusRunning ProjectStatus = "running"

	// StatusStopping is retained for backward compatibility with existing DB rows.
	StatusStopping ProjectStatus = "stopping"

	// StatusError is retained for backward compatibility with existing DB rows.
	StatusError ProjectStatus = "error"
)

// PortRangeStart is the first port in the auto-assignment range. Each project
// gets a unique port from PortRangeStart to PortRangeEnd (inclusive).
const PortRangeStart = 10000

// PortRangeEnd is the last port in the auto-assignment range. This gives a
// maximum of 1000 projects (10000-10999).
const PortRangeEnd = 10999

// Project represents a project managed by appx. Each project maps to a directory
// on disk containing a git repository. The AssignedPort is used by the subdomain
// reverse proxy to route <name>.localhost requests to the project's dev server.
type Project struct {
	ID                string        `json:"id"`
	Name              string        `json:"name"`
	Status            ProjectStatus `json:"status"`
	AssignedPort      int           `json:"assignedPort"`
	OpenCodeProjectID string        `json:"openCodeProjectId,omitempty"`
	LastError         string        `json:"lastError,omitempty"`
	CreatedAt         string        `json:"createdAt"`
}

var (
	// ErrInvalidName is returned when a project name does not match the
	// required slug pattern (lowercase alphanumeric and hyphens, 2-63 chars).
	ErrInvalidName = errors.New("invalid project name: must match [a-z][a-z0-9-]{0,61}")

	// ErrDuplicateName is returned when a project with the same name already exists.
	ErrDuplicateName = errors.New("project name already exists")

	// ErrNotFound is returned when the requested project does not exist in the database.
	ErrNotFound = errors.New("project not found")

	// ErrInvalidState is returned when the requested operation cannot be
	// performed in the project's current state.
	ErrInvalidState = errors.New("invalid state transition")

	// ErrNoPortAvailable is returned when all ports in the assignment range
	// (10000-10999) are already allocated to existing projects.
	ErrNoPortAvailable = errors.New("no port available in range 10000-10999")
)

// namePattern enforces that project names are valid slugs: start with a lowercase
// letter, then lowercase alphanumeric or hyphens, 2-63 chars total.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// ValidateName checks that name is a valid project slug. Single-character names
// are rejected because they would produce ambiguous resource names.
func ValidateName(name string) error {
	if len(name) < 2 || !namePattern.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}
```

- [ ] **Step 2: Verify compilation fails (store.go still references old fields)**

Run: `go build ./internal/project/ 2>&1 | head -20`
Expected: compile errors in `store.go` and `store_test.go` referencing removed fields

- [ ] **Step 3: Commit**

```bash
git add internal/project/project.go
git commit -m "refactor(project): remove Docker fields, add AssignedPort and OpenCodeProjectID"
```

---

### Task 3: Port allocator

**Files:**
- Modify: `internal/project/store.go`

- [ ] **Step 1: Write port allocator tests**

Add to `internal/project/store_test.go` (will fail until implementation):

```go
func TestNextAvailablePort_Empty(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

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

	// Manually insert projects with ports 10000 and 10002 (gap at 10001)
	db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('a', 'app-a', 10000)")
	db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('b', 'app-b', 10002)")

	port, err := store.nextAvailablePort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 10001 {
		t.Errorf("expected 10001 (gap fill), got %d", port)
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

	// Fill all 1000 ports
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
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/project/ -run TestNextAvailablePort -v 2>&1 | tail -5`
Expected: FAIL — `nextAvailablePort` not defined

- [ ] **Step 3: Implement `nextAvailablePort`**

Add to `internal/project/store.go`:

```go
// nextAvailablePort finds the lowest unused port in the range PortRangeStart to
// PortRangeEnd by querying existing assigned_port values. It fills gaps left by
// deleted projects before extending to higher ports. Returns ErrNoPortAvailable
// if all 1000 ports are allocated.
func (s *Store) nextAvailablePort() (int, error) {
	rows, err := s.db.Query(
		"SELECT assigned_port FROM projects WHERE assigned_port IS NOT NULL ORDER BY assigned_port ASC",
	)
	if err != nil {
		return 0, fmt.Errorf("query assigned ports: %w", err)
	}
	defer rows.Close()

	usedPorts := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return 0, fmt.Errorf("scan port: %w", err)
		}
		usedPorts[p] = true
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate ports: %w", err)
	}

	for port := PortRangeStart; port <= PortRangeEnd; port++ {
		if !usedPorts[port] {
			return port, nil
		}
	}

	return 0, ErrNoPortAvailable
}
```

- [ ] **Step 4: Run port allocator tests**

Run: `go test ./internal/project/ -run TestNextAvailablePort -v`
Expected: all 4 tests PASS

- [ ] **Step 5: Commit**

```bash
git add internal/project/store.go internal/project/store_test.go
git commit -m "feat(project): add port allocator with gap-filling (range 10000-10999)"
```

---

### Task 4: Rewrite `Store` — new `Create`, `scanInto`, remove Docker methods

**Files:**
- Modify: `internal/project/store.go`

- [ ] **Step 1: Update `setupTestDB` in `store_test.go` for new schema**

Replace the `setupTestDB` function in `internal/project/store_test.go`:

```go
// setupTestDB creates an in-memory SQLite database with the projects table
// matching the schema from migrations 1-4. Docker columns are retained for
// backward compatibility but not used by new code.
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
			assigned_port INTEGER UNIQUE,
			opencode_project_id TEXT
		)
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
```

- [ ] **Step 2: Rewrite the Store**

Replace the full content of `internal/project/store.go`:

```go
package project

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Store provides CRUD operations for projects in the SQLite database. It
// operates on the projects table created by migrations 1-4. New projects use
// the assigned_port and opencode_project_id columns; legacy Docker columns
// (container_id, network_id, image_name, container_secret, resources) are
// retained in the schema but ignored by new code.
type Store struct {
	db *sql.DB
}

// NewStore creates a Store backed by the given database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// projectColumns is the canonical SELECT column list for the projects table.
// Only columns used by the new architecture are selected. Legacy Docker columns
// are not read.
const projectColumns = `id, name, status, assigned_port, opencode_project_id, last_error, created_at`

// Create inserts a new project with the given name and an auto-assigned port
// from the range PortRangeStart-PortRangeEnd. It validates the name, allocates
// the next available port, generates a UUID, and sets the initial status to
// "stopped". Returns ErrInvalidName if the name is not a valid slug,
// ErrDuplicateName if a project with the same name already exists, or
// ErrNoPortAvailable if all ports in the range are allocated.
func (s *Store) Create(name string) (*Project, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	port, err := s.nextAvailablePort()
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	_, err = s.db.Exec(
		"INSERT INTO projects (id, name, status, assigned_port) VALUES (?, ?, ?, ?)",
		id, name, StatusStopped, port,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("insert project: %w", err)
	}

	return s.Get(id)
}

// List returns all projects ordered by creation date (newest first).
// Returns an empty slice (not nil) when no projects exist.
func (s *Store) List() ([]*Project, error) {
	rows, err := s.db.Query(
		`SELECT ` + projectColumns + ` FROM projects ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	projects := []*Project{}
	for rows.Next() {
		p, err := scanInto(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// Get returns a single project by ID. Returns ErrNotFound if the project
// does not exist.
func (s *Store) Get(id string) (*Project, error) {
	row := s.db.QueryRow(
		`SELECT `+projectColumns+` FROM projects WHERE id = ?`, id,
	)

	p, err := scanInto(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

// Delete removes a project from the database by ID. Returns ErrNotFound if the
// project does not exist. The caller is responsible for removing the project
// directory from disk before calling Delete.
func (s *Store) Delete(id string) error {
	res, err := s.db.Exec("DELETE FROM projects WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetByName returns a single project by name. Returns ErrNotFound if no
// project with the given name exists. Used by the subdomain reverse proxy to
// look up projects from the Host header.
func (s *Store) GetByName(name string) (*Project, error) {
	row := s.db.QueryRow(`SELECT `+projectColumns+` FROM projects WHERE name = ?`, name)
	p, err := scanInto(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project by name: %w", err)
	}
	return p, nil
}

// SetOpenCodeProjectID stores the OpenCode-assigned project ID after OpenCode
// auto-discovers the project directory. This mapping is used for API calls
// targeting a specific project in the OpenCode server.
func (s *Store) SetOpenCodeProjectID(id, ocProjectID string) error {
	res, err := s.db.Exec(
		"UPDATE projects SET opencode_project_id = ? WHERE id = ?",
		ocProjectID, id,
	)
	if err != nil {
		return fmt.Errorf("set opencode project id: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetError updates a project to the error state and records the error message
// in the last_error column for display in the UI.
func (s *Store) SetError(id string, errMsg string) error {
	_, err := s.db.Exec(
		"UPDATE projects SET status = ?, last_error = ? WHERE id = ?",
		StatusError, errMsg, id,
	)
	if err != nil {
		return fmt.Errorf("set error: %w", err)
	}
	return nil
}

// nextAvailablePort finds the lowest unused port in the range PortRangeStart to
// PortRangeEnd by querying existing assigned_port values. It fills gaps left by
// deleted projects before extending to higher ports. Returns ErrNoPortAvailable
// if all 1000 ports are allocated.
func (s *Store) nextAvailablePort() (int, error) {
	rows, err := s.db.Query(
		"SELECT assigned_port FROM projects WHERE assigned_port IS NOT NULL ORDER BY assigned_port ASC",
	)
	if err != nil {
		return 0, fmt.Errorf("query assigned ports: %w", err)
	}
	defer rows.Close()

	usedPorts := map[int]bool{}
	for rows.Next() {
		var p int
		if err := rows.Scan(&p); err != nil {
			return 0, fmt.Errorf("scan port: %w", err)
		}
		usedPorts[p] = true
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate ports: %w", err)
	}

	for port := PortRangeStart; port <= PortRangeEnd; port++ {
		if !usedPorts[port] {
			return port, nil
		}
	}

	return 0, ErrNoPortAvailable
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanInto reads a project row into a Project struct. Nullable columns are
// handled with sql.NullString and sql.NullInt64. Only columns in the new
// architecture are scanned; legacy Docker columns are not selected.
func scanInto(sc scanner) (*Project, error) {
	var p Project
	var assignedPort sql.NullInt64
	var ocProjectID, lastError sql.NullString
	err := sc.Scan(
		&p.ID, &p.Name, &p.Status, &assignedPort,
		&ocProjectID, &lastError, &p.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if assignedPort.Valid {
		p.AssignedPort = int(assignedPort.Int64)
	}
	p.OpenCodeProjectID = ocProjectID.String
	p.LastError = lastError.String
	return &p, nil
}

// isUniqueViolation checks whether an error is a SQLite UNIQUE constraint
// violation. modernc.org/sqlite returns errors containing "UNIQUE constraint".
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint")
}
```

- [ ] **Step 3: Verify store compiles**

Run: `go build ./internal/project/`
Expected: compiles cleanly (tests may not pass yet — next step)

- [ ] **Step 4: Commit**

```bash
git add internal/project/store.go
git commit -m "refactor(store): rewrite Store for de-Docker model — auto-assign port, drop Docker methods"
```

---

### Task 5: Rewrite store tests

**Files:**
- Modify: `internal/project/store_test.go`

- [ ] **Step 1: Replace store_test.go with updated tests**

Replace the full content of `internal/project/store_test.go`:

```go
package project

import (
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

// setupTestDB creates an in-memory SQLite database with the projects table
// matching the schema from migrations 1-4. Docker columns are retained for
// backward compatibility but not used by new code.
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
			assigned_port INTEGER UNIQUE,
			opencode_project_id TEXT
		)
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

	invalid := []string{
		"",         // empty
		"a",        // too short
		"A",        // uppercase
		"My-App",   // uppercase
		"123-app",  // starts with digit
		"-app",     // starts with hyphen
		"app_test", // underscore not allowed
		"app.test", // period not allowed
		"app test", // space not allowed
	}

	for _, name := range invalid {
		_, err := store.Create(name)
		if err != ErrInvalidName {
			t.Errorf("name %q: expected ErrInvalidName, got %v", name, err)
		}
	}
}

func TestCreate_ValidNames(t *testing.T) {
	store := NewStore(setupTestDB(t))

	valid := []string{
		"ab",
		"my-app",
		"test-project-123",
		"a0",
	}

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
		t.Errorf("expected 0 projects, got %d", len(projects))
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
		t.Errorf("expected 2 projects, got %d", len(projects))
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
	if got.Name != "my-app" {
		t.Errorf("expected name my-app, got %s", got.Name)
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

	// Delete the first project — its port should become available
	store.Delete(p1.ID)

	p3, err := store.Create("app-c")
	if err != nil {
		t.Fatalf("create app-c: %v", err)
	}
	// Should reuse the freed port 10000
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

func TestSetOpenCodeProjectID(t *testing.T) {
	store := NewStore(setupTestDB(t))

	p, _ := store.Create("my-app")

	err := store.SetOpenCodeProjectID(p.ID, "oc-abc123")
	if err != nil {
		t.Fatalf("SetOpenCodeProjectID: %v", err)
	}

	got, _ := store.Get(p.ID)
	if got.OpenCodeProjectID != "oc-abc123" {
		t.Errorf("expected 'oc-abc123', got %q", got.OpenCodeProjectID)
	}
}

func TestSetOpenCodeProjectID_NotFound(t *testing.T) {
	store := NewStore(setupTestDB(t))

	err := store.SetOpenCodeProjectID("nonexistent", "oc-abc123")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestNextAvailablePort_Empty(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

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
		t.Errorf("expected 10001 (gap fill), got %d", port)
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
```

- [ ] **Step 2: Run all store tests**

Run: `go test ./internal/project/ -v`
Expected: ALL tests PASS

- [ ] **Step 3: Commit**

```bash
git add internal/project/store_test.go
git commit -m "test(store): rewrite store tests for de-Docker project model"
```

---

### Task 6: AGENTS.md template and project directory scaffolding

**Files:**
- Modify: `internal/project/manager.go`
- Modify: `internal/project/manager_test.go`

- [ ] **Step 1: Write tests for Manager.Create with directory scaffolding**

Replace `internal/project/manager_test.go`:

```go
package project

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// setupManagerTest creates an in-memory SQLite database, a temporary project
// root directory, and returns a Manager ready for testing.
func setupManagerTest(t *testing.T) (*Manager, *sql.DB) {
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
			assigned_port INTEGER UNIQUE,
			opencode_project_id TEXT
		)
	`)
	if err != nil {
		t.Fatal(err)
	}

	store := NewStore(db)
	projectRoot := t.TempDir()
	mgr := NewManager(store, projectRoot)
	return mgr, db
}

func TestNewManager(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	if mgr.Store == nil {
		t.Fatal("expected non-nil store")
	}
}

func TestManagerCreate_CreatesDirectory(t *testing.T) {
	mgr, _ := setupManagerTest(t)

	p, err := mgr.Create("my-app")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	projectDir := filepath.Join(mgr.ProjectRoot, "my-app")

	// Directory exists
	info, err := os.Stat(projectDir)
	if err != nil {
		t.Fatalf("project dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}

	// .git directory exists (git init was run)
	gitDir := filepath.Join(projectDir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		t.Fatalf(".git dir not created: %v", err)
	}

	// AGENTS.md exists with correct port
	agentsPath := filepath.Join(projectDir, "AGENTS.md")
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("AGENTS.md not created: %v", err)
	}
	if !strings.Contains(string(content), "10000") {
		t.Errorf("AGENTS.md missing port number, content: %s", content)
	}
	if !strings.Contains(string(content), "my-app") {
		t.Errorf("AGENTS.md missing project name, content: %s", content)
	}

	// Project has correct assigned port
	if p.AssignedPort != 10000 {
		t.Errorf("expected port 10000, got %d", p.AssignedPort)
	}
}

func TestManagerCreate_GitHasInitialCommit(t *testing.T) {
	mgr, _ := setupManagerTest(t)

	mgr.Create("my-app")

	projectDir := filepath.Join(mgr.ProjectRoot, "my-app")

	// Verify git has at least one commit (HEAD exists)
	headPath := filepath.Join(projectDir, ".git", "HEAD")
	if _, err := os.Stat(headPath); err != nil {
		t.Fatalf("git HEAD not created: %v", err)
	}
}

func TestManagerCreate_InvalidName(t *testing.T) {
	mgr, _ := setupManagerTest(t)

	_, err := mgr.Create("A")
	if err != ErrInvalidName {
		t.Errorf("expected ErrInvalidName, got %v", err)
	}
}

func TestManagerDelete_RemovesDirectory(t *testing.T) {
	mgr, _ := setupManagerTest(t)

	p, err := mgr.Create("my-app")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	projectDir := filepath.Join(mgr.ProjectRoot, "my-app")
	if _, err := os.Stat(projectDir); err != nil {
		t.Fatalf("directory should exist before delete: %v", err)
	}

	if err := mgr.Delete(p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
		t.Errorf("expected directory to be removed, got err: %v", err)
	}
}

func TestManagerDelete_NotFound(t *testing.T) {
	mgr, _ := setupManagerTest(t)

	err := mgr.Delete("nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `go test ./internal/project/ -run TestManagerCreate -v 2>&1 | tail -10`
Expected: FAIL — `NewManager` signature wrong or `Create`/`Delete` methods not defined on Manager

- [ ] **Step 3: Implement Manager with directory scaffolding**

Replace `internal/project/manager.go`:

```go
package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// agentsTemplate is the AGENTS.md content scaffolded into every new project
// directory. It tells the AI agent which port to use for dev servers. The
// placeholders {{name}}, {{port}}, and {{subdomain}} are replaced at creation.
const agentsTemplate = `# Project: {{name}}

## App Port

When running a dev server, always use port {{port}}.
This port is assigned by appx and has proxy routing configured.
Your app will be accessible at {{subdomain}}.

## Guidelines

- Use this port for ALL dev servers (Vite, Next.js, Express, etc.)
- Do not change the port — it is mapped to a subdomain by the appx proxy
- The project directory is the working directory for all commands
`

// Manager provides project lifecycle operations. It delegates to the Store for
// database CRUD and handles filesystem operations (directory creation, git init,
// AGENTS.md scaffolding) for new projects. In the de-Docker architecture, there
// are no container lifecycle methods — OpenCode manages agent sessions natively.
type Manager struct {
	Store       *Store
	ProjectRoot string
}

// NewManager creates a Manager backed by the given project store. The
// projectRoot is the base directory where project subdirectories are created
// (e.g. /home/opencode/projects). Each project gets a subdirectory named after
// the project slug.
func NewManager(store *Store, projectRoot string) *Manager {
	return &Manager{
		Store:       store,
		ProjectRoot: projectRoot,
	}
}

// Create creates a new project: inserts a DB record with an auto-assigned port,
// creates the project directory, scaffolds AGENTS.md with the assigned port, runs
// git init, stages all files, and makes an initial commit. The git repo is
// required for OpenCode to discover the project. If directory creation or git
// init fails, the DB record is rolled back (deleted).
func (m *Manager) Create(name string) (*Project, error) {
	proj, err := m.Store.Create(name)
	if err != nil {
		return nil, err
	}

	projectDir := filepath.Join(m.ProjectRoot, name)
	if err := m.scaffoldProject(projectDir, proj); err != nil {
		// Roll back the DB record on filesystem failure
		m.Store.Delete(proj.ID)
		return nil, fmt.Errorf("scaffold project: %w", err)
	}

	return proj, nil
}

// Delete removes a project's directory from disk and its record from the
// database. The directory is removed first; if that fails the DB record is
// kept so the user can retry. Returns ErrNotFound if the project does not
// exist in the database.
func (m *Manager) Delete(id string) error {
	proj, err := m.Store.Get(id)
	if err != nil {
		return err
	}

	projectDir := filepath.Join(m.ProjectRoot, proj.Name)
	if err := os.RemoveAll(projectDir); err != nil {
		return fmt.Errorf("remove project directory: %w", err)
	}

	return m.Store.Delete(id)
}

// List returns all projects ordered by creation date (newest first).
func (m *Manager) List() ([]*Project, error) {
	return m.Store.List()
}

// Get returns a single project by ID. Returns ErrNotFound if the project
// does not exist.
func (m *Manager) Get(id string) (*Project, error) {
	return m.Store.Get(id)
}

// GetByName returns a single project by name. Returns ErrNotFound if no
// project with the given name exists.
func (m *Manager) GetByName(name string) (*Project, error) {
	return m.Store.GetByName(name)
}

// scaffoldProject creates the project directory, writes AGENTS.md with the
// assigned port, initializes a git repo, stages all files, and makes an initial
// commit. Returns an error if any step fails; the caller should roll back the
// DB record.
func (m *Manager) scaffoldProject(dir string, proj *Project) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Write AGENTS.md
	content := agentsTemplate
	content = strings.ReplaceAll(content, "{{name}}", proj.Name)
	content = strings.ReplaceAll(content, "{{port}}", fmt.Sprintf("%d", proj.AssignedPort))
	content = strings.ReplaceAll(content, "{{subdomain}}", fmt.Sprintf("%s.localhost", proj.Name))

	agentsPath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write AGENTS.md: %w", err)
	}

	// git init
	if err := runGit(dir, "init"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}

	// git add + commit
	if err := runGit(dir, "add", "."); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := runGit(dir, "commit", "-m", "Initial project scaffold"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	return nil
}

// runGit executes a git command in the given directory. Returns an error if the
// command fails, including stderr output for debugging.
func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Set minimal git config for the commit to succeed without global config
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=appx",
		"GIT_AUTHOR_EMAIL=appx@localhost",
		"GIT_COMMITTER_NAME=appx",
		"GIT_COMMITTER_EMAIL=appx@localhost",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, string(out))
	}
	return nil
}
```

- [ ] **Step 4: Run manager tests**

Run: `go test ./internal/project/ -run TestManager -v`
Expected: ALL tests PASS

- [ ] **Step 5: Run all project tests**

Run: `go test ./internal/project/ -v`
Expected: ALL tests PASS

- [ ] **Step 6: Commit**

```bash
git add internal/project/manager.go internal/project/manager_test.go
git commit -m "feat(project): Manager with directory scaffolding, AGENTS.md, git init"
```

---

### Task 7: Update handlers and router for new Manager signature

**Files:**
- Modify: `internal/server/project_handlers.go`
- Modify: `internal/server/router.go`
- Modify: `internal/server/router_test.go`
- Modify: `cmd/appx/main.go`

This task adapts the server layer to the new Manager API. After Steps 1-2 remove Docker routes, the remaining handlers are: list, create, get, delete, update. Of these, `create` changes signature (no more user port), `delete` calls Manager.Delete (which removes directory), and `update` (port update) is removed since ports are auto-assigned.

- [ ] **Step 1: Update `handleCreateProject` — remove port from request**

In `internal/server/project_handlers.go`, replace `handleCreateProject`:

```go
// handleCreateProject returns the handler for POST /api/projects. It reads a
// JSON body with "name" (required slug), creates the project with an
// auto-assigned port from the range 10000-10999, scaffolds the project
// directory with AGENTS.md and git init, and returns 201 with the project JSON.
// Returns 400 for invalid input, 409 for duplicate names, and 507 if all ports
// are exhausted.
func handleCreateProject(pm *project.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		proj, err := pm.Create(req.Name)
		if err != nil {
			if errors.Is(err, project.ErrInvalidName) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if errors.Is(err, project.ErrDuplicateName) {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			if errors.Is(err, project.ErrNoPortAvailable) {
				http.Error(w, err.Error(), http.StatusInsufficientStorage)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusCreated)
		writeJSON(w, proj)
	}
}
```

- [ ] **Step 2: Remove `handleStartProject`, `handleStopProject`, `handleResetProject`, `handleUpdateProject`**

Delete these four functions from `internal/server/project_handlers.go`. They reference Docker lifecycle methods and user-controlled port updates that no longer exist.

- [ ] **Step 3: Update `handleDeleteProject` doc comment**

Replace the doc comment on `handleDeleteProject`:

```go
// handleDeleteProject returns the handler for DELETE /api/projects/{id}. It
// removes the project directory from disk and deletes the database record.
// Returns 204 on success or 404 if not found.
func handleDeleteProject(pm *project.Manager) http.HandlerFunc {
```

- [ ] **Step 4: Remove deleted routes from router.go**

In `internal/server/router.go`, remove these lines from the `api` mux registration (they were already removed in Step 1, but verify):

```
api.HandleFunc("PATCH /api/projects/{id}", handleUpdateProject(pm))
api.HandleFunc("POST /api/projects/{id}/start", handleStartProject(pm))
api.HandleFunc("POST /api/projects/{id}/stop", handleStopProject(pm))
api.HandleFunc("POST /api/projects/{id}/reset", handleResetProject(pm))
```

If Step 1 already removed these, this is a no-op verification.

- [ ] **Step 5: Update `cmd/appx/main.go` — pass projectRoot to NewManager**

In `cmd/appx/main.go`, update the Manager construction. Add a `--project-root` flag defaulting to `./data/projects`:

Find the line:
```go
pm := project.NewManager(projectStore)
```

Replace with:
```go
projectRoot := filepath.Join(*dataDir, "projects")
if err := os.MkdirAll(projectRoot, 0755); err != nil {
	log.Fatalf("create project root: %v", err)
}
pm := project.NewManager(projectStore, projectRoot)
```

- [ ] **Step 6: Update router_test.go setupTest**

Update the `setupTest` function in `internal/server/router_test.go` to use the new Manager signature. Add `assigned_port` and `opencode_project_id` columns to the test schema. Remove Docker columns from the schema if Step 1 already did; otherwise add the new columns:

In `setupTest`, find the Manager construction and replace:

```go
ps := project.NewStore(db)
pm := project.NewManager(ps, t.TempDir())
```

Add new columns to the test schema (after `container_secret TEXT`):

```sql
assigned_port INTEGER UNIQUE,
opencode_project_id TEXT
```

Update the `NewRouter` call to match the new signature (should already be `NewRouter(a, pm, webFS)` from Step 1).

Remove any tests that reference `handleUpdateProject`, `handleStartProject`, `handleStopProject`, or `handleResetProject` if not already removed by Step 1.

Update `TestCreateProject` tests to send only `{"name":"..."}` (no port field) and verify the response includes `assignedPort`.

- [ ] **Step 7: Verify compilation**

Run: `go build ./cmd/appx/`
Expected: compiles cleanly

- [ ] **Step 8: Run all tests**

Run: `go test ./... 2>&1 | tail -20`
Expected: ALL tests PASS

- [ ] **Step 9: Commit**

```bash
git add internal/server/project_handlers.go internal/server/router.go internal/server/router_test.go cmd/appx/main.go
git commit -m "refactor(server): adapt handlers for auto-port project model, remove Docker lifecycle routes"
```

---

### Task 8: Update frontend API client

**Files:**
- Modify: `web/src/api/client.ts`
- Modify: `web/src/components/CreateProjectModal.tsx`
- Modify: `web/src/components/ProjectCard.tsx`

- [ ] **Step 1: Update the Project type in client.ts**

In `web/src/api/client.ts`, find the `Project` interface and update it:

```typescript
/** Project represents a project managed by appx. */
export interface Project {
  id: string;
  name: string;
  status: string;
  assignedPort: number;
  openCodeProjectId?: string;
  lastError?: string;
  createdAt: string;
}
```

Remove the old `port`, `containerId`, `imageName`, `resources` fields.

- [ ] **Step 2: Update `createProject` to not send port**

Find the `createProject` function and update it to only send `name`:

```typescript
/** createProject creates a new project with auto-assigned port. POST /api/projects. */
export async function createProject(name: string): Promise<Project> {
  return request<Project>('/api/projects', {
    method: 'POST',
    body: JSON.stringify({ name }),
  });
}
```

Remove the `port` parameter.

- [ ] **Step 3: Update CreateProjectModal.tsx — remove port input**

In `web/src/components/CreateProjectModal.tsx`, remove the port input field and its validation. The form should only have a name input. Update the `onSubmit` handler to call `createProject(name)` without a port.

- [ ] **Step 4: Update ProjectCard.tsx — show assignedPort instead of port**

In `web/src/components/ProjectCard.tsx`, replace references to `project.port` with `project.assignedPort`. Update the display label from "Port" to "Port" (same label, different field). Remove any references to `containerId`, `imageName`, or `resources`.

- [ ] **Step 5: Build frontend**

Run: `task web`
Expected: builds cleanly

- [ ] **Step 6: Commit**

```bash
git add web/src/api/client.ts web/src/components/CreateProjectModal.tsx web/src/components/ProjectCard.tsx
git commit -m "ui: update project model — auto-assigned port, remove Docker fields"
```

---

### Task 9: Final verification

- [ ] **Step 1: Run full Go test suite**

Run: `task test`
Expected: ALL tests pass

- [ ] **Step 2: Run frontend lint**

Run: `task lint`
Expected: no errors

- [ ] **Step 3: Full build**

Run: `task build`
Expected: compiles cleanly — Go binary with embedded frontend

- [ ] **Step 4: Verify migration on fresh database**

```bash
rm -rf /tmp/appx-test-data
./appx -port 8443 -data /tmp/appx-test-data &
sleep 2
```

Run: `curl -k https://localhost:8443/ -o /dev/null -w '%{http_code}'`
Expected: `200`

Run: `kill %1`

- [ ] **Step 5: Verify project creation via API**

```bash
./appx -port 8443 -data /tmp/appx-test-data &
sleep 2
```

Get the initial password:
```bash
cat /tmp/appx-test-data/initial_password
```

Login and create a project:
```bash
TOKEN=$(curl -sk https://localhost:8443/api/login -H 'Content-Type: application/json' -d '{"password":"<pw>"}' -c - | grep appx_session | awk '{print $NF}')
curl -sk https://localhost:8443/api/projects -H 'Content-Type: application/json' -H "Cookie: appx_session=$TOKEN" -d '{"name":"test-app"}'
```

Expected: 201 response with `"assignedPort":10000` and project directory at `/tmp/appx-test-data/projects/test-app/` containing `AGENTS.md` and `.git/`.

Run: `kill %1`

- [ ] **Step 6: Clean up test data**

```bash
rm -rf /tmp/appx-test-data
```

- [ ] **Step 7: Commit any final fixes**

```bash
git add -A
git commit -m "feat: Phase 5 Step 3 complete — adapted project model with auto-port and directory scaffolding"
```
