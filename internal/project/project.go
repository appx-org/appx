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

// PortRangeStart is the first port in the auto-assignment range.
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
	// AppRunning indicates whether a TCP listener is active on the project's
	// assigned port. Populated at query time by the health checker, not persisted.
	AppRunning bool `json:"appRunning"`
	// ProjectDir is the absolute path to the project's directory on the host.
	// Populated at query time by the Manager, not persisted in the database.
	ProjectDir string `json:"projectDir,omitempty"`
}

var (
	// ErrInvalidName is returned when a project name does not match the slug pattern.
	ErrInvalidName = errors.New("invalid project name: must match [a-z][a-z0-9-]{0,61}")

	// ErrDuplicateName is returned when a project with the same name already exists.
	ErrDuplicateName = errors.New("project name already exists")

	// ErrNotFound is returned when the requested project does not exist in the database.
	ErrNotFound = errors.New("project not found")

	// ErrInvalidState is retained for backward compatibility.
	ErrInvalidState = errors.New("invalid state transition")

	// ErrNoPortAvailable is returned when all ports in 10000-10999 are allocated.
	ErrNoPortAvailable = errors.New("no port available in range 10000-10999")
)

// namePattern enforces project name slugs.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// ValidateName checks that name is a valid project slug.
func ValidateName(name string) error {
	if len(name) < 2 || !namePattern.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}
