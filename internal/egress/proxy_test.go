package egress

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupProxyTest creates a Store and starts a Proxy on a random port.
// Returns the proxy address and the store for assertions.
func setupProxyTest(t *testing.T) (string, *Store) {
	t.Helper()
	return setupProxyTestOpts(t, false)
}

// setupProxyTestAllowInternal is like setupProxyTest but disables the
// post-dial internal-IP check. Used by tests that connect to a localhost echo
// server and need the tunnel to succeed.
func setupProxyTestAllowInternal(t *testing.T) (string, *Store) {
	t.Helper()
	return setupProxyTestOpts(t, true)
}

// setupProxyTestOpts creates a Store and starts a Proxy on a random port.
// When allowInternal is true, the proxy skips the resolved-IP check.
func setupProxyTestOpts(t *testing.T, allowInternal bool) (string, *Store) {
	t.Helper()
	db := setupTestDB(t)
	store := NewStore(db)
	p := NewProxy(store)
	p.allowInternal = allowInternal

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go p.Serve(ln)

	return ln.Addr().String(), store
}

// startEchoServer starts a TCP server that echoes back one message.
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		n, _ := conn.Read(buf)
		conn.Write(buf[:n])
	}()

	return ln.Addr().String()
}

func TestProxy_AllowedConnect(t *testing.T) {
	proxyAddr, store := setupProxyTestAllowInternal(t)

	echoAddr := startEchoServer(t)
	host, port, _ := net.SplitHostPort(echoAddr)
	store.SetAllowlist([]string{fmt.Sprintf("%s:%s", host, port)})

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr)

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	fmt.Fprintf(conn, "hello")
	buf := make([]byte, 5)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := io.ReadFull(conn, buf)
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf[:n]) != "hello" {
		t.Errorf("expected 'hello', got %q", string(buf[:n]))
	}
}

func TestProxy_BlockedConnect(t *testing.T) {
	proxyAddr, _ := setupProxyTest(t)

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT evil.com:443 HTTP/1.1\r\nHost: evil.com:443\r\n\r\n")

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
}

func TestProxy_LogsEntries(t *testing.T) {
	proxyAddr, store := setupProxyTest(t)

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "CONNECT blocked.com:443 HTTP/1.1\r\nHost: blocked.com:443\r\n\r\n")
	http.ReadResponse(bufio.NewReader(conn), nil)
	conn.Close()

	entries, total, err := store.ListLog(50, 0)
	if err != nil {
		t.Fatalf("ListLog: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected 1 log entry, got %d", total)
	}
	if entries[0].Destination != "blocked.com" {
		t.Errorf("expected blocked.com, got %s", entries[0].Destination)
	}
	if entries[0].Allowed {
		t.Error("expected entry to be blocked")
	}
}

// TestProxy_BlocksInternalIP verifies that even when a destination is in the
// allowlist, the proxy blocks the tunnel if the resolved IP is loopback or
// private (DNS rebinding protection).
func TestProxy_BlocksInternalIP(t *testing.T) {
	proxyAddr, store := setupProxyTest(t)

	echoAddr := startEchoServer(t)
	host, port, _ := net.SplitHostPort(echoAddr)
	store.SetAllowlist([]string{fmt.Sprintf("%s:%s", host, port)})

	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr)

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 (internal IP blocked), got %d", resp.StatusCode)
	}
}

func TestProxy_NonConnectMethod(t *testing.T) {
	proxyAddr, _ := setupProxyTest(t)

	resp, err := http.Get("http://" + proxyAddr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}
