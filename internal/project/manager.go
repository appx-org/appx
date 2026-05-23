package project

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// agentsTemplate is the root AGENTS.md content scaffolded into every new
// project directory. The Pi runtime also reads the richer project-local harness
// at .pi/AGENTS.md, so this file stays as a short workspace orientation note.
const agentsTemplate = `# Project: {{name}}

## App Port

When running a dev server, always use port {{port}}.
This port is assigned by appx and has proxy routing configured.
Your app will be accessible at {{subdomain}}.

## Guidelines

- Use this port for ALL dev servers (Vite, Next.js, Express, etc.)
- Do not change the port — it is mapped to a subdomain by the appx proxy
- The project directory is the working directory for all commands
- Pi-specific prompt, skill, and extension assets are in .pi/
`

// Manager provides project lifecycle operations. It delegates to the Store for
// database CRUD and handles filesystem operations (directory creation, git init,
// AGENTS.md scaffolding) for new projects.
type Manager struct {
	Store       *Store
	ProjectRoot string
	BaseDomain  string // e.g. "localhost" or "user.appx.app"
}

// NewManager creates a Manager backed by the given project store. The projectRoot
// is the base directory where project subdirectories are created. It is resolved
// to an absolute path so ProjectDir always returns a stable host path.
func NewManager(store *Store, projectRoot string) *Manager {
	abs, err := filepath.Abs(projectRoot)
	if err == nil {
		projectRoot = abs
	}
	return &Manager{
		Store:       store,
		ProjectRoot: projectRoot,
	}
}

// Create creates a new project: inserts a DB record with an auto-assigned port,
// creates the project directory, scaffolds AGENTS.md, runs git init, stages all
// files, and makes an initial commit. If filesystem operations fail, the DB
// record is rolled back.
func (m *Manager) Create(name string) (*Project, error) {
	proj, err := m.Store.Create(name)
	if err != nil {
		return nil, err
	}

	projectDir := filepath.Join(m.ProjectRoot, name)
	if err := m.scaffoldProject(projectDir, proj); err != nil {
		os.RemoveAll(projectDir) // clean up partial directory before DB rollback
		m.Store.Delete(proj.ID)
		return nil, fmt.Errorf("scaffold project: %w", err)
	}

	return proj, nil
}

// Delete removes a project's directory from disk and its record from the database.
// Returns ErrNotFound if the project does not exist.
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

// Get returns a single project by ID. Returns ErrNotFound if not found.
func (m *Manager) Get(id string) (*Project, error) {
	return m.Store.Get(id)
}

// GetByName returns a single project by name. Returns ErrNotFound if not found.
func (m *Manager) GetByName(name string) (*Project, error) {
	return m.Store.GetByName(name)
}

// ProjectDir returns the absolute path to the directory for the project with
// the given name. The directory may or may not exist; this is purely a path
// construction helper for use by API handlers populating the ProjectDir field.
func (m *Manager) ProjectDir(name string) string {
	return filepath.Join(m.ProjectRoot, name)
}

// scaffoldProject creates the project directory, writes AGENTS.md, initializes
// a git repo, stages files, and makes an initial commit.
func (m *Manager) scaffoldProject(dir string, proj *Project) error {
	if err := os.MkdirAll(dir, 0770); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	domain := m.BaseDomain
	if domain == "" {
		domain = "localhost"
	}

	content := agentsTemplate
	content = strings.ReplaceAll(content, "{{name}}", proj.Name)
	content = strings.ReplaceAll(content, "{{port}}", fmt.Sprintf("%d", proj.AssignedPort))
	content = strings.ReplaceAll(content, "{{subdomain}}", fmt.Sprintf("%s.%s", proj.Name, domain))

	agentsPath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write AGENTS.md: %w", err)
	}

	if err := scaffoldPiHarness(dir, proj, domain); err != nil {
		return fmt.Errorf("write .pi harness: %w", err)
	}

	if err := runGit(dir, "init"); err != nil {
		return fmt.Errorf("git init: %w", err)
	}
	if err := runGit(dir, "add", "."); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	if err := runGit(dir, "commit", "-m", "Initial project scaffold"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}

	return nil
}

// runGit executes a git command in the given directory with minimal git config.
func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
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
