package egress

import (
	"testing"
	"time"
)

func TestPendingRegistry_AddAndList(t *testing.T) {
	r := NewPendingRegistry(nil)
	req, _ := r.Add("pypi.org", 443, "install rich")
	if req.Host != "pypi.org" || req.Port != 443 || req.Reason != "install rich" {
		t.Fatalf("unexpected request: %+v", req)
	}
	list := r.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(list))
	}
	if list[0].ID != req.ID {
		t.Errorf("expected ID %s, got %s", req.ID, list[0].ID)
	}
}

func TestPendingRegistry_ResolveApprove(t *testing.T) {
	r := NewPendingRegistry(nil)
	req, ch := r.Add("pypi.org", 443, "install rich")
	go func() {
		time.Sleep(10 * time.Millisecond)
		if err := r.Resolve(req.ID, true); err != nil {
			t.Errorf("resolve: %v", err)
		}
	}()
	result := <-ch
	if !result {
		t.Error("expected approved=true")
	}
	if len(r.List()) != 0 {
		t.Error("expected request removed after resolve")
	}
}

func TestPendingRegistry_ResolveDeny(t *testing.T) {
	r := NewPendingRegistry(nil)
	req, ch := r.Add("evil.com", 443, "exfiltrate data")
	go func() {
		time.Sleep(10 * time.Millisecond)
		if err := r.Resolve(req.ID, false); err != nil {
			t.Errorf("resolve: %v", err)
		}
	}()
	result := <-ch
	if result {
		t.Error("expected approved=false")
	}
}

func TestPendingRegistry_ResolveNotFound(t *testing.T) {
	r := NewPendingRegistry(nil)
	if err := r.Resolve("nonexistent", true); err == nil {
		t.Error("expected error for unknown ID")
	}
}

func TestPendingRegistry_CleanupExpired(t *testing.T) {
	r := NewPendingRegistry(nil)
	r.timeout = 10 * time.Millisecond
	_, ch := r.Add("pypi.org", 443, "test")
	time.Sleep(20 * time.Millisecond)
	r.cleanup()
	if len(r.List()) != 0 {
		t.Error("expected expired request to be cleaned up")
	}
	select {
	case v := <-ch:
		if v {
			t.Error("expected false for expired request")
		}
	default:
	}
}
