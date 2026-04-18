package project

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestHealthChecker_PortListening(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	hc := NewHealthChecker()
	projects := []*Project{{ID: "p1", Name: "myapp", AssignedPort: port}}
	result := hc.Check(projects)
	if !result["p1"] {
		t.Errorf("expected p1 healthy, got false")
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
	projects := []*Project{{ID: "p2", Name: "deadapp", AssignedPort: port}}
	result := hc.Check(projects)
	if result["p2"] {
		t.Errorf("expected p2 unhealthy, got true")
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	listenPort := ln.Addr().(*net.TCPAddr).Port

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
	if !result["alive"] {
		t.Errorf("expected 'alive' healthy")
	}
	if result["dead"] {
		t.Errorf("expected 'dead' unhealthy")
	}
}

func TestHealthChecker_ManyProjectsConcurrentCorrectness(t *testing.T) {
	// Mix of live and dead ports — verifies correctness under concurrent execution.
	// After parallelising Check(), this test guards against race conditions.
	var listeners []net.Listener
	defer func() {
		for _, ln := range listeners {
			ln.Close()
		}
	}()

	projects := make([]*Project, 30)
	for i := range projects {
		if i%3 == 0 {
			// Every 3rd project has a live listener.
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			listeners = append(listeners, ln)
			projects[i] = &Project{ID: fmt.Sprintf("p%d", i), Name: fmt.Sprintf("app%d", i), AssignedPort: ln.Addr().(*net.TCPAddr).Port}
		} else {
			// Dead port.
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := ln.Addr().(*net.TCPAddr).Port
			ln.Close()
			projects[i] = &Project{ID: fmt.Sprintf("p%d", i), Name: fmt.Sprintf("app%d", i), AssignedPort: port}
		}
	}

	hc := NewHealthChecker()
	start := time.Now()
	result := hc.Check(projects)
	elapsed := time.Since(start)

	for i, p := range projects {
		if i%3 == 0 {
			if !result[p.ID] {
				t.Errorf("expected %s (live) healthy, got false", p.ID)
			}
		} else {
			if result[p.ID] {
				t.Errorf("expected %s (dead) unhealthy, got true", p.ID)
			}
		}
	}

	// Sanity: should complete quickly even with many projects.
	if elapsed > 5*time.Second {
		t.Errorf("health checks took %v — too slow for 30 projects", elapsed)
	}
}

func TestHealthChecker_ZeroPort(t *testing.T) {
	hc := NewHealthChecker()
	projects := []*Project{{ID: "noport", Name: "noport", AssignedPort: 0}}
	result := hc.Check(projects)
	if result["noport"] {
		t.Errorf("expected port 0 unhealthy")
	}
}
