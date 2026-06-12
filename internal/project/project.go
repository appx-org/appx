package project

import (
	"errors"
	"regexp"
	"strings"
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

// PortRangeEnd is the last port in the auto-assignment range. This bounds the
// values that resolve from existing DB rows.
const PortRangeEnd = 10999

// PublishedPortRangeEnd caps *new* allocations. The outer container publishes
// 10000-10199 on the host (one docker-proxy process per port), and each project
// consumes a DEV + PROD pair, so this gives 100 projects. Existing rows above
// the cap still resolve via PortRangeEnd.
const PublishedPortRangeEnd = 10199

// Project represents a project managed by appx. Each project maps to a directory
// on disk containing a git repository. AssignedPort is the PROD port (the
// subdomain proxy routes <name>.<domain> to it); DevPort is the DEV port
// (<name>-dev.<domain>). Both are published on host loopback by the outer
// container and proxied by appx.
type Project struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Status       ProjectStatus `json:"status"`
	AssignedPort int           `json:"assignedPort"`
	// DevPort is the project's DEV-environment host port. Zero for legacy
	// single-port rows created before paired allocation.
	DevPort   int    `json:"devPort"`
	LastError string `json:"lastError,omitempty"`
	CreatedAt string `json:"createdAt"`
	// AppRunning indicates whether a TCP listener is active on the project's
	// assigned port. Populated at query time by the health checker, not persisted.
	AppRunning bool `json:"appRunning"`
	// ProjectDir is the absolute path to the project's directory on the host.
	// Populated at query time by the Manager, not persisted in the database.
	ProjectDir string `json:"projectDir,omitempty"`
}

// EnvTarget is one deployment environment's address: the host port appx
// allocated and the public URL it exposes. The control plane owns both; the
// agent never invents or reports a port back.
type EnvTarget struct {
	Port int
	URL  string
}

// Deployment is the dev + prod metadata appx pushes to agent-server on create
// and reconcile. Defined here (not in the agentserver client) so the
// AgentRegistrar seam and the Manager share one type without the project
// package importing the HTTP client.
type Deployment struct {
	Dev  EnvTarget
	Prod EnvTarget
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

	// ErrReservedNameSuffix is returned when a project name ends in the reserved
	// "-dev" suffix (used to address a project's DEV subdomain).
	ErrReservedNameSuffix = errors.New("invalid project name: the \"-dev\" suffix is reserved")

	// ErrNoPortAvailable is returned when no DEV+PROD port pair is free within
	// the published allocation range (10000-10199).
	ErrNoPortAvailable = errors.New("no port pair available in range 10000-10199")
)

// namePattern enforces project name slugs.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// ValidateName checks that name is a valid project slug. Names ending in "-dev"
// are reserved so the <name>-dev subdomain unambiguously addresses project
// <name>'s DEV environment (a project literally named "foo-dev" would otherwise
// collide with foo's dev URL).
func ValidateName(name string) error {
	if len(name) < 2 || !namePattern.MatchString(name) {
		return ErrInvalidName
	}
	if strings.HasSuffix(name, "-dev") {
		return ErrReservedNameSuffix
	}
	return nil
}
