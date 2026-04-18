package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForHealthy_ImmediateSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.WaitForHealthy(ctx, 50*time.Millisecond); err != nil {
		t.Errorf("expected nil, got: %v", err)
	}
}

func TestWaitForHealthy_EventualSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.WaitForHealthy(ctx, 50*time.Millisecond); err != nil {
		t.Errorf("expected nil, got: %v", err)
	}
	if n := calls.Load(); n < 3 {
		t.Errorf("expected at least 3 calls, got %d", n)
	}
}

func TestWaitForHealthy_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if err := c.WaitForHealthy(ctx, 50*time.Millisecond); err == nil {
		t.Error("expected error for cancelled context, got nil")
	}
}

func TestInjectAPIKey_InjectsWhenHealthy(t *testing.T) {
	var gotKey string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/global/health":
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/auth/") && r.Method == http.MethodPut:
			var body struct {
				Key string `json:"key"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			gotKey = body.Key
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.InjectAPIKey(ctx, 50*time.Millisecond, "sk-ant-abc"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotKey != "sk-ant-abc" {
		t.Errorf("expected key 'sk-ant-abc', got %q", gotKey)
	}
}

func TestInjectAPIKey_SkipsWhenNoKey(t *testing.T) {
	authCalled := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/global/health":
			w.WriteHeader(http.StatusOK)
		case "/auth":
			authCalled = true
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.InjectAPIKey(ctx, 50*time.Millisecond, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authCalled {
		t.Error("expected /auth not to be called when apiKey is empty")
	}
}
