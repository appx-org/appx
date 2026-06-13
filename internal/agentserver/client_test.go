package agentserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neuromaxer/appx/internal/project"
)

// captureServer records the last request body + bearer token sent to the
// agent-server create-project endpoint.
type captured struct {
	name       string
	deployment map[string]map[string]any
	hasDeploy  bool
	authz      string
}

func newCaptureServer(t *testing.T, sink *captured) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Name       string                    `json:"name"`
			Deployment map[string]map[string]any `json:"deployment"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("unmarshal request body %q: %v", body, err)
		}
		sink.name = payload.Name
		sink.deployment = payload.Deployment
		_, sink.hasDeploy = mapHasKey(body, "deployment")
		sink.authz = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id":"x","name":"x","projectDir":"/x","createdAt":"t"}`))
	}))
}

// mapHasKey reports whether the raw JSON object contains the given top-level key.
func mapHasKey(raw []byte, key string) (any, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, false
	}
	v, ok := m[key]
	return v, ok
}

func TestEnsureProject_SendsNestedDeployment(t *testing.T) {
	var sink captured
	srv := newCaptureServer(t, &sink)
	defer srv.Close()

	client := NewClient(srv.URL, "secret-token")
	dep := project.Deployment{
		Dev:  project.EnvTarget{Port: 10006, URL: "https://eventx-dev.example.com"},
		Prod: project.EnvTarget{Port: 10007, URL: "https://eventx.example.com"},
	}
	if err := client.EnsureProject(context.Background(), "eventx", dep); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}

	if sink.name != "eventx" {
		t.Errorf("name = %q, want eventx", sink.name)
	}
	if sink.authz != "Bearer secret-token" {
		t.Errorf("authorization = %q", sink.authz)
	}
	if got := sink.deployment["dev"]["port"]; got != float64(10006) {
		t.Errorf("dev.port = %v, want 10006", got)
	}
	if got := sink.deployment["dev"]["url"]; got != "https://eventx-dev.example.com" {
		t.Errorf("dev.url = %v", got)
	}
	if got := sink.deployment["prod"]["port"]; got != float64(10007) {
		t.Errorf("prod.port = %v, want 10007", got)
	}
	if got := sink.deployment["prod"]["url"]; got != "https://eventx.example.com" {
		t.Errorf("prod.url = %v", got)
	}
}

func TestEnsureProject_OmitsEmptyDeployment(t *testing.T) {
	var sink captured
	srv := newCaptureServer(t, &sink)
	defer srv.Close()

	client := NewClient(srv.URL, "")
	if err := client.EnsureProject(context.Background(), "plain", project.Deployment{}); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	if sink.hasDeploy {
		t.Error("expected no deployment key for an empty Deployment")
	}
	if sink.name != "plain" {
		t.Errorf("name = %q, want plain", sink.name)
	}
}

func TestEnsureProject_OmitsEmptyEnvironment(t *testing.T) {
	var sink captured
	srv := newCaptureServer(t, &sink)
	defer srv.Close()

	client := NewClient(srv.URL, "")
	// Only PROD set; DEV must be omitted entirely.
	dep := project.Deployment{Prod: project.EnvTarget{Port: 10007, URL: "https://eventx.example.com"}}
	if err := client.EnsureProject(context.Background(), "eventx", dep); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	if _, ok := sink.deployment["dev"]; ok {
		t.Error("expected dev environment omitted")
	}
	if _, ok := sink.deployment["prod"]; !ok {
		t.Error("expected prod environment present")
	}
}
