package server

import (
	"testing"
)

// TestHTTPAndDomainMutuallyExclusive verifies that setting both HTTPMode and
// Domain on the Config produces an error from Run().
func TestHTTPAndDomainMutuallyExclusive(t *testing.T) {
	cfg := Config{
		Port:     8080,
		HTTPMode: true,
		Domain:   "example.com",
	}
	err := Run(cfg)
	if err == nil {
		t.Fatal("expected error when both HTTPMode and Domain are set")
	}
	want := "--http and --domain are mutually exclusive"
	if err.Error() != want {
		t.Errorf("got error %q, want %q", err.Error(), want)
	}
}
