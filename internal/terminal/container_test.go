package terminal

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestContainerShell_CommandFor(t *testing.T) {
	cs := ContainerShell{Bin: "docker", Container: "builder-outer", WorkspaceDir: "/workspace"}

	spec, err := cs.CommandFor("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "docker" {
		t.Errorf("Name = %q, want docker", spec.Name)
	}

	// The working directory must be the project's dir *inside* the container.
	i := slices.Index(spec.Args, "-w")
	if i < 0 || i+1 >= len(spec.Args) {
		t.Fatalf("no -w flag in %v", spec.Args)
	}
	if got := spec.Args[i+1]; got != "/workspace/myapp" {
		t.Errorf("workdir = %q, want /workspace/myapp", got)
	}

	// Interactive TTY is required or the shell gets no job control.
	for _, want := range []string{"exec", "-i", "-t", "builder-outer", DefaultContainerShell} {
		if !slices.Contains(spec.Args, want) {
			t.Errorf("missing %q in args %v", want, spec.Args)
		}
	}
	if !slices.Contains(spec.Args, "TERM=xterm-256color") {
		t.Errorf("TERM not set for the exec'd process: %v", spec.Args)
	}

	// Dir must stay empty: docker runs on the host, the workdir is in-container.
	if spec.Dir != "" {
		t.Errorf("Dir = %q, want empty (the -w flag carries the workdir)", spec.Dir)
	}
}

func TestContainerShell_DefaultsWorkspaceAndShell(t *testing.T) {
	cs := ContainerShell{Bin: "podman", Container: "c"}
	spec, err := cs.CommandFor("app")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spec.Args, "/workspace/app") {
		t.Errorf("expected default workspace root, got %v", spec.Args)
	}
	if !slices.Contains(spec.Args, DefaultContainerShell) {
		t.Errorf("expected default shell, got %v", spec.Args)
	}
}

func TestContainerShell_ShellOverride(t *testing.T) {
	cs := ContainerShell{Bin: "docker", Container: "c", Shell: "/bin/zsh"}
	spec, err := cs.CommandFor("app")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(spec.Args, "/bin/zsh") {
		t.Errorf("expected overridden shell, got %v", spec.Args)
	}
}

// TestContainerShell_RejectsPathEscapes guards the one place a project name
// becomes a filesystem path. Names are validated on create, so these inputs
// should be unreachable — the check is defence in depth against a future
// grammar change letting a name climb out of the workspace root.
func TestContainerShell_RejectsPathEscapes(t *testing.T) {
	cs := ContainerShell{Bin: "docker", Container: "c", WorkspaceDir: "/workspace"}

	for _, name := range []string{"", ".", "..", "../etc", "a/b", `a\b`, "A", "app;rm -rf /", "app$(id)"} {
		if spec, err := cs.CommandFor(name); err == nil {
			t.Errorf("CommandFor(%q) succeeded with args %v, want error", name, spec.Args)
		}
	}
}

func TestContainerShell_RequiresBinAndContainer(t *testing.T) {
	if _, err := (ContainerShell{Container: "c"}).CommandFor("app"); err == nil {
		t.Error("expected an error with no container CLI configured")
	}
	if _, err := (ContainerShell{Bin: "docker"}).CommandFor("app"); err == nil {
		t.Error("expected an error with no container name configured")
	}
}

// TestCreateCommand_RunsInDir verifies the generic PTY path used by project
// terminals: an arbitrary command, attached to a PTY, in a chosen directory.
func TestCreateCommand_RunsInDir(t *testing.T) {
	dir := t.TempDir()
	m := NewLocalManager(65536)

	// Write pwd to a file rather than reading the PTY: the session is
	// deregistered as soon as the process exits, and subscribing first would
	// race the output pump.
	sess, err := m.CreateCommand(CommandSpec{
		Name: "/bin/sh",
		Args: []string{"-c", "pwd > pwd.txt"},
		Dir:  dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(sess.ID)

	<-m.Done(sess.ID)

	out, err := os.ReadFile(filepath.Join(dir, "pwd.txt"))
	if err != nil {
		t.Fatalf("command did not run in %s: %v", dir, err)
	}
	// macOS resolves TMPDIR through a /private symlink, so compare on the suffix.
	got := strings.TrimSpace(string(out))
	if !strings.HasSuffix(got, strings.TrimPrefix(dir, "/private")) {
		t.Errorf("pwd = %q, want it to end with %q", got, dir)
	}
}

func TestCreateCommand_RejectsEmptyName(t *testing.T) {
	m := NewLocalManager(1024)
	if _, err := m.CreateCommand(CommandSpec{}); err == nil {
		t.Error("expected an error for a spec with no executable")
	}
}
