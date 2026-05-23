package project

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// setupManagerTest creates an in-memory DB, temp project root dir, and returns a Manager.
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
			assigned_port INTEGER
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_assigned_port ON projects(assigned_port) WHERE assigned_port IS NOT NULL;
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

	// .git directory exists
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

	// Pi harness exists with prompt, settings, first-party extension, and skill helper.
	piAgentsPath := filepath.Join(projectDir, ".pi", "AGENTS.md")
	piAgents, err := os.ReadFile(piAgentsPath)
	if err != nil {
		t.Fatalf(".pi/AGENTS.md not created: %v", err)
	}
	if !strings.Contains(string(piAgents), "10000") {
		t.Errorf(".pi/AGENTS.md missing port number, content: %s", piAgents)
	}
	if !strings.Contains(string(piAgents), "my-app") {
		t.Errorf(".pi/AGENTS.md missing project name, content: %s", piAgents)
	}
	for _, path := range []string{
		filepath.Join(projectDir, ".pi", "settings.json"),
		filepath.Join(projectDir, ".pi", "extensions", "appx-guardrails.ts"),
		filepath.Join(projectDir, ".pi", "skills", "appx-egress", "SKILL.md"),
		filepath.Join(projectDir, ".pi", "skills", "appx-egress", "request_egress.py"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Pi harness file not created: %s: %v", path, err)
		}
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
		t.Errorf("expected directory to be removed")
	}
}

func TestManagerDelete_NotFound(t *testing.T) {
	mgr, _ := setupManagerTest(t)

	err := mgr.Delete("nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestManagerCreate_CleansUpDirectoryOnScaffoldFailure(t *testing.T) {
	mgr, db := setupManagerTest(t)

	// Make the projectRoot a read-only directory so MkdirAll for the project
	// subdirectory succeeds (the root itself exists) but writing AGENTS.md fails.
	// We do this by replacing the projectRoot with a read-only directory.
	readOnlyRoot := t.TempDir()
	if err := os.Chmod(readOnlyRoot, 0555); err != nil {
		t.Skipf("cannot chmod temp dir (may be running as root): %v", err)
	}
	t.Cleanup(func() { os.Chmod(readOnlyRoot, 0755) })

	mgr.ProjectRoot = readOnlyRoot

	_, err := mgr.Create("fail-app")
	if err == nil {
		t.Fatal("expected Create to fail on read-only projectRoot")
	}

	// The partial directory should NOT exist.
	projectDir := filepath.Join(readOnlyRoot, "fail-app")
	if _, statErr := os.Stat(projectDir); !os.IsNotExist(statErr) {
		t.Errorf("expected directory %s to be cleaned up, but it still exists", projectDir)
	}

	// The DB record should NOT exist.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM projects WHERE name = 'fail-app'").Scan(&count)
	if count != 0 {
		t.Errorf("expected no DB record for failed project, got %d", count)
	}
}

func TestManagerCreate_AGENTSmdUsesBaseDomain(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	mgr.BaseDomain = "user.appx.app"

	_, err := mgr.Create("my-app")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	agentsPath := filepath.Join(mgr.ProjectRoot, "my-app", "AGENTS.md")
	content, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("AGENTS.md not created: %v", err)
	}
	if !strings.Contains(string(content), "my-app.user.appx.app") {
		t.Errorf("expected AGENTS.md to contain 'my-app.user.appx.app', got:\n%s", content)
	}
	if strings.Contains(string(content), ".localhost") {
		t.Errorf("expected AGENTS.md to NOT contain '.localhost' when baseDomain is set, got:\n%s", content)
	}

	piAgentsPath := filepath.Join(mgr.ProjectRoot, "my-app", ".pi", "AGENTS.md")
	piContent, err := os.ReadFile(piAgentsPath)
	if err != nil {
		t.Fatalf(".pi/AGENTS.md not created: %v", err)
	}
	if !strings.Contains(string(piContent), "my-app.user.appx.app") {
		t.Errorf("expected .pi/AGENTS.md to contain 'my-app.user.appx.app', got:\n%s", piContent)
	}
}

func TestManagerProjectDir_ReturnsPath(t *testing.T) {
	mgr, _ := setupManagerTest(t)

	dir := mgr.ProjectDir("my-app")
	if dir == "" {
		t.Fatal("expected non-empty project dir")
	}
	if !strings.HasSuffix(dir, "my-app") {
		t.Errorf("expected dir to end with project name, got %q", dir)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("expected absolute path, got %q", dir)
	}
}
