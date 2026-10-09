package main

import (
	"path/filepath"
	"testing"
)

// TestHostProjectRoot_ContainerModeHasNoHostPath pins the invariant behind
// issue #7: in container mode project files live in the agent's workspace
// volume, so appx must not derive a host path under $APPX_DATA (it would be
// created, reported to the UI, and stay permanently empty).
func TestHostProjectRoot_ContainerModeHasNoHostPath(t *testing.T) {
	if got := hostProjectRoot("/mnt/vol/appx-data", true); got != "" {
		t.Errorf("container mode: hostProjectRoot = %q, want \"\"", got)
	}
}

// TestHostProjectRoot_CoLocatedDerivesFromDataDir covers the co-located
// (host-mode agent-server) deployment used for local dev, where appx and the
// agent do share a filesystem and $APPX_DATA/projects is bind-mounted in.
func TestHostProjectRoot_CoLocatedDerivesFromDataDir(t *testing.T) {
	want := filepath.Join("/mnt/vol/appx-data", "projects")
	if got := hostProjectRoot("/mnt/vol/appx-data", false); got != want {
		t.Errorf("host mode: hostProjectRoot = %q, want %q", got, want)
	}
}
