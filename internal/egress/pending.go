package egress

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"
)

// PendingRequest represents an agent's request to access a blocked egress host.
// Created when the agent calls the internal listener, and resolved when the user
// approves or denies via the dashboard.
type PendingRequest struct {
	ID        string    `json:"id"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
	done      chan bool
}

// PendingRegistry holds in-memory egress permission requests. Requests live at
// most 60 seconds. When resolved, the registry optionally adds the host to the
// egress allowlist via the Store.
type PendingRegistry struct {
	mu       sync.Mutex
	requests map[string]*PendingRequest
	store    *Store
	timeout  time.Duration
}

// NewPendingRegistry creates a registry. The store is used to add approved hosts
// to the persistent allowlist. Pass nil in tests where allowlist updates aren't needed.
func NewPendingRegistry(store *Store) *PendingRegistry {
	return &PendingRegistry{
		requests: make(map[string]*PendingRequest),
		store:    store,
		timeout:  60 * time.Second,
	}
}

// Add creates a pending egress request and returns it along with a channel that
// receives true (approved) or false (denied/expired). The caller should block on
// the channel.
func (r *PendingRegistry) Add(host string, port int, reason string) (PendingRequest, <-chan bool) {
	id := randomID()
	ch := make(chan bool, 1)
	req := &PendingRequest{
		ID:        id,
		Host:      host,
		Port:      port,
		Reason:    reason,
		CreatedAt: time.Now(),
		done:      ch,
	}
	r.mu.Lock()
	r.requests[id] = req
	r.mu.Unlock()
	return *req, ch
}

// List returns all pending requests, cleaning up expired ones first.
func (r *PendingRegistry) List() []PendingRequest {
	r.cleanup()
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]PendingRequest, 0, len(r.requests))
	for _, req := range r.requests {
		result = append(result, *req)
	}
	return result
}

// Resolve approves or denies a pending request. If approved and a store is
// configured, the host:port is added to the persistent egress allowlist.
func (r *PendingRegistry) Resolve(id string, approved bool) error {
	r.mu.Lock()
	req, ok := r.requests[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("pending request %s not found", id)
	}
	delete(r.requests, id)
	r.mu.Unlock()

	if approved && r.store != nil {
		r.store.AddToAllowlist(req.Host, req.Port)
	}

	req.done <- approved
	close(req.done)
	return nil
}

// cleanup removes requests that have exceeded the timeout, sending false on
// their channels.
func (r *PendingRegistry) cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().Add(-r.timeout)
	for id, req := range r.requests {
		if req.CreatedAt.Before(cutoff) {
			select {
			case req.done <- false:
			default:
			}
			close(req.done)
			delete(r.requests, id)
		}
	}
}

// randomID generates a short random hex ID for pending requests.
func randomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}
