package project

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
)

// AgentRegistrar registers/removes projects with the Pi agent-server, which
// owns project identity, on-disk layout (including each project's `.pi/`), and
// the durable runtime registry. Kept as an interface (rather than importing the
// agentserver package) so the project package stays dependency-light and easy
// to test with a fake.
type AgentRegistrar interface {
	// EnsureProject registers a project by name (idempotent on name). The
	// agent-server creates WORKSPACE_DIR/{id}/ and persists project metadata.
	EnsureProject(ctx context.Context, name string) error
	// DeleteProject removes a project by its agent-server id (idempotent),
	// including its directory and session transcripts.
	DeleteProject(ctx context.Context, id string) error
}

// Manager provides project lifecycle operations. appx is a control plane: it
// owns a per-project record (assigned port, subdomain, owning user, health) in
// its own store and asks the agent-server to create/remove the project. The
// agent-server owns the project directory, its `.pi/` harness, and session
// transcripts — appx no longer scaffolds the filesystem (see
// .superpowers/specs/2026-06-09-project-ownership-and-agent-chat-integration-adr.md).
//
// agent-server's project id equals the appx project name (appx names already
// satisfy the slug grammar, so `slugify(name) == name`).
type Manager struct {
	Store       *Store
	ProjectRoot string
	// BaseDomain is retained for control-plane URL construction and future
	// harness templating; it no longer drives any filesystem scaffolding.
	BaseDomain string
	Agent      AgentRegistrar // optional; nil disables agent-server registration
}

// NewManager creates a Manager backed by the given project store. The projectRoot
// is the base directory where project subdirectories live (in a co-located
// deployment it must equal agent-server's WORKSPACE_DIR). It is resolved to an
// absolute path so ProjectDir always returns a stable host path.
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

// Create registers a new project. It first reserves the appx control-plane
// record (name validation + atomic port assignment), then asks the agent-server
// to create the project (which owns the on-disk directory + durable registry).
//
// The appx record is inserted first because it is the cheap, transactional,
// trivially-rolled-back half; agent-server registration (an idempotent upsert)
// follows. If registration fails, the appx record is removed so a failed create
// leaves no partial state. We deliberately do NOT delete the agent-server
// project on rollback: EnsureProject is an idempotent upsert that may have
// matched a pre-existing project, and deleting it could destroy another owner's
// directory and transcripts.
func (m *Manager) Create(ctx context.Context, name string) (*Project, error) {
	proj, err := m.Store.Create(name)
	if err != nil {
		return nil, err
	}

	if m.Agent != nil {
		if err := m.Agent.EnsureProject(ctx, proj.Name); err != nil {
			// Roll back only our own freshly-created record.
			_ = m.Store.Delete(proj.ID)
			return nil, fmt.Errorf("register project with agent-server: %w", err)
		}
	}

	return proj, nil
}

// Delete removes a project. The agent-server owns the directory and session
// transcripts, so DeleteProject (idempotent) removes WORKSPACE_DIR/{id}/ and
// .pi-global/sessions/{id}/. appx then drops its own control-plane record. appx
// never touches the filesystem — in the target container deployment it has no
// access to the agent-server volume. Returns ErrNotFound if the project does
// not exist.
func (m *Manager) Delete(ctx context.Context, id string) error {
	proj, err := m.Store.Get(id)
	if err != nil {
		return err
	}

	if m.Agent != nil {
		// agent-server id == project name.
		if err := m.Agent.DeleteProject(ctx, proj.Name); err != nil {
			return fmt.Errorf("deregister project from agent-server: %w", err)
		}
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
// the given name. The directory is created and owned by the agent-server; this
// is purely a path-construction helper for control-plane features that run on a
// shared filesystem (e.g. the local terminal). The directory may or may not
// exist.
func (m *Manager) ProjectDir(name string) string {
	return filepath.Join(m.ProjectRoot, name)
}

// ReconcileAgentProjects re-registers every known project with the agent-server.
// Registration is idempotent, so this is safe to run at startup to (a) register
// projects that predate agent-server ownership and (b) rehydrate the agent-server
// after it (or appx) restarts. It is best-effort: individual failures are
// returned joined but never abort the caller's boot — the per-project create/
// proxy paths remain the authoritative registration points.
func (m *Manager) ReconcileAgentProjects(ctx context.Context) error {
	if m.Agent == nil {
		return nil
	}
	projects, err := m.Store.List()
	if err != nil {
		return fmt.Errorf("list projects for reconcile: %w", err)
	}
	var errs []error
	for _, proj := range projects {
		if err := m.Agent.EnsureProject(ctx, proj.Name); err != nil {
			errs = append(errs, fmt.Errorf("register %q: %w", proj.Name, err))
		}
	}
	return errors.Join(errs...)
}
