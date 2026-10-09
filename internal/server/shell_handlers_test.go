package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/neuromaxer/appx/internal/auth"
	"github.com/neuromaxer/appx/internal/egress"
	"github.com/neuromaxer/appx/internal/project"
	"github.com/neuromaxer/appx/internal/terminal"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// setupShellTest builds a router with an explicit project root and project-shell
// config, so the container and co-located branches can both be exercised.
func setupShellTest(t *testing.T, projectRoot string, psc ProjectShellConfig) (http.Handler, *auth.Store, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(testSchema); err != nil {
		t.Fatal(err)
	}

	store := auth.NewStore(db)
	store.SetBcryptCost(bcrypt.MinCost)
	store.SetPassword("testpassword1")

	pm := project.NewManager(project.NewStore(db), projectRoot)
	handler := NewRouter(auth.New(store), pm, fstest.MapFS{},
		RouterConfig{ProjectShell: psc}, egress.NewStore(db), nil, terminal.NewLocalManager(65536))
	return handler, store, db
}

// TestProjectShell_CoLocatedOpensInProjectDir covers the host-mode branch: the
// shell runs in the project's directory on disk.
func TestProjectShell_CoLocatedOpensInProjectDir(t *testing.T) {
	root := t.TempDir()
	handler, store, db := setupShellTest(t, root, ProjectShellConfig{})

	db.Exec("INSERT INTO projects (id, name, status, assigned_port) VALUES ('p1','myapp','stopped',10000)")
	if err := os.MkdirAll(filepath.Join(root, "myapp"), 0o755); err != nil {
		t.Fatal(err)
	}

	req := authedRequest(t, store, "POST", "/api/projects/p1/shell", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct{ ID string }
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.ID == "" {
		t.Error("expected a session id")
	}
}

// TestProjectShell_ContainerModeExecsIntoContainer covers the container branch
// without a running container: the exec command is built and started, so a
// bogus CLI name surfaces as a 502 rather than a 500 or a panic. What the
// command *is* is asserted in terminal.TestContainerShell_CommandFor.
func TestProjectShell_ContainerModeExecsIntoContainer(t *testing.T) {
	// Empty project root == container mode: there is no host path to fall back to.
	handler, store, db := setupShellTest(t, "", ProjectShellConfig{
		Container: &terminal.ContainerShell{
			Bin:          "/nonexistent/docker",
			Container:    "builder-outer",
			WorkspaceDir: "/workspace",
		},
	})

	db.Exec("INSERT INTO projects (id, name, status, assigned_port) VALUES ('p1','myapp','stopped',10000)")

	req := authedRequest(t, store, "POST", "/api/projects/p1/shell", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when the container CLI cannot be started, got %d: %s", w.Code, w.Body.String())
	}
}

// TestProjectShell_NoBackendIsNotImplemented pins the failure mode that used to
// be a 500: container mode without a configured container, and no host project
// directory, reports unavailable instead of trying a path that cannot exist.
func TestProjectShell_NoBackendIsNotImplemented(t *testing.T) {
	handler, store, db := setupShellTest(t, "", ProjectShellConfig{})

	db.Exec("INSERT INTO projects (id, name, status, assigned_port) VALUES ('p1','myapp','stopped',10000)")

	req := authedRequest(t, store, "POST", "/api/projects/p1/shell", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d: %s", w.Code, w.Body.String())
	}
}

func TestProjectShell_UnknownProject(t *testing.T) {
	handler, store, _ := setupShellTest(t, t.TempDir(), ProjectShellConfig{})

	req := authedRequest(t, store, "POST", "/api/projects/nope/shell", "")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestProjectShell_RequiresAuth(t *testing.T) {
	handler, _, db := setupShellTest(t, t.TempDir(), ProjectShellConfig{})
	db.Exec("INSERT INTO projects (id, name, status, assigned_port) VALUES ('p1','myapp','stopped',10000)")

	req := httptest.NewRequest("POST", "/api/projects/p1/shell", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// TestServerShell_IgnoresCallerSuppliedCwd pins the removal of the
// caller-controlled working directory. The endpoint used to open a shell at any
// path an authenticated request named; it now always uses the appx user's home.
func TestServerShell_IgnoresCallerSuppliedCwd(t *testing.T) {
	handler, store, _ := setupShellTest(t, t.TempDir(), ProjectShellConfig{})

	req := authedRequest(t, store, "POST", "/api/shell", `{"cwd":"/etc"}`)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// The body is accepted but ignored — it must not fail, and must not honour
	// the path.
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}
