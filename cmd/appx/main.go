package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"strconv"

	"github.com/neuromaxer/appx/internal/agentserver"
	"github.com/neuromaxer/appx/internal/auth"
	"github.com/neuromaxer/appx/internal/containerruntime"
	"github.com/neuromaxer/appx/internal/db"
	"github.com/neuromaxer/appx/internal/egress"
	"github.com/neuromaxer/appx/internal/project"
	"github.com/neuromaxer/appx/internal/server"
	"github.com/neuromaxer/appx/internal/terminal"
)

// webEmbed holds the built React frontend assets, embedded at compile time.
// The server serves these as the SPA for all non-API routes.
//
//go:embed web/dist/*
var webEmbed embed.FS

// main is the entry point for the appx server. It parses CLI flags, initializes
// the SQLite database and auth store, sets up the project manager, generates a
// password on first run, and starts the HTTPS server.
func main() {
	port := flag.Int("port", 0, "listen port (default: 443 for HTTPS, 8080 for --http)")
	dataDir := flag.String("data", "", "data directory (contains .appx-internals and projects)")
	host := flag.String("host", "", "additional hostname or IP for TLS cert SANs")
	domain := flag.String("domain", "", "domain for automatic Let's Encrypt TLS via Cloudflare DNS (requires CLOUDFLARE_API_TOKEN env var)")
	httpMode := flag.Bool("http", false, "run in plain HTTP mode (localhost only, for local development)")
	recreateAgentContainer := flag.Bool("recreate-agent-container", false, "force-recreate the outer agent container on spec drift (container mode; stops running apps)")
	flag.Parse()

	// Flags fall back to env vars (set via /etc/appx/appx.env in production).
	if *dataDir == "" {
		*dataDir = envOr("APPX_DATA", "./data")
	}
	if *host == "" {
		*host = os.Getenv("APPX_HOST")
	}
	if *domain == "" {
		*domain = os.Getenv("APPX_DOMAIN")
	}
	if *port == 0 {
		if v := os.Getenv("APPX_PORT"); v != "" {
			if p, err := strconv.Atoi(v); err == nil {
				*port = p
			}
		}
	}

	// Default port depends on mode.
	if *port == 0 {
		if *httpMode {
			*port = 8080
		} else {
			*port = 443
		}
	}

	// Compute base domain for cookie scoping and subdomain routing.
	// Priority: --domain flag > APPX_HOST (server IP or hostname) > "localhost".
	// APPX_HOST is set by bootstrap to the server's public IP or hostname, so in
	// production this ensures the session cookie is scoped to the actual origin
	// rather than "localhost" (which browsers reject for IP-based access).
	baseDomain := "localhost"
	if *domain != "" {
		baseDomain = *domain
	} else if *host != "" {
		baseDomain = *host
	}

	internalsDir := filepath.Join(*dataDir, ".appx-internals")
	projectRoot := filepath.Join(*dataDir, "projects")

	if err := os.MkdirAll(internalsDir, 0700); err != nil {
		log.Fatalf("create internals dir: %v", err)
	}

	// Auto-migrate: if DB exists at the old location (directly in dataDir),
	// move it and related files into .appx-internals.
	migrateDataDir(*dataDir, internalsDir)

	database, err := db.Open(internalsDir)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()

	authStore := auth.NewStore(database)

	// Log the server URL before any background goroutines emit their own logs.
	if *httpMode {
		log.Printf("WARNING: running in HTTP mode -- for local development only")
		log.Printf("Appx running on http://%s:%d", baseDomain, *port)
	}

	// Container mode (Stage 3): appx creates/supervises the outer builder
	// container that holds agent-server + rootless podman. Host mode
	// (APPX_AGENT_SERVER_URL) stays the default/fallback (macOS local dev).
	containerMode := envBool("APPX_AGENT_CONTAINER")
	recreateContainer := envBool("APPX_RECREATE_AGENT_CONTAINER") || *recreateAgentContainer

	agentServerURL := envOr("APPX_AGENT_SERVER_URL", "http://127.0.0.1:4001")
	agentServerToken := os.Getenv("APPX_AGENT_SERVER_TOKEN")

	// Egress bind host: loopback by default. In container mode the agent-server
	// runs inside the outer container, where loopback no longer reaches appx, so
	// the CONNECT proxy + internal listener bind on the docker bridge gateway and
	// the container reaches them via host.docker.internal. Overridable.
	egressBindHost := envOr("APPX_EGRESS_BIND", "127.0.0.1")
	var hostGateway string
	if containerMode {
		// Token is mandatory in container mode: the API port is published, so
		// loopback is no longer a sufficient trust boundary (OWASP A01/A07).
		// Generate once + persist 0600; inject into both the container env and
		// the proxy clients.
		tok, err := containerruntime.LoadOrCreateToken(filepath.Join(internalsDir, "agent-server-token"))
		if err != nil {
			log.Fatalf("agent-server token: %v", err)
		}
		agentServerToken = tok

		// Bind egress on the bridge gateway unless explicitly overridden.
		bin := containerruntime.DetectBin(os.Getenv("APPX_CONTAINER_BIN"), exec.LookPath)
		if os.Getenv("APPX_EGRESS_BIND") == "" {
			gw, err := containerruntime.BridgeGateway(context.Background(), bin, execCommandRunner{})
			if err != nil {
				log.Fatalf("container mode: determine docker bridge gateway for egress proxy: %v\n"+
					"  remediation: ensure docker is installed and running, or set APPX_EGRESS_BIND to the gateway IP", err)
			}
			egressBindHost = gw
		}
		hostGateway = "host-gateway"
		log.Printf("container mode: egress proxy bind host %s, agent reaches it via host.docker.internal", egressBindHost)
	}

	// Start egress CONNECT proxy for outbound traffic control.
	egressStore := egress.NewStore(database)
	egressProxy := egress.NewProxy(egressStore)
	proxyAddr := net.JoinHostPort(egressBindHost, egress.ProxyPort)
	go func() {
		if err := egressProxy.ListenAndServe(proxyAddr); err != nil {
			log.Printf("egress proxy error: %v", err)
		}
	}()

	// Start internal listener for agent egress permission requests.
	pendingRegistry := egress.NewPendingRegistry(egressStore)
	internalAddr := net.JoinHostPort(egressBindHost, egress.InternalPort)
	go func() {
		if err := egress.ListenAndServeInternalAddr(pendingRegistry, internalAddr); err != nil {
			log.Printf("egress internal listener error: %v", err)
		}
	}()

	// If no password set, generate one and print it.
	set, err := authStore.IsPasswordSet()
	if err != nil {
		log.Fatalf("check password: %v", err)
	}
	if !set {
		pw, err := authStore.GeneratePassword()
		if err != nil {
			log.Fatalf("generate password: %v", err)
		}
		if err := authStore.SetPassword(pw); err != nil {
			log.Fatalf("set password: %v", err)
		}
		pwFile := filepath.Join(internalsDir, "initial_password")
		if err := os.WriteFile(pwFile, []byte(pw+"\n"), 0600); err != nil {
			log.Fatalf("write password file: %v", err)
		}
		fmt.Fprintf(os.Stderr, "Initial password written to %s — delete this file after logging in.\n", pwFile)
	}

	projectStore := project.NewStore(database)
	if *httpMode {
		// In local dev mode system-setup.sh has not been run, so create the
		// directory on demand with permissive perms (single-user machine).
		if err := os.MkdirAll(projectRoot, 0755); err != nil {
			log.Fatalf("create project root: %v", err)
		}
	} else {
		// In production the directory is created by deploy/system-setup.sh with
		// the correct ownership and setgid bit (appx:projects 2770). Require it
		// to exist so a misconfigured deploy fails loudly rather than creating a
		// directory with wrong permissions.
		if _, err := os.Stat(projectRoot); os.IsNotExist(err) {
			log.Fatalf("project directory %s does not exist — run deploy/system-setup.sh first", projectRoot)
		}
	}
	pm := project.NewManager(projectStore, projectRoot)
	pm.BaseDomain = baseDomain
	// External edge knobs for public DEV/PROD URL construction (appx's own
	// scheme/host/port, not the app's internal port).
	pm.HTTPMode = *httpMode
	pm.ExternalPort = *port

	log.Printf("agent backend: pi (%s)", agentServerURL)

	// In container mode, create/supervise the outer container BEFORE reconcile so
	// agent-server is up and healthy when we (re-)register projects.
	if containerMode {
		ensureOuterContainer(agentServerURL, agentServerToken, egressBindHost, hostGateway, internalsDir, recreateContainer)
	}

	// agent-server owns project runtimes; appx registers/removes projects through it.
	pm.Agent = agentserver.NewClient(agentServerURL, agentServerToken)
	// Best-effort: re-register known projects so existing projects work and an
	// agent-server restart is transparent. Idempotent on the agent-server side.
	if err := pm.ReconcileAgentProjects(context.Background()); err != nil {
		log.Printf("warning: agent-server project reconcile incomplete: %v", err)
	}

	webFS, err := fs.Sub(webEmbed, "web/dist")
	if err != nil {
		log.Fatalf("embed fs: %v", err)
	}

	var hosts []string
	if *host != "" {
		hosts = []string{*host}
	}

	localManager := terminal.NewLocalManager(512 * 1024) // 512 KB ring buffer

	if err := server.Run(server.Config{
		Port:             *port,
		InternalsDir:     internalsDir,
		DB:               database,
		AuthStore:        authStore,
		ProjectManager:   pm,
		WebFS:            webFS,
		TLSHosts:         hosts,
		Domain:           *domain,
		CloudflareToken:  os.Getenv("CLOUDFLARE_API_TOKEN"),
		HTTPMode:         *httpMode,
		BaseDomain:       baseDomain,
		HostAliases:      hosts,
		AgentServerURL:   agentServerURL,
		AgentServerToken: agentServerToken,
		EgressStore:      egressStore,
		EgressPending:    pendingRegistry,
		LocalManager:     localManager,
	}); err != nil {
		log.Fatal(err)
	}
}

// migrateDataDir moves legacy files from the top-level data directory into the
// .appx-internals subdirectory. This is a one-time migration for installations
// created before the directory restructure. Files that don't exist are skipped.
func migrateDataDir(dataDir, internalsDir string) {
	files := []string{"appx.db", "appx.db-wal", "appx.db-shm", "cert.pem", "key.pem", "initial_password"}
	for _, name := range files {
		src := filepath.Join(dataDir, name)
		dst := filepath.Join(internalsDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			log.Printf("migrate %s: %v (copy manually)", name, err)
			continue
		}
		log.Printf("migrated %s → %s", src, dst)
	}
}

// envOr returns the value of the environment variable key, or fallback if unset/empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool returns true when the env var is set to a truthy value.
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// execCommandRunner adapts os/exec to containerruntime.CommandRunner for the
// helper queries (bridge gateway lookup) made directly from main.
type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errBuf strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return []byte(out.String()), []byte(errBuf.String()), err
}

// ensureOuterContainer builds the ContainerSpec from config and creates /
// starts / health-checks the outer builder container. It fails loudly (log.Fatal)
// with a remediation hint when docker is unavailable, the image is missing, the
// container is unhealthy, or the spec drifts — never silently recreating on
// drift (that would kill running user apps).
func ensureOuterContainer(agentServerURL, token, egressBindHost, hostGateway, internalsDir string, recreate bool) {
	bin := containerruntime.DetectBin(os.Getenv("APPX_CONTAINER_BIN"), exec.LookPath)

	// API port + readiness URL derive from the published agent-server URL.
	apiPort := 4001
	readiness := strings.TrimRight(agentServerURL, "/") + "/"
	if u, err := url.Parse(agentServerURL); err == nil && u.Port() != "" {
		if p, perr := strconv.Atoi(u.Port()); perr == nil {
			apiPort = p
		}
	}

	seccomp := os.Getenv("APPX_AGENT_SECCOMP")
	if seccomp == "" {
		log.Fatalf("container mode: APPX_AGENT_SECCOMP is required (absolute path to seccomp-builder.json)\n" +
			"  remediation: run deploy/tools-install.sh (installs the profile) or set APPX_AGENT_SECCOMP")
	}
	if _, err := os.Stat(seccomp); err != nil {
		log.Fatalf("container mode: seccomp profile %s not readable: %v", seccomp, err)
	}

	// Egress proxy reachable from inside the container via host.docker.internal,
	// which --add-host maps to the bridge gateway appx bound the proxy on.
	egressProxyURL := envOr("APPX_AGENT_EGRESS_PROXY_URL", "http://host.docker.internal:"+egress.ProxyPort)

	cfg := containerruntime.Config{
		Image:              envOr("APPX_AGENT_IMAGE", containerruntime.DefaultImage),
		Name:               envOr("APPX_AGENT_CONTAINER_NAME", containerruntime.DefaultName),
		SeccompProfilePath: seccomp,
		APIPort:            apiPort,
		AppPortStart:       project.PortRangeStart,
		AppPortEnd:         project.PublishedPortRangeEnd,
		WorkspaceVolume:    envOr("APPX_AGENT_WORKSPACE_VOLUME", containerruntime.DefaultWorkspaceVolume),
		PodmanVolume:       envOr("APPX_AGENT_PODMAN_VOLUME", containerruntime.DefaultPodmanVolume),
		Token:              token,
		EnvPassthrough:     passthroughKeys(),
		HostGateway:        hostGateway,
		EgressProxyURL:     egressProxyURL,
		NoProxy:            envOr("APPX_AGENT_NO_PROXY", defaultNoProxy),
		Memory:             os.Getenv("APPX_AGENT_MEMORY"),
		CPUs:               os.Getenv("APPX_AGENT_CPUS"),
		ReadinessURL:       readiness,
	}
	spec := containerruntime.BuildSpec(cfg)

	sup := containerruntime.NewDockerSupervisor(bin,
		containerruntime.WithReadyTimeout(containerReadyTimeout()))

	ctx, cancel := context.WithTimeout(context.Background(), containerReadyTimeout()+30*time.Second)
	defer cancel()

	var err error
	if recreate {
		log.Printf("container mode: --recreate-agent-container set — recreating %q", spec.Name)
		err = sup.Recreate(ctx, spec)
	} else {
		err = sup.EnsureRunning(ctx, spec)
	}
	if err == nil {
		log.Printf("container mode: outer container %q is up and healthy (image %s)", spec.Name, spec.Image)
		return
	}

	// Structured remediation per failure class.
	var drift *containerruntime.SpecDriftError
	switch {
	case errors.As(err, &drift):
		log.Fatalf("container mode: %v", drift)
	case errors.Is(err, containerruntime.ErrDaemonUnavailable):
		log.Fatalf("container mode: container runtime unavailable: %v\n"+
			"  remediation: ensure rootful Docker is running and the appx user is in the 'docker' group (deploy/system-setup.sh wires this; needs a re-login/service restart to take effect), then restart appx", err)
	case errors.Is(err, containerruntime.ErrImageMissing):
		log.Fatalf("container mode: %v\n"+
			"  remediation: build or pull the outer image (deploy/tools-install.sh), or set APPX_AGENT_IMAGE to an available tag/digest", err)
	case errors.Is(err, containerruntime.ErrUnhealthy):
		log.Fatalf("container mode: %v\n"+
			"  remediation: check `%s logs %s` — agent-server started but never answered %s", err, bin, spec.Name, spec.ReadinessURL)
	default:
		log.Fatalf("container mode: ensure outer container: %v", err)
	}
}

// defaultNoProxy is the NO_PROXY value injected into the outer container in
// container mode. It keeps in-container loopback direct (app↔agent traffic) AND
// bypasses common container image registries: HTTPS_PROXY is honoured by podman
// (not just Node), so without these entries every `podman pull` of a base image
// would be forced through appx's LLM egress allowlist and rejected (403). The
// egress proxy's job is to control agent-server's secret-bearing LLM traffic
// (api.anthropic.com etc.), which is NOT listed here and so still traverses it.
// Trade-off documented in docs/plans/phase_9_plan.md (Stage 3, egress).
const defaultNoProxy = "localhost,127.0.0.1,.docker.io,.docker.com,ghcr.io,.ghcr.io,quay.io,.quay.io,registry.k8s.io,.gcr.io,gcr.io"

// passthroughKeys returns the env var NAMES forwarded by name into the container
// (secrets — never baked). ANTHROPIC_API_KEY always; extend via
// APPX_AGENT_ENV_PASSTHROUGH (comma-separated).
func passthroughKeys() []string {
	keys := []string{"ANTHROPIC_API_KEY"}
	if extra := os.Getenv("APPX_AGENT_ENV_PASSTHROUGH"); extra != "" {
		for _, k := range strings.Split(extra, ",") {
			if k = strings.TrimSpace(k); k != "" && k != "ANTHROPIC_API_KEY" {
				keys = append(keys, k)
			}
		}
	}
	return keys
}

// containerReadyTimeout bounds the health poll after create/start (default 120s;
// the cold podman warmup + Node boot can take a while on a fresh volume).
func containerReadyTimeout() time.Duration {
	if v := os.Getenv("APPX_AGENT_READY_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 120 * time.Second
}
