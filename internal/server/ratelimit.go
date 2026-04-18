package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// rateLimiter tracks request timestamps per IP address within a sliding window
// and rejects requests that exceed the configured maximum. Used to protect the
// login endpoint from brute-force attacks.
type rateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	window   time.Duration
	max      int
}

// newRateLimiter creates a rate limiter that allows at most max requests per IP
// within the given sliding time window. It starts a background goroutine that
// periodically removes entries for IPs that have had no activity within the
// window, preventing unbounded memory growth from abandoned IPs.
func newRateLimiter(window time.Duration, max int) *rateLimiter {
	rl := &rateLimiter{
		attempts: make(map[string][]time.Time),
		window:   window,
		max:      max,
	}
	go rl.cleanupLoop()
	return rl
}

// cleanupLoop runs for the lifetime of the server, purging stale IP entries
// once per window duration.
func (rl *rateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.window)
	defer ticker.Stop()
	for range ticker.C {
		rl.cleanup()
	}
}

// cleanup removes entries for IPs that have no attempts within the current
// window. Called periodically by cleanupLoop.
func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-rl.window)
	for ip, times := range rl.attempts {
		valid := times[:0]
		for _, t := range times {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(rl.attempts, ip)
		} else {
			rl.attempts[ip] = valid
		}
	}
}

// allow returns true if the given IP has not exceeded the rate limit. It prunes
// expired timestamps and records the current request if allowed.
func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Remove expired entries
	valid := rl.attempts[ip][:0]
	for _, t := range rl.attempts[ip] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.max {
		rl.attempts[ip] = valid
		return false
	}

	rl.attempts[ip] = append(valid, now)
	return true
}

// trustedProxyCIDRs is the set of CIDRs from which X-Forwarded-For /
// X-Real-IP headers are trusted. These cover loopback and private ranges,
// which covers the typical case of appx running behind a local reverse proxy
// (nginx, Caddy) on the same host or LAN.
var trustedProxyCIDRs = func() []*net.IPNet {
	cidrs := []string{
		"127.0.0.0/8",
		"::1/128",
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"fe80::/10",
		"fc00::/7",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil {
			nets = append(nets, network)
		}
	}
	return nets
}()

// isTrustedProxy reports whether ip belongs to one of the trusted proxy CIDRs.
func isTrustedProxy(ip string) bool {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	for _, network := range trustedProxyCIDRs {
		if network.Contains(parsed) {
			return true
		}
	}
	return false
}

// parseIP normalises, validates and returns a canonical IP string.
// It trims whitespace, strips any port, takes the first element of a
// comma-separated X-Forwarded-For list, and returns "" for invalid input.
func parseIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if commaIdx := strings.IndexByte(raw, ','); commaIdx >= 0 {
		raw = strings.TrimSpace(raw[:commaIdx])
	}
	// Strip port if present (e.g. from RemoteAddr "1.2.3.4:5678").
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	if net.ParseIP(raw) == nil {
		return ""
	}
	return raw
}

// getClientIP extracts the true client IP from the request. Proxy headers are
// only trusted when the immediate peer (RemoteAddr) is within a known private
// or loopback range, preventing clients from spoofing their IP via headers
// when appx is exposed directly without a reverse proxy.
func getClientIP(r *http.Request) string {
	remoteIP := parseIP(r.RemoteAddr)

	if isTrustedProxy(remoteIP) {
		if ip := parseIP(r.Header.Get("X-Forwarded-For")); ip != "" {
			return ip
		}
		if ip := parseIP(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}

	if remoteIP != "" {
		return remoteIP
	}
	return r.RemoteAddr
}

func (rl *rateLimiter) middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if !rl.allow(ip) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}
