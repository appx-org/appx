package project

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// fakeAgent is an in-memory AgentRegistrar that records calls (including the
// deployment payload) and can be primed to fail, so Manager lifecycle behaviour
// is tested without a real agent-server.
type fakeAgent struct {
	ensured     []string
	deployments []Deployment
	deleted     []string
	ensureErr   error
	deleteErr   error
}

func (f *fakeAgent) EnsureProject(_ context.Context, name string, dep Deployment) error {
	if f.ensureErr != nil {
		return f.ensureErr
	}
	f.ensured = append(f.ensured, name)
	f.deployments = append(f.deployments, dep)
	return nil
}

func (f *fakeAgent) DeleteProject(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, id)
	return nil
}

// setupManagerTest creates an in-memory DB, temp project root dir, and returns a
// Manager plus the fake agent wired into it.
func setupManagerTest(t *testing.T) (*Manager, *fakeAgent, *sql.DB) {
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

	store := NewStore(db)
	projectRoot := t.TempDir()
	mgr := NewManager(store, projectRoot)
	agent := &fakeAgent{}
	mgr.Agent = agent
	return mgr, agent, db
}

func TestNewManager(t *testing.T) {
	mgr, _, _ := setupManagerTest(t)
	if mgr == nil {
		t.Fatal("expected non-nil manager")
	}
	if mgr.Store == nil {
		t.Fatal("expected non-nil store")
	}
}

func TestManagerCreate_RegistersWithAgentServer(t *testing.T) {
	mgr, agent, _ := setupManagerTest(t)

	p, err := mgr.Create(context.Background(), "my-app")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if p.Name != "my-app" {
		t.Errorf("expected name my-app, got %q", p.Name)
	}
	// agent-server id == project name; the manager registers by name.
	if len(agent.ensured) != 1 || agent.ensured[0] != "my-app" {
		t.Errorf("expected EnsureProject(\"my-app\"), got %v", agent.ensured)
	}
	// First project gets the bottom of the port range.
	if p.AssignedPort != 10000 {
		t.Errorf("expected port 10000, got %d", p.AssignedPort)
	}

	// The appx control-plane record exists.
	if _, err := mgr.Get(p.ID); err != nil {
		t.Fatalf("expected project record to exist: %v", err)
	}
}

func TestManagerCreate_NoAgentStillCreatesRecord(t *testing.T) {
	mgr, _, _ := setupManagerTest(t)
	mgr.Agent = nil // co-located/test deployments may run without registration

	p, err := mgr.Create(context.Background(), "my-app")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := mgr.Get(p.ID); err != nil {
		t.Fatalf("expected project record to exist: %v", err)
	}
}

func TestManagerCreate_InvalidNameSkipsRegistration(t *testing.T) {
	mgr, agent, _ := setupManagerTest(t)

	_, err := mgr.Create(context.Background(), "A")
	if err != ErrInvalidName {
		t.Errorf("expected ErrInvalidName, got %v", err)
	}
	if len(agent.ensured) != 0 {
		t.Errorf("expected no agent registration for invalid name, got %v", agent.ensured)
	}
}

func TestManagerCreate_RollsBackRecordOnAgentFailure(t *testing.T) {
	mgr, agent, db := setupManagerTest(t)
	agent.ensureErr = errors.New("agent-server down")

	_, err := mgr.Create(context.Background(), "fail-app")
	if err == nil {
		t.Fatal("expected Create to fail when agent registration fails")
	}

	// The appx record must have been rolled back.
	var count int
	db.QueryRow("SELECT COUNT(*) FROM projects WHERE name = 'fail-app'").Scan(&count)
	if count != 0 {
		t.Errorf("expected no DB record after rollback, got %d", count)
	}
}

func TestManagerDelete_DeregistersAndRemovesRecord(t *testing.T) {
	mgr, agent, _ := setupManagerTest(t)

	p, err := mgr.Create(context.Background(), "my-app")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := mgr.Delete(context.Background(), p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Deregistered by name (agent-server id).
	if len(agent.deleted) != 1 || agent.deleted[0] != "my-app" {
		t.Errorf("expected DeleteProject(\"my-app\"), got %v", agent.deleted)
	}
	// appx record removed.
	if _, err := mgr.Get(p.ID); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestManagerDelete_NotFound(t *testing.T) {
	mgr, _, _ := setupManagerTest(t)

	err := mgr.Delete(context.Background(), "nonexistent")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestManagerDelete_AgentFailureKeepsRecord(t *testing.T) {
	mgr, agent, _ := setupManagerTest(t)

	p, err := mgr.Create(context.Background(), "my-app")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	agent.deleteErr = errors.New("agent-server down")

	if err := mgr.Delete(context.Background(), p.ID); err == nil {
		t.Fatal("expected Delete to fail when deregistration fails")
	}
	// The record must remain so the operator can retry rather than losing track.
	if _, err := mgr.Get(p.ID); err != nil {
		t.Errorf("expected record to remain after failed deregistration: %v", err)
	}
}

func TestManagerReconcileAgentProjects_RegistersAll(t *testing.T) {
	mgr, agent, _ := setupManagerTest(t)

	if _, err := mgr.Create(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(context.Background(), "beta"); err != nil {
		t.Fatal(err)
	}
	agent.ensured = nil // reset to observe only the reconcile pass

	if err := mgr.ReconcileAgentProjects(context.Background()); err != nil {
		t.Fatalf("ReconcileAgentProjects: %v", err)
	}
	if len(agent.ensured) != 2 {
		t.Errorf("expected 2 re-registrations, got %v", agent.ensured)
	}
}

func TestManagerCreate_PushesDeploymentPayload(t *testing.T) {
	mgr, agent, _ := setupManagerTest(t)
	mgr.HTTPMode = true
	mgr.BaseDomain = "127.0.0.1.sslip.io"
	mgr.ExternalPort = 8080

	p, err := mgr.Create(context.Background(), "eventx")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(agent.deployments) != 1 {
		t.Fatalf("expected 1 deployment payload, got %d", len(agent.deployments))
	}
	dep := agent.deployments[0]
	if dep.Prod.Port != p.AssignedPort || dep.Dev.Port != p.DevPort {
		t.Errorf("ports: got dev=%d prod=%d, want dev=%d prod=%d", dep.Dev.Port, dep.Prod.Port, p.DevPort, p.AssignedPort)
	}
	if dep.Prod.URL != "http://eventx.127.0.0.1.sslip.io:8080" {
		t.Errorf("prod URL: got %q", dep.Prod.URL)
	}
	if dep.Dev.URL != "http://eventx-dev.127.0.0.1.sslip.io:8080" {
		t.Errorf("dev URL: got %q", dep.Dev.URL)
	}
}

func TestManagerReconcile_PushesDeploymentPayload(t *testing.T) {
	mgr, agent, _ := setupManagerTest(t)
	mgr.BaseDomain = "example.com" // HTTPS production defaults

	if _, err := mgr.Create(context.Background(), "eventx"); err != nil {
		t.Fatal(err)
	}
	agent.ensured = nil
	agent.deployments = nil

	if err := mgr.ReconcileAgentProjects(context.Background()); err != nil {
		t.Fatalf("ReconcileAgentProjects: %v", err)
	}
	if len(agent.deployments) != 1 {
		t.Fatalf("expected 1 reconcile payload, got %d", len(agent.deployments))
	}
	dep := agent.deployments[0]
	if dep.Prod.URL != "https://eventx.example.com" {
		t.Errorf("prod URL: got %q, want https://eventx.example.com", dep.Prod.URL)
	}
	if dep.Dev.URL != "https://eventx-dev.example.com" {
		t.Errorf("dev URL: got %q, want https://eventx-dev.example.com", dep.Dev.URL)
	}
}

func TestManagerAppURL_SchemeAndPortRules(t *testing.T) {
	cases := []struct {
		name       string
		httpMode   bool
		baseDomain string
		port       int
		label      string
		want       string
	}{
		{"https default port elided", false, "example.com", 443, "eventx", "https://eventx.example.com"},
		{"https custom port appended", false, "example.com", 8443, "eventx", "https://eventx.example.com:8443"},
		{"http dev port appended", true, "127.0.0.1.sslip.io", 8080, "eventx-dev", "http://eventx-dev.127.0.0.1.sslip.io:8080"},
		{"http default port elided", true, "localhost", 80, "eventx", "http://eventx.localhost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{HTTPMode: tc.httpMode, BaseDomain: tc.baseDomain, ExternalPort: tc.port}
			if got := m.appURL(tc.label); got != tc.want {
				t.Errorf("appURL(%q) = %q, want %q", tc.label, got, tc.want)
			}
		})
	}
}

func TestManagerProjectDir_ReturnsPath(t *testing.T) {
	mgr, _, _ := setupManagerTest(t)

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
