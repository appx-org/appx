package terminal

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Manual verification of the container-exec project terminal against a real
// container. The production agent-server image is amd64-only, so this runs
// against any image with a workspace directory — what it exercises is appx's
// side: docker exec + PTY attach + resize. Skipped unless the env var is set.
//
//	docker run -d --name appx-shell-test debian:bookworm-slim \
//	  sh -c 'mkdir -p /workspace/myapp && echo hi > /workspace/myapp/README.md && sleep 600'
//	APPX_SHELL_TEST_CONTAINER=appx-shell-test \
//	  go test ./internal/terminal -run ContainerExec_Manual -v
func TestContainerExec_Manual(t *testing.T) {
	name := os.Getenv("APPX_SHELL_TEST_CONTAINER")
	if name == "" {
		t.Skip("set APPX_SHELL_TEST_CONTAINER to a running container (see comment)")
	}

	cs := ContainerShell{Bin: "docker", Container: name, WorkspaceDir: "/workspace"}
	spec, err := cs.CommandFor("myapp")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("exec: %s %s", spec.Name, strings.Join(spec.Args, " "))

	m := NewLocalManager(65536)
	sess, err := m.CreateCommand(spec)
	if err != nil {
		t.Fatalf("exec into container: %v", err)
	}
	defer m.Close(sess.ID)

	out := m.Subscribe(sess.ID)
	if out == nil {
		t.Fatal("subscribe returned nil")
	}
	defer m.Unsubscribe(sess.ID, out)

	// Resize must reach the shell inside the container: the docker CLI watches
	// its own stdin TTY (our PTY) and forwards the new size over the exec API.
	if err := m.Resize(sess.ID, 100, 42); err != nil {
		t.Fatalf("resize: %v", err)
	}

	// Ask the in-container shell where it is, what it can see, and its tty size.
	// The quotes in the marker are stripped by the shell, so the sentinel we
	// wait for appears only in the command's *output* — the PTY echoes our
	// input back verbatim, which would otherwise end the loop immediately.
	if err := m.Write(sess.ID, []byte("pwd; ls; tput cols; echo DONE''MARKER\n")); err != nil {
		t.Fatalf("write to shell: %v", err)
	}

	var buf strings.Builder
	deadline := time.After(20 * time.Second)
	for !strings.Contains(buf.String(), "DONEMARKER") {
		select {
		case chunk, ok := <-out:
			if !ok {
				t.Fatalf("shell exited early; output so far:\n%s", buf.String())
			}
			buf.Write(chunk)
		case <-deadline:
			t.Fatalf("timed out waiting for shell output; got:\n%s", buf.String())
		}
	}
	got := buf.String()
	t.Logf("shell output:\n%s", got)

	// The shell must start in the project directory inside the container...
	if !strings.Contains(got, "/workspace/myapp") {
		t.Errorf("expected the shell to start in /workspace/myapp, got:\n%s", got)
	}
	// ...and actually see the project's files.
	if !strings.Contains(got, "README.md") {
		t.Errorf("expected to see the project's files, got:\n%s", got)
	}
	// ...with the terminal size we pushed.
	if !strings.Contains(got, "100") {
		t.Errorf("expected tput cols to report the resized width 100, got:\n%s", got)
	}
}
