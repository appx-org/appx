package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"strconv"

	"github.com/neuromaxer/appx/internal/auth"
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

	// Start egress CONNECT proxy for outbound traffic control.
	egressStore := egress.NewStore(database)
	egressProxy := egress.NewProxy(egressStore)
	go func() {
		if err := egressProxy.ListenAndServe(egress.ProxyAddr); err != nil {
			log.Printf("egress proxy error: %v", err)
		}
	}()

	// Start internal listener for agent egress permission requests.
	pendingRegistry := egress.NewPendingRegistry(egressStore)
	go func() {
		if err := egress.ListenAndServeInternal(pendingRegistry); err != nil {
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

	agentServerURL := envOr("APPX_AGENT_SERVER_URL", "http://127.0.0.1:4001")
	agentServerToken := os.Getenv("APPX_AGENT_SERVER_TOKEN")
	log.Printf("agent backend: pi (%s)", agentServerURL)

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
