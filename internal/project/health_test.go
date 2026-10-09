package project

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// acceptThenClose mimics the `docker-proxy` behaviour behind issue #8: the
// outer container publishes the whole app port range, so one proxy process
// accepts on every port in the range and only drops the connection once it
// fails to reach anything upstream. reset selects RST (SetLinger(0)) over a
// graceful FIN; both must read as "nothing is serving here".
func acceptThenClose(t *testing.T, reset bool) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if reset {
				if tc, ok := conn.(*net.TCPConn); ok {
					tc.SetLinger(0)
				}
			}
			conn.Close()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestHealthChecker_PublishedPortWithNothingBehindIt is the regression test for
// issue #8: a connect-only probe reported every project as running because
// docker-proxy completes the handshake on every published port.
func TestHealthChecker_PublishedPortWithNothingBehindIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reset bool
	}{
		{"graceful close", false},
		{"connection reset", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			port := acceptThenClose(t, tc.reset)

			hc := NewHealthChecker()
			result := hc.Check([]*Project{{ID: "p1", Name: "myapp", AssignedPort: port}})
			if result["p1"].App {
				t.Error("expected App=false: the port accepts but nothing serves on it")
			}
		})
	}
}

// TestHealthChecker_RespondingServer covers the positive case — a real HTTP
// server of the kind the subdomain proxy forwards to.
func TestHealthChecker_RespondingServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	hc := NewHealthChecker()
	result := hc.Check([]*Project{{ID: "p1", Name: "myapp", AssignedPort: port}})
	if !result["p1"].App {
		t.Error("expected App=true for a responding HTTP server")
	}
}

// TestHealthChecker_SilentListenerIsUp pins the deliberate bias: a listener that
// accepts the probe but has not answered yet is reported up, so a transiently
// slow app does not flicker to "not running".
func TestHealthChecker_SilentListenerIsUp(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	hc := NewHealthChecker()
	result := hc.Check([]*Project{{ID: "p1", Name: "myapp", AssignedPort: port}})
	if !result["p1"].App {
		t.Error("expected App=true for a listener that accepts but stays silent")
	}
}

func TestHealthChecker_PortNotListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	hc := NewHealthChecker()
	result := hc.Check([]*Project{{ID: "p2", Name: "deadapp", AssignedPort: port}})
	if result["p2"].App {
		t.Error("expected App=false when the dial itself fails")
	}
}

// TestHealthChecker_DevPortProbedSeparately covers the second half of issue #8:
// only AssignedPort was probed, so a project whose DEV server was the running
// one looked identical to a project running nothing.
func TestHealthChecker_DevPortProbedSeparately(t *testing.T) {
	devSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer devSrv.Close()
	devPort := devSrv.Listener.Addr().(*net.TCPAddr).Port

	// PROD port accepts but serves nothing (the docker-proxy shape).
	prodPort := acceptThenClose(t, true)

	hc := NewHealthChecker()
	result := hc.Check([]*Project{
		{ID: "p1", Name: "devonly", AssignedPort: prodPort, DevPort: devPort},
	})
	if result["p1"].App {
		t.Error("expected App=false — nothing serves on the PROD port")
	}
	if !result["p1"].Dev {
		t.Error("expected Dev=true — the dev server is running")
	}
}

func TestHealthChecker_EmptyList(t *testing.T) {
	hc := NewHealthChecker()
	result := hc.Check([]*Project{})
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

func TestHealthChecker_MultipleProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	listenPort := srv.Listener.Addr().(*net.TCPAddr).Port

	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()

	hc := NewHealthChecker()
	projects := []*Project{
		{ID: "alive", Name: "alive", AssignedPort: listenPort},
		{ID: "dead", Name: "dead", AssignedPort: closedPort},
	}
	result := hc.Check(projects)
	if !result["alive"].App {
		t.Error("expected 'alive' healthy")
	}
	if result["dead"].App {
		t.Error("expected 'dead' unhealthy")
	}
}

func TestHealthChecker_ManyProjectsConcurrentCorrectness(t *testing.T) {
	// Mix of serving and published-but-empty ports across both environments —
	// verifies correctness under concurrent execution.
	var servers []*httptest.Server
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()

	projects := make([]*Project, 30)
	for i := range projects {
		id := fmt.Sprintf("p%d", i)
		if i%3 == 0 {
			// Every 3rd project serves on both ports.
			prod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			dev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
			servers = append(servers, prod, dev)
			projects[i] = &Project{
				ID:           id,
				Name:         id,
				AssignedPort: prod.Listener.Addr().(*net.TCPAddr).Port,
				DevPort:      dev.Listener.Addr().(*net.TCPAddr).Port,
			}
		} else {
			// Published but empty, on both ports.
			projects[i] = &Project{
				ID:           id,
				Name:         id,
				AssignedPort: acceptThenClose(t, true),
				DevPort:      acceptThenClose(t, true),
			}
		}
	}

	hc := NewHealthChecker()
	start := time.Now()
	result := hc.Check(projects)
	elapsed := time.Since(start)

	for i, p := range projects {
		want := i%3 == 0
		if got := result[p.ID].App; got != want {
			t.Errorf("%s: App = %v, want %v", p.ID, got, want)
		}
		if got := result[p.ID].Dev; got != want {
			t.Errorf("%s: Dev = %v, want %v", p.ID, got, want)
		}
	}

	// Sanity: the sweep runs on every project list request, so it must stay
	// cheap even when every port has to be probed.
	if elapsed > 5*time.Second {
		t.Errorf("health checks took %v — too slow for 30 projects", elapsed)
	}
}

func TestHealthChecker_ZeroPort(t *testing.T) {
	hc := NewHealthChecker()
	result := hc.Check([]*Project{{ID: "noport", Name: "noport", AssignedPort: 0}})
	if result["noport"].App || result["noport"].Dev {
		t.Error("expected port 0 unhealthy")
	}
	if _, ok := result["noport"]; !ok {
		t.Error("expected a portless project to still appear in the result map")
	}
}
