package terminal

import (
	"fmt"
	"path"
	"strings"
)

// DefaultContainerShell is the shell launched inside the outer container. The
// agent-server image is Debian-based, so bash is present; override per
// deployment with APPX_PROJECT_SHELL if a future image drops it.
const DefaultContainerShell = "/bin/bash"

// ContainerShell describes how to open an interactive shell inside the agent's
// outer container. appx cannot reach project files on the host in container
// mode — the workspace is a Docker volume mounted at WorkspaceDir inside the
// container — so a project terminal has to be an exec into that container
// rather than a local PTY.
type ContainerShell struct {
	// Bin is the container CLI to drive ("docker" or "podman").
	Bin string
	// Container is the outer container's name (e.g. "builder-outer").
	Container string
	// WorkspaceDir is the in-container workspace root that project directories
	// live under (agent-server's WORKSPACE_DIR, e.g. "/workspace").
	WorkspaceDir string
	// Shell is the shell to exec. Empty defaults to DefaultContainerShell.
	Shell string
}

// CommandFor builds the CommandSpec that opens a shell in the given project's
// directory inside the container. projectName must be a validated project slug
// (see project.ValidateName): it is interpolated into an in-container path, and
// the slug grammar is what keeps that path from escaping WorkspaceDir.
//
// The arguments are passed as a vector to exec (never through a shell), so
// there is no command-injection surface even if the name grammar changes.
func (c ContainerShell) CommandFor(projectName string) (CommandSpec, error) {
	if c.Bin == "" {
		return CommandSpec{}, fmt.Errorf("container shell: no container CLI configured")
	}
	if c.Container == "" {
		return CommandSpec{}, fmt.Errorf("container shell: no container name configured")
	}
	if err := validSlug(projectName); err != nil {
		return CommandSpec{}, fmt.Errorf("container shell: %w", err)
	}

	workspace := c.WorkspaceDir
	if workspace == "" {
		workspace = "/workspace"
	}
	shell := c.Shell
	if shell == "" {
		shell = DefaultContainerShell
	}

	return CommandSpec{
		Name: c.Bin,
		Args: []string{
			"exec", "-i", "-t",
			// TERM has to be set for the exec'd process, not for the docker CLI.
			"-e", "TERM=xterm-256color",
			"-w", path.Join(workspace, projectName),
			c.Container,
			shell, "-l",
		},
	}, nil
}

// validSlug rejects anything that could turn an interpolated project name into
// a different path (separators, traversal, empty). Defence in depth: names are
// already validated on create, but this is the point where one becomes a path.
func validSlug(name string) error {
	if name == "" {
		return fmt.Errorf("empty project name")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("project name %q is not a single path segment", name)
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return fmt.Errorf("project name %q contains an unexpected character %q", name, r)
	}
	return nil
}
