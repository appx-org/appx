package egress

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"
)

// ProxyAddr is the default listen address for the egress CONNECT proxy.
const ProxyAddr = "127.0.0.1:9080"

// ProxyPort is the egress CONNECT proxy's TCP port. Used to build a bind
// address on a non-loopback host (container mode binds it on the bridge
// gateway so the in-container agent can reach it via host.docker.internal).
const ProxyPort = "9080"

const tunnelTimeout = 30 * time.Minute
const dialTimeout = 10 * time.Second

// Proxy is an HTTP CONNECT proxy that enforces an allowlist on outbound
// connections and logs every attempt. After connecting, it verifies the resolved
// IP is not internal (loopback, private, link-local) to prevent DNS rebinding.
type Proxy struct {
	store *Store

	// allowInternal disables the post-dial internal-IP check. Only set to true
	// in tests where the echo server necessarily binds to 127.0.0.1.
	allowInternal bool
}

// NewProxy creates a CONNECT proxy backed by the given egress store.
func NewProxy(store *Store) *Proxy {
	return &Proxy{store: store}
}

// ListenAndServe starts the CONNECT proxy on the given address.
func (p *Proxy) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("egress proxy listen: %w", err)
	}
	log.Printf("Egress CONNECT proxy listening on %s", addr)
	return p.Serve(ln)
}

// Serve accepts connections on the given listener. Factored out of
// ListenAndServe for testability.
func (p *Proxy) Serve(ln net.Listener) error {
	srv := &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	return srv.Serve(ln)
}

// ServeHTTP implements http.Handler. Only CONNECT requests are accepted.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT is supported", http.StatusMethodNotAllowed)
		return
	}

	host, portStr, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "invalid host:port", http.StatusBadRequest)
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		http.Error(w, "invalid port", http.StatusBadRequest)
		return
	}

	allowed := p.store.IsAllowed(host, port)

	if err := p.store.LogEntry(host, port, allowed); err != nil {
		log.Printf("egress: failed to log %s:%d: %v", host, port, err)
	}

	if !allowed {
		log.Printf("egress: BLOCKED %s:%d", host, port)
		http.Error(w, "destination not in allowlist", http.StatusForbidden)
		return
	}

	log.Printf("egress: ALLOWED %s:%d", host, port)

	destConn, err := net.DialTimeout("tcp", r.Host, dialTimeout)
	if err != nil {
		http.Error(w, "failed to connect to destination", http.StatusBadGateway)
		return
	}

	// Reject connections that resolved to loopback or private addresses.
	// The allowlist validates hostnames, but DNS rebinding can make an
	// allowed hostname resolve to an internal IP at connection time.
	if !p.allowInternal {
		if addr, ok := destConn.RemoteAddr().(*net.TCPAddr); ok {
			if addr.IP.IsLoopback() || addr.IP.IsPrivate() || addr.IP.IsLinkLocalUnicast() {
				destConn.Close()
				log.Printf("egress: BLOCKED %s:%d (resolved to internal address %s)", host, port, addr.IP)
				http.Error(w, "destination resolved to internal address", http.StatusForbidden)
				return
			}
		}
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		destConn.Close()
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hj.Hijack()
	if err != nil {
		destConn.Close()
		return
	}

	fmt.Fprintf(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n")

	deadline := time.Now().Add(tunnelTimeout)
	clientConn.SetDeadline(deadline)
	destConn.SetDeadline(deadline)

	go func() {
		io.Copy(destConn, clientConn)
		destConn.Close()
	}()
	go func() {
		io.Copy(clientConn, destConn)
		clientConn.Close()
	}()
}
