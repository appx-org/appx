package project

import (
	"net"
	"strconv"
	"sync"
	"time"
)

// healthDialTimeout is the maximum time to wait for a TCP connection when
// probing a project's assigned port.
const healthDialTimeout = 500 * time.Millisecond

// maxConcurrentDials limits the number of simultaneous TCP dials during a
// health check sweep to avoid file descriptor exhaustion.
const maxConcurrentDials = 20

// HealthChecker probes whether agent-built apps are listening on their
// assigned ports via TCP dial on 127.0.0.1. Stateless and safe for concurrent use.
type HealthChecker struct{}

// NewHealthChecker creates a HealthChecker ready for use.
func NewHealthChecker() *HealthChecker {
	return &HealthChecker{}
}

// Check probes each project's assigned port concurrently and returns a map of
// project ID to reachability. Projects with port 0 are always reported as
// unhealthy. Concurrency is bounded by maxConcurrentDials to avoid file
// descriptor exhaustion.
func (hc *HealthChecker) Check(projects []*Project) map[string]bool {
	result := make(map[string]bool, len(projects))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentDials)

	for _, p := range projects {
		if p.AssignedPort <= 0 {
			result[p.ID] = false
			continue
		}
		wg.Add(1)
		go func(p *Project) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			addr := "127.0.0.1:" + strconv.Itoa(p.AssignedPort)
			conn, err := net.DialTimeout("tcp", addr, healthDialTimeout)

			mu.Lock()
			if err != nil {
				result[p.ID] = false
			} else {
				conn.Close()
				result[p.ID] = true
			}
			mu.Unlock()
		}(p)
	}

	wg.Wait()
	return result
}
