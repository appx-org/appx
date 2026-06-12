// Package containerruntime supervises the single outer "builder" container that
// holds agent-server + rootless podman. appx is the control plane: at startup
// (in container mode) it creates the outer container if absent, starts it if
// stopped, and waits for agent-server to become healthy.
//
// The security boundary lives in ContainerSpec.RunArgs: the docker run flag set
// is transcribed VERBATIM from agent-server's container/run-outer.sh (the
// deletion-tested Stage 0/2 recipe). Do not weaken it. See
// agent-server/container/SPIKE-FINDINGS.md for the justification of each flag
// (esp. the file-cap newuidmap fix and why no-new-privileges is forbidden).
package containerruntime

import (
	"fmt"
	"sort"
	"strings"
)

// VolumeMount is a named-volume → container-path bind for the outer container.
type VolumeMount struct {
	// Name is the docker named volume (e.g. "builder-workspace").
	Name string
	// Dest is the absolute mount path inside the container.
	Dest string
}

// ContainerSpec is the full description of the outer builder container. The
// security-relevant fields (Device, SecurityOpts, the absence of privilege)
// are fixed by RunArgs; the configurable fields are image/name/ports/volumes/
// env/limits.
type ContainerSpec struct {
	// Image is the outer image, pinned by tag or digest (e.g. "builder-outer"
	// or "builder-outer@sha256:...").
	Image string
	// Name is the docker container name (e.g. "builder-outer").
	Name string

	// SeccompProfilePath is the absolute host path to the tailored seccomp
	// profile (seccomp-builder.json). Required — the default docker profile
	// blocks mount(2) and breaks nested rootless podman.
	SeccompProfilePath string

	// APIBindHost is the loopback host the agent-server API publishes on
	// (always 127.0.0.1 — appx is the only edge).
	APIBindHost string
	// APIPort is the agent-server API port (4001), published host:container 1:1.
	APIPort int

	// AppBindHost is the loopback host the app port range publishes on
	// (always 127.0.0.1 — appx proxies in).
	AppBindHost string
	// AppPortStart/AppPortEnd is the inclusive published app-port range. MUST
	// match appx's project.PublishedPortRangeEnd (10000-10199).
	AppPortStart int
	AppPortEnd   int

	// Volumes are the persistent named volumes (workspace + podman storage).
	Volumes []VolumeMount

	// Env is the set of explicit KEY=value env vars injected into the container
	// (token, proxy vars, WORKSPACE_DIR). Iterated in sorted order for a stable,
	// testable arg list.
	Env map[string]string
	// EnvPassthrough is the list of env var NAMES forwarded by name (docker -e
	// VAR with no =value): docker forwards the host's value if set and omits the
	// var otherwise. Used for secrets like ANTHROPIC_API_KEY — never baked.
	EnvPassthrough []string

	// AddHosts are extra /etc/hosts entries (host:ip), e.g.
	// "host.docker.internal:host-gateway" so the container can reach the egress
	// proxy on the bridge gateway.
	AddHosts []string

	// Memory / CPUs are optional resource limits (e.g. "2g", "2.0"). Empty = no
	// limit. (Limit POLICY is Stage 4; this is just the plumbing.)
	Memory string
	CPUs   string

	// ReadinessURL is the agent-server health URL appx polls after create/start
	// (e.g. "http://127.0.0.1:4001/").
	ReadinessURL string
}

// RunArgs returns the full `docker run` argument vector (excluding the leading
// binary name). The security flag set is byte-for-byte the proven run-outer.sh
// recipe; appx adds only the explicit env (token/proxy/workspace), the
// passthrough secrets, optional resource limits, and host aliases.
//
// HARD INVARIANTS (do NOT change): no --privileged, no --cap-add, no /dev/fuse,
// no seccomp=unconfined, NEVER --security-opt no-new-privileges (it breaks the
// file-cap newuidmap helpers). The outer process runs as uid 1000.
func (s ContainerSpec) RunArgs() []string {
	args := []string{
		"run", "-d", "--name", s.Name,

		// ── proven security boundary (verbatim from run-outer.sh) ──
		// rootless slirp4netns networking opens /dev/net/tun.
		"--device", "/dev/net/tun",
		// tailored seccomp profile: docker's default blocks mount(2); this is
		// strictly tighter than unconfined (only sethostname/setdomainname/setns
		// ungated). See gen-seccomp.sh.
		"--security-opt", "seccomp=" + s.SeccompProfilePath,
		// docker-default apparmor blocks the rootless overlay mount(2).
		"--security-opt", "apparmor=unconfined",
		// docker masks /proc submounts; the kernel then blocks the inner
		// container's fresh proc mount. Adds no caps/privilege.
		"--security-opt", "systempaths=unconfined",
	}

	// Optional resource limits (Stage 4 policy lives elsewhere; this is plumbing).
	if s.Memory != "" {
		args = append(args, "--memory", s.Memory)
	}
	if s.CPUs != "" {
		args = append(args, "--cpus", s.CPUs)
	}

	// Host aliases (host.docker.internal:host-gateway for egress).
	for _, h := range s.AddHosts {
		args = append(args, "--add-host", h)
	}

	// Secrets passed BY NAME (forwarded from the host env if set, omitted
	// otherwise) — never baked into the image.
	for _, name := range s.EnvPassthrough {
		args = append(args, "-e", name)
	}

	// Explicit env (token, proxy vars, workspace) in sorted order for stability.
	for _, kv := range sortedEnv(s.Env) {
		args = append(args, "-e", kv)
	}

	// Persistent volumes (workspace + podman storage survive recreate).
	for _, v := range s.Volumes {
		args = append(args, "-v", v.Name+":"+v.Dest)
	}

	// Loopback-only publishes. appx is the only edge.
	args = append(args, "-p", fmt.Sprintf("%s:%d:%d", s.APIBindHost, s.APIPort, s.APIPort))
	args = append(args, "-p", fmt.Sprintf("%s:%d-%d:%d-%d",
		s.AppBindHost, s.AppPortStart, s.AppPortEnd, s.AppPortStart, s.AppPortEnd))

	args = append(args, s.Image)
	return args
}

// sortedEnv returns "KEY=value" entries sorted by key for a deterministic arg
// vector (so tests and logs are stable).
func sortedEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

// Validate checks the spec has the fields RunArgs/EnsureRunning require. It is
// not a security check (RunArgs fixes the flags) — just a fail-loud guard
// against a half-built spec.
func (s ContainerSpec) Validate() error {
	var missing []string
	if s.Image == "" {
		missing = append(missing, "image")
	}
	if s.Name == "" {
		missing = append(missing, "name")
	}
	if s.SeccompProfilePath == "" {
		missing = append(missing, "seccomp profile path")
	}
	if s.APIPort == 0 {
		missing = append(missing, "API port")
	}
	if s.AppPortStart == 0 || s.AppPortEnd == 0 {
		missing = append(missing, "app port range")
	}
	if s.ReadinessURL == "" {
		missing = append(missing, "readiness URL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("incomplete container spec: missing %s", strings.Join(missing, ", "))
	}
	return nil
}
