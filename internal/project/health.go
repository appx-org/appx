package project

import (
	"errors"
	"net"
	"strconv"
	"sync"
	"time"
)

// healthDialTimeout is the maximum time to wait for a TCP connection when
// probing a project's port.
const healthDialTimeout = 500 * time.Millisecond

// healthProbeTimeout bounds the write+read exchange that follows a successful
// connect. A listener that accepts our request and stays silent is treated as
// alive when this expires, so the timeout only needs to be long enough for a
// loopback round trip.
const healthProbeTimeout = 250 * time.Millisecond

// maxConcurrentDials limits the number of simultaneous probes during a health
// check sweep to avoid file descriptor exhaustion. A sweep now costs two probes
// per project (DEV + PROD), so this is high enough that a full 100-project
// allocation range finishes in a handful of waves.
const maxConcurrentDials = 64

// healthProbeRequest is the payload written to a port to find out whether a
// real server is behind it. appx only ever reverse-proxies HTTP to these ports
// (see the subdomain proxy in internal/server/router.go), so every app on them
// is an HTTP server by construction and a HEAD request is the cheapest
// well-formed thing to send. Connection: close keeps the exchange to one round
// trip.
var healthProbeRequest = []byte("HEAD / HTTP/1.0\r\nHost: localhost\r\nConnection: close\r\n\r\n")

// Health is the probe result for one project's two environments. They are
// reported separately because a project whose DEV server is the running one is
// otherwise indistinguishable from a project with nothing running at all.
type Health struct {
	// App reports a live listener on the project's PROD port (AssignedPort).
	App bool
	// Dev reports a live listener on the project's DEV port (DevPort).
	Dev bool
}

// HealthChecker probes whether agent-built apps are serving on their assigned
// ports. Stateless and safe for concurrent use.
type HealthChecker struct{}

// NewHealthChecker creates a HealthChecker ready for use.
func NewHealthChecker() *HealthChecker {
	return &HealthChecker{}
}

// Check probes each project's DEV and PROD ports concurrently and returns a map
// of project ID to Health. Ports that are zero are always reported as
// unhealthy. Concurrency is bounded by maxConcurrentDials.
func (hc *HealthChecker) Check(projects []*Project) map[string]Health {
	result := make(map[string]Health, len(projects))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentDials)

	// probe runs one port check and folds the verdict into the project's entry.
	probe := func(id string, port int, set func(*Health, bool)) {
		defer wg.Done()
		sem <- struct{}{}
		defer func() { <-sem }()

		up := portServing(port)

		mu.Lock()
		h := result[id]
		set(&h, up)
		result[id] = h
		mu.Unlock()
	}

	for _, p := range projects {
		// Seed the entry so a project with no ports still appears in the map.
		result[p.ID] = Health{}

		if p.AssignedPort > 0 {
			wg.Add(1)
			go probe(p.ID, p.AssignedPort, func(h *Health, up bool) { h.App = up })
		}
		if p.DevPort > 0 {
			wg.Add(1)
			go probe(p.ID, p.DevPort, func(h *Health, up bool) { h.Dev = up })
		}
	}

	wg.Wait()
	return result
}

// portServing reports whether a real server is listening on 127.0.0.1:port.
//
// A bare connect is not sufficient evidence in container mode: appx publishes
// the whole app port range (PortRangeStart-PublishedPortRangeEnd) from the outer
// container, so one docker-proxy process accepts connections on every port in
// the range whether or not anything is behind it. It completes the handshake and
// only resets once the client writes, which made a connect-and-close probe
// report every project as running.
//
// So we exchange bytes. Only a reset or an EOF *before any response* counts as
// down; a timeout counts as up, because a listener that accepted the request and
// has not answered yet is still a listener. That bias is deliberate — a
// transiently slow app should not flicker to "not running". The one case it gets
// wrong is a server that closes the connection without replying at all, which no
// HTTP server does in practice.
func portServing(port int) bool {
	addr := "127.0.0.1:" + strconv.Itoa(port)
	conn, err := net.DialTimeout("tcp", addr, healthDialTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(healthProbeTimeout))

	// docker-proxy with a failed upstream dial typically resets here.
	if _, err := conn.Write(healthProbeRequest); err != nil {
		return false
	}

	var buf [1]byte
	if _, err := conn.Read(buf[:]); err != nil {
		var nerr net.Error
		return errors.As(err, &nerr) && nerr.Timeout()
	}
	return true
}
