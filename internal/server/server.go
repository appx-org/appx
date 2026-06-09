package server

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
	"github.com/neuromaxer/appx/internal/auth"
	"github.com/neuromaxer/appx/internal/egress"
	"github.com/neuromaxer/appx/internal/project"
	"github.com/neuromaxer/appx/internal/terminal"
	appxtls "github.com/neuromaxer/appx/internal/tls"
)

// Config holds all dependencies needed to start the HTTPS server.
// It is constructed in main() and passed to Run().
type Config struct {
	Port             int
	InternalsDir     string // path to .appx-internals (DB, TLS certs)
	DB               *sql.DB
	AuthStore        *auth.Store
	ProjectManager   *project.Manager
	WebFS            fs.FS
	TLSHosts         []string
	Domain           string
	CloudflareToken  string
	HTTPMode         bool     // true = plain HTTP, locked to localhost
	BaseDomain       string   // "localhost" in HTTP mode, Domain value in production
	HostAliases      []string // additional hosts that serve the dashboard (e.g. server IP or hostname)
	AgentServerURL   string
	AgentServerToken string
	EgressStore      *egress.Store
	EgressPending    *egress.PendingRegistry
	LocalManager     *terminal.LocalManager
}

// Run starts the HTTPS server and blocks until it receives SIGINT/SIGTERM or
// encounters a fatal error. When Domain and CloudflareToken are set, it uses
// CertMagic for automatic Let's Encrypt certificates; otherwise it falls back
// to a self-signed certificate.
func Run(cfg Config) error {
	if cfg.HTTPMode && cfg.Domain != "" {
		return fmt.Errorf("--http and --domain are mutually exclusive")
	}

	a := auth.New(cfg.AuthStore)
	if cfg.BaseDomain != "" && net.ParseIP(cfg.BaseDomain) == nil {
		// Only set a Domain attribute for hostnames, not IP addresses.
		// Browsers reject cookies where Domain doesn't match the origin host
		// (RFC 6265), so setting Domain=.localhost when accessed via an IP
		// silently drops the cookie and breaks login. For IP-based access the
		// cookie is host-only (no Domain attribute) which is correct — subdomain
		// routing doesn't apply to IP addresses anyway.
		a.Cookie.Domain = "." + cfg.BaseDomain
	}
	a.Cookie.Secure = !cfg.HTTPMode
	a.Store.CleanExpiredSessions()

	cleanupTicker := time.NewTicker(1 * time.Hour)
	defer cleanupTicker.Stop()
	go func() {
		for range cleanupTicker.C {
			a.Store.CleanExpiredSessions()
			if cfg.EgressStore != nil {
				if err := cfg.EgressStore.PruneLog(30 * 24 * time.Hour); err != nil {
					log.Printf("prune egress log: %v", err)
				}
			}
		}
	}()

	handler := NewRouter(a, cfg.ProjectManager, cfg.WebFS, RouterConfig{
		HTTPMode:         cfg.HTTPMode,
		BaseDomain:       cfg.BaseDomain,
		HostAliases:      cfg.HostAliases,
		AgentServerURL:   cfg.AgentServerURL,
		AgentServerToken: cfg.AgentServerToken,
	}, cfg.EgressStore, cfg.EgressPending, cfg.LocalManager)

	if cfg.HTTPMode {
		return runHTTP(cfg, handler)
	}

	if cfg.Domain != "" {
		if cfg.CloudflareToken == "" {
			return fmt.Errorf("--domain requires CLOUDFLARE_API_TOKEN to be set")
		}
		return runWithCertMagic(cfg, handler)
	}
	return runWithSelfSigned(cfg, handler)
}

func runWithCertMagic(cfg Config, handler http.Handler) error {
	storage := &certmagic.FileStorage{
		Path: filepath.Join(cfg.InternalsDir, "certmagic"),
	}

	magic := certmagic.NewDefault()
	magic.Storage = storage

	issuer := certmagic.NewACMEIssuer(magic, certmagic.ACMEIssuer{
		Agreed: true,
		Email:  "admin@" + cfg.Domain,
		DNS01Solver: &certmagic.DNS01Solver{
			DNSManager: certmagic.DNSManager{
				DNSProvider: &cloudflare.Provider{
					APIToken: cfg.CloudflareToken,
				},
			},
		},
	})
	magic.Issuers = []certmagic.Issuer{issuer}

	domains := []string{cfg.Domain, "*." + cfg.Domain}
	if err := magic.ManageSync(context.Background(), domains); err != nil {
		return fmt.Errorf("certmagic: %w", err)
	}

	tlsConfig := magic.TLSConfig()
	tlsConfig.MinVersion = tls.VersionTLS13

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	return serve(srv, cfg.Port, true)
}

func runWithSelfSigned(cfg Config, handler http.Handler) error {
	cert, err := appxtls.LoadOrGenerateSelfSigned(cfg.InternalsDir, cfg.TLSHosts...)
	if err != nil {
		return fmt.Errorf("tls setup: %w", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           handler,
		TLSConfig:         tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	return serve(srv, cfg.Port, false)
}

// runHTTP starts a plain HTTP server on 127.0.0.1 only. Used in --http dev mode
// where TLS is unnecessary (localhost traffic never leaves the machine). Logs a
// warning that this mode is for local development only. Binding to 127.0.0.1
// prevents accidental exposure on public interfaces.
func runHTTP(cfg Config, handler http.Handler) error {
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	return serveHTTP(srv, cfg.Port)
}

// serveHTTP starts an HTTP (non-TLS) server and blocks until shutdown signal.
func serveHTTP(srv *http.Server, port int) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		log.Printf("Received %s, shutting down...", sig)
	}

	if err := srv.Close(); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	log.Println("Server stopped")
	return nil
}

func serve(srv *http.Server, port int, autoTLS bool) error {
	errCh := make(chan error, 1)
	go func() {
		if autoTLS {
			log.Printf("Appx running with automatic TLS on https://localhost:%d", port)
		} else {
			log.Printf("Appx running on https://localhost:%d", port)
			log.Printf("To connect from another machine: https://<your-server-ip>:%d", port)
		}
		errCh <- srv.ListenAndServeTLS("", "")
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		log.Printf("Received %s, shutting down...", sig)
	}

	if err := srv.Close(); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	log.Println("Server stopped")
	return nil
}
