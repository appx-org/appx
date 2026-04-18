package opencode

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthCheck_Healthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/global/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.HealthCheck(); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

func TestHealthCheck_Unhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.HealthCheck(); err == nil {
		t.Error("expected error for 503, got nil")
	}
}

func TestHealthCheck_ConnectionRefused(t *testing.T) {
	c := NewClient("http://127.0.0.1:1")
	if err := c.HealthCheck(); err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}

func TestListProjects_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/project" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]OpenCodeProject{
			{ID: "proj-abc", Name: "myapp", AbsolutePath: "/home/opencode/projects/myapp"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	projects, err := c.ListProjects()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	if projects[0].ID != "proj-abc" {
		t.Errorf("expected ID 'proj-abc', got %q", projects[0].ID)
	}
}

func TestListProjects_EmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	projects, err := c.ListProjects()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected 0, got %d", len(projects))
	}
}

func TestListProjects_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.ListProjects()
	if err == nil {
		t.Error("expected error for 500, got nil")
	}
}

func TestSetAuth_Success(t *testing.T) {
	var gotProvider, gotKey, gotType string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Correct endpoint: PUT /auth/:providerID
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		// Provider ID is in the path, not the body
		gotProvider = strings.TrimPrefix(r.URL.Path, "/auth/")
		var body struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		gotType = body.Type
		gotKey = body.Key
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	err := c.SetAuth("anthropic", "sk-ant-test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotProvider != "anthropic" {
		t.Errorf("expected provider 'anthropic' in path, got %q", gotProvider)
	}
	if gotType != "api" {
		t.Errorf("expected type 'api', got %q", gotType)
	}
	if gotKey != "sk-ant-test-key" {
		t.Errorf("expected key 'sk-ant-test-key', got %q", gotKey)
	}
}

func TestSetAuth_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.SetAuth("bad", "key"); err == nil {
		t.Error("expected error for 400, got nil")
	}
}

func TestNewClient_BaseURL(t *testing.T) {
	c := NewClient("http://localhost:4096/")
	if c.baseURL != "http://localhost:4096" {
		t.Errorf("expected trimmed URL, got %q", c.baseURL)
	}
}
