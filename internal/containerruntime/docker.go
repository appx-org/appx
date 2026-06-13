package containerruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// CommandRunner runs a single command and returns its stdout/stderr. The docker
// CLI is invoked through this seam so unit tests can script responses without a
// real daemon (mirrors the project.AgentRegistrar fake-at-the-seam pattern).
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

// execRunner is the production CommandRunner backed by os/exec.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return []byte(out.String()), []byte(errBuf.String()), err
}

// DockerSupervisor implements Supervisor by shelling out to the docker (or
// podman) CLI. The CLI was chosen over the Docker Go SDK deliberately (D3): one
// container's lifecycle does not justify the SDK's dependency tree, and CLI
// compatibility means the host runtime can be docker OR podman for free.
type DockerSupervisor struct {
	// bin is the CLI binary ("docker" or "podman").
	bin string
	// runner executes CLI commands (seam for tests).
	runner CommandRunner
	// ping probes the readiness URL; nil uses the default HTTP probe.
	ping func(ctx context.Context, url string) error
	// readyTimeout bounds the health poll after create/start.
	readyTimeout time.Duration
	// pollInterval is the gap between readiness probes.
	pollInterval time.Duration
}

// Option configures a DockerSupervisor.
type Option func(*DockerSupervisor)

// WithRunner overrides the command runner (tests).
func WithRunner(r CommandRunner) Option { return func(d *DockerSupervisor) { d.runner = r } }

// WithPing overrides the readiness probe (tests).
func WithPing(p func(ctx context.Context, url string) error) Option {
	return func(d *DockerSupervisor) { d.ping = p }
}

// WithReadyTimeout sets the health-poll timeout.
func WithReadyTimeout(d time.Duration) Option {
	return func(s *DockerSupervisor) { s.readyTimeout = d }
}

// WithPollInterval sets the readiness poll interval.
func WithPollInterval(d time.Duration) Option {
	return func(s *DockerSupervisor) { s.pollInterval = d }
}

// NewDockerSupervisor builds a supervisor that drives the given CLI binary
// ("docker" or "podman").
func NewDockerSupervisor(bin string, opts ...Option) *DockerSupervisor {
	d := &DockerSupervisor{
		bin:          bin,
		runner:       execRunner{},
		readyTimeout: 90 * time.Second,
		pollInterval: time.Second,
	}
	for _, o := range opts {
		o(d)
	}
	if d.ping == nil {
		d.ping = defaultPing
	}
	return d
}

// dockerInspect is the slice of `docker inspect` JSON appx cares about.
type dockerInspect struct {
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
	} `json:"State"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
	Image      string `json:"Image"`
	HostConfig struct {
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
	} `json:"HostConfig"`
}

// Status inspects the named container and classifies daemon/absence errors.
func (d *DockerSupervisor) Status(ctx context.Context, name string) (ContainerStatus, error) {
	stdout, stderr, err := d.runner.Run(ctx, d.bin, "inspect", "--type", "container", name)
	if err != nil {
		msg := string(stderr)
		if isDaemonUnavailable(msg) {
			return ContainerStatus{}, fmt.Errorf("%w: %s", ErrDaemonUnavailable, strings.TrimSpace(msg))
		}
		if isNoSuchObject(msg) {
			return ContainerStatus{Exists: false}, nil
		}
		return ContainerStatus{}, fmt.Errorf("%s inspect %q: %v: %s", d.bin, name, err, strings.TrimSpace(msg))
	}

	var inspected []dockerInspect
	if err := json.Unmarshal(stdout, &inspected); err != nil {
		return ContainerStatus{}, fmt.Errorf("parse %s inspect output: %w", d.bin, err)
	}
	if len(inspected) == 0 {
		return ContainerStatus{Exists: false}, nil
	}
	di := inspected[0]
	published := map[string]bool{}
	for port, bindings := range di.HostConfig.PortBindings {
		if len(bindings) > 0 {
			published[port] = true
		}
	}
	return ContainerStatus{
		Exists:         true,
		Running:        di.State.Running,
		State:          di.State.Status,
		Image:          di.Config.Image,
		ImageID:        di.Image,
		PublishedPorts: published,
	}, nil
}

// EnsureRunning implements the idempotent create/start/noop + health state
// machine. See the Supervisor interface doc for the drift contract.
func (d *DockerSupervisor) EnsureRunning(ctx context.Context, spec ContainerSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	status, err := d.Status(ctx, spec.Name)
	if err != nil {
		return err
	}

	switch {
	case !status.Exists:
		log.Printf("containerruntime: outer container %q absent — creating", spec.Name)
		if err := d.create(ctx, spec); err != nil {
			return err
		}
	default:
		if reasons := spec.driftReasons(status); len(reasons) > 0 {
			return &SpecDriftError{Name: spec.Name, Reasons: reasons}
		}
		if !status.Running {
			log.Printf("containerruntime: outer container %q is %q — starting", spec.Name, status.State)
			if err := d.start(ctx, spec.Name); err != nil {
				return err
			}
		} else {
			log.Printf("containerruntime: outer container %q already running", spec.Name)
		}
	}

	return d.waitHealthy(ctx, spec)
}

// Recreate force-removes and recreates the container (explicit operator action).
func (d *DockerSupervisor) Recreate(ctx context.Context, spec ContainerSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	log.Printf("containerruntime: recreating outer container %q (explicit operator action — running inner apps will be stopped)", spec.Name)
	// rm -f is idempotent for our purposes; ignore "no such object".
	if _, stderr, err := d.runner.Run(ctx, d.bin, "rm", "-f", spec.Name); err != nil {
		msg := string(stderr)
		if isDaemonUnavailable(msg) {
			return fmt.Errorf("%w: %s", ErrDaemonUnavailable, strings.TrimSpace(msg))
		}
		if !isNoSuchObject(msg) {
			return fmt.Errorf("%s rm -f %q: %v: %s", d.bin, spec.Name, err, strings.TrimSpace(msg))
		}
	}
	if err := d.create(ctx, spec); err != nil {
		return err
	}
	return d.waitHealthy(ctx, spec)
}

func (d *DockerSupervisor) create(ctx context.Context, spec ContainerSpec) error {
	_, stderr, err := d.runner.Run(ctx, d.bin, spec.RunArgs()...)
	if err != nil {
		msg := string(stderr)
		if isDaemonUnavailable(msg) {
			return fmt.Errorf("%w: %s", ErrDaemonUnavailable, strings.TrimSpace(msg))
		}
		if isImageMissing(msg) {
			return fmt.Errorf("%w: %q: %s", ErrImageMissing, spec.Image, strings.TrimSpace(msg))
		}
		return fmt.Errorf("%s run %q: %v: %s", d.bin, spec.Name, err, strings.TrimSpace(msg))
	}
	return nil
}

func (d *DockerSupervisor) start(ctx context.Context, name string) error {
	_, stderr, err := d.runner.Run(ctx, d.bin, "start", name)
	if err != nil {
		msg := string(stderr)
		if isDaemonUnavailable(msg) {
			return fmt.Errorf("%w: %s", ErrDaemonUnavailable, strings.TrimSpace(msg))
		}
		return fmt.Errorf("%s start %q: %v: %s", d.bin, name, err, strings.TrimSpace(msg))
	}
	return nil
}

// waitHealthy polls the readiness URL until it answers or the timeout fires.
func (d *DockerSupervisor) waitHealthy(ctx context.Context, spec ContainerSpec) error {
	deadline := time.Now().Add(d.readyTimeout)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	var lastErr error
	for {
		if err := d.ping(ctx, spec.ReadinessURL); err == nil {
			log.Printf("containerruntime: outer container %q healthy at %s", spec.Name, spec.ReadinessURL)
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %q at %s after %s (last probe error: %v)",
				ErrUnhealthy, spec.Name, spec.ReadinessURL, d.readyTimeout, lastErr)
		case <-time.After(d.pollInterval):
		}
	}
}

// defaultPing performs a GET against the readiness URL and treats any HTTP
// response (even 401) as "agent-server is up". A transport error means not yet.
func defaultPing(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func isDaemonUnavailable(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "cannot connect to the docker daemon") ||
		strings.Contains(s, "is the docker daemon running") ||
		strings.Contains(s, "cannot connect to podman") ||
		strings.Contains(s, "permission denied while trying to connect") ||
		strings.Contains(s, "command not found") ||
		strings.Contains(s, "executable file not found")
}

func isNoSuchObject(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "no such object") ||
		strings.Contains(s, "no such container")
}

func isImageMissing(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "unable to find image") ||
		strings.Contains(s, "manifest unknown") ||
		strings.Contains(s, "no such image") ||
		strings.Contains(s, "image not known") ||
		strings.Contains(s, "pull access denied")
}
