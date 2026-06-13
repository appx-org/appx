package containerruntime

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Supervisor creates, starts, and health-checks the outer builder container.
// The docker-CLI implementation is DockerSupervisor; tests use a fake.
type Supervisor interface {
	// EnsureRunning is idempotent: it creates the container if absent, starts it
	// if stopped, no-ops if running, then polls the readiness URL until healthy
	// (or the context/timeout fires). If the existing container's spec drifts
	// from the desired one (image tag or published ports), it returns a
	// *SpecDriftError WITHOUT recreating — recreation destroys running user apps
	// and must be an explicit operator action (see Recreate).
	EnsureRunning(ctx context.Context, spec ContainerSpec) error
	// Status reports the current state of the named container.
	Status(ctx context.Context, name string) (ContainerStatus, error)
	// Recreate force-removes the existing container and creates it fresh from
	// spec, then waits for health. This is the explicit, operator-initiated
	// remediation for spec drift (--recreate-agent-container); it WILL kill
	// running inner apps, though the named volumes preserve workspace + podman
	// storage.
	Recreate(ctx context.Context, spec ContainerSpec) error
}

// ContainerStatus is the observed state of the outer container.
type ContainerStatus struct {
	// Exists is false when no container with the given name is present.
	Exists bool
	// Running is true when State.Status == "running".
	Running bool
	// State is the raw docker state string ("running", "exited", "created", ...).
	State string
	// Image is the configured image reference (tag) the container was created
	// from (Config.Image).
	Image string
	// ImageID is the resolved image content id (.Image, a sha256).
	ImageID string
	// PublishedPorts is the set of container ports that have a host binding,
	// e.g. {"4001/tcp", "10000/tcp", ...}. Used for drift detection.
	PublishedPorts map[string]bool
}

// Structured error sentinels. Callers use errors.Is to surface a precise
// remediation hint (image missing vs daemon down vs unhealthy vs drift).
var (
	// ErrDaemonUnavailable means the container runtime (docker/podman) could not
	// be reached at all — almost always "not installed" or "daemon not running"
	// or "this user can't talk to the socket".
	ErrDaemonUnavailable = errors.New("container runtime unavailable")
	// ErrImageMissing means the configured image is not present locally and could
	// not be pulled.
	ErrImageMissing = errors.New("outer container image missing")
	// ErrUnhealthy means the container started but agent-server never answered the
	// readiness probe within the timeout.
	ErrUnhealthy = errors.New("outer container did not become healthy")
)

// SpecDriftError is returned when the running container's image or published
// ports differ from the desired spec. appx never silently recreates on drift —
// that kills running user apps — so this surfaces remediation instead.
type SpecDriftError struct {
	Name    string
	Reasons []string
}

func (e *SpecDriftError) Error() string {
	return fmt.Sprintf(
		"outer container %q does not match desired spec (%s); refusing to recreate automatically (would kill running apps). "+
			"Re-run with --recreate-agent-container (or APPX_RECREATE_AGENT_CONTAINER=true) to recreate it explicitly.",
		e.Name, strings.Join(e.Reasons, "; "))
}

// driftReasons compares an existing container's observed status to the desired
// spec and returns human-readable mismatch reasons (empty = no drift). It checks
// the image reference and that the API port + the app-range boundaries are
// published — the operationally meaningful, cheap-to-verify properties.
func (s ContainerSpec) driftReasons(status ContainerStatus) []string {
	var reasons []string
	if status.Image != s.Image {
		reasons = append(reasons, fmt.Sprintf("image %q != desired %q", status.Image, s.Image))
	}
	want := []int{s.APIPort, s.AppPortStart, s.AppPortEnd}
	for _, p := range want {
		key := strconv.Itoa(p) + "/tcp"
		if !status.PublishedPorts[key] {
			reasons = append(reasons, fmt.Sprintf("port %s not published", key))
		}
	}
	return reasons
}
