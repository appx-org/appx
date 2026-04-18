package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrate_FreshDB(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		t.Fatalf("migrate on fresh DB: %v", err)
	}

	// Verify tables exist by inserting into each
	_, err = db.Exec("INSERT INTO settings (key, value) VALUES ('test', 'val')")
	if err != nil {
		t.Errorf("settings table missing: %v", err)
	}

	_, err = db.Exec("INSERT INTO projects (id, name) VALUES ('p1', 'test-project')")
	if err != nil {
		t.Errorf("projects table missing: %v", err)
	}

	_, err = db.Exec("INSERT INTO sessions (token, expires_at) VALUES ('tok', datetime('now'))")
	if err != nil {
		t.Errorf("sessions table missing: %v", err)
	}

	_, err = db.Exec("INSERT INTO egress_log (project_id, destination, port) VALUES ('p1', 'example.com', 443)")
	if err != nil {
		t.Errorf("egress_log table missing: %v", err)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("second migrate (should be no-op): %v", err)
	}
}

func TestMigrate_ProjectDockerColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Verify migration 2 columns exist by inserting with all fields.
	_, err = db.Exec(`
		INSERT INTO projects (id, name, status, internal_port, network_id, image_name, last_error, resources)
		VALUES ('p1', 'test-proj', 'stopped', 3000, 'net-123', 'appx-base:latest', 'some error', '{"memory":"1g"}')
	`)
	if err != nil {
		t.Fatalf("insert with migration 2 columns failed: %v", err)
	}

	var networkID, imageName, lastError, resources sql.NullString
	err = db.QueryRow("SELECT network_id, image_name, last_error, resources FROM projects WHERE id = 'p1'").
		Scan(&networkID, &imageName, &lastError, &resources)
	if err != nil {
		t.Fatalf("select migration 2 columns: %v", err)
	}
	if networkID.String != "net-123" {
		t.Errorf("network_id: expected net-123, got %s", networkID.String)
	}
	if imageName.String != "appx-base:latest" {
		t.Errorf("image_name: expected appx-base:latest, got %s", imageName.String)
	}
	if lastError.String != "some error" {
		t.Errorf("last_error: expected 'some error', got %s", lastError.String)
	}
	if resources.String != `{"memory":"1g"}` {
		t.Errorf("resources: expected JSON, got %s", resources.String)
	}
}

func TestMigrate_SkipsAlreadyApplied(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}

	// Insert data
	db.Exec("INSERT INTO settings (key, value) VALUES ('keep', 'me')")

	// Run again — should not destroy data
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}

	var val string
	err = db.QueryRow("SELECT value FROM settings WHERE key = 'keep'").Scan(&val)
	if err != nil {
		t.Fatalf("data lost after re-migration: %v", err)
	}
	if val != "me" {
		t.Errorf("expected 'me', got %q", val)
	}
}

// TestMigration3ContainerSecret verifies that migration 3 adds the
// container_secret column to the projects table. This column stores the
// password used for authenticating proxy requests to the container's
// opencode serve instance.
func TestMigration3ContainerSecret(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Verify container_secret column exists by inserting and reading back.
	_, err = db.Exec(`INSERT INTO projects (id, name, status, internal_port, container_secret)
		VALUES ('test-id', 'test-proj', 'stopped', 3000, 'mysecret')`)
	if err != nil {
		t.Fatalf("insert with container_secret: %v", err)
	}

	var secret string
	err = db.QueryRow(`SELECT container_secret FROM projects WHERE id = 'test-id'`).Scan(&secret)
	if err != nil {
		t.Fatalf("select container_secret: %v", err)
	}
	if secret != "mysecret" {
		t.Errorf("got %q, want %q", secret, "mysecret")
	}
}

// TestMigration4ProjectModel verifies that migration 4 adds the assigned_port
// and opencode_project_id columns to the projects table. assigned_port tracks
// the external port allocated for the project (nullable, with unique constraint),
// and opencode_project_id stores the ID of the project in the opencode repository
// within the container.
func TestMigration4ProjectModel(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Verify assigned_port column exists and accepts values.
	_, err = db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('test-1', 'port-test', 10000)")
	if err != nil {
		t.Fatalf("insert with assigned_port: %v", err)
	}

	// Verify UNIQUE constraint on assigned_port (partial index).
	_, err = db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('test-2', 'port-dup', 10000)")
	if err == nil {
		t.Fatal("expected UNIQUE violation on assigned_port, got nil")
	}

	// Verify NULL values don't violate uniqueness (multiple projects can have NULL assigned_port).
	_, err = db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('test-3', 'null-port-1', NULL)")
	if err != nil {
		t.Fatalf("insert with NULL assigned_port: %v", err)
	}
	_, err = db.Exec("INSERT INTO projects (id, name, assigned_port) VALUES ('test-4', 'null-port-2', NULL)")
	if err != nil {
		t.Fatalf("insert with second NULL assigned_port: %v", err)
	}

	// Verify opencode_project_id column exists.
	_, err = db.Exec("UPDATE projects SET opencode_project_id = 'oc-abc123' WHERE id = 'test-1'")
	if err != nil {
		t.Fatalf("update opencode_project_id: %v", err)
	}

	var ocID sql.NullString
	err = db.QueryRow("SELECT opencode_project_id FROM projects WHERE id = 'test-1'").Scan(&ocID)
	if err != nil {
		t.Fatalf("select opencode_project_id: %v", err)
	}
	if !ocID.Valid || ocID.String != "oc-abc123" {
		t.Errorf("expected 'oc-abc123', got %v", ocID)
	}
}

// TestMigration5EgressAllowedColumn verifies that migration 5 adds the allowed
// column to the egress_log table with a default value of true (1).
func TestMigration5EgressAllowedColumn(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Verify allowed column exists with default value of 1 (true)
	_, err = db.Exec("INSERT INTO egress_log (project_id, destination, port) VALUES ('p1', 'api.anthropic.com', 443)")
	if err != nil {
		t.Fatalf("insert without allowed: %v", err)
	}

	var allowed bool
	err = db.QueryRow("SELECT allowed FROM egress_log WHERE destination = 'api.anthropic.com'").Scan(&allowed)
	if err != nil {
		t.Fatalf("select allowed: %v", err)
	}
	if !allowed {
		t.Error("expected allowed=true (default), got false")
	}

	// Verify explicit false (0) works
	_, err = db.Exec("INSERT INTO egress_log (project_id, destination, port, allowed) VALUES ('p2', 'evil.com', 443, 0)")
	if err != nil {
		t.Fatalf("insert with allowed=false: %v", err)
	}

	var blockedAllowed bool
	err = db.QueryRow("SELECT allowed FROM egress_log WHERE destination = 'evil.com'").Scan(&blockedAllowed)
	if err != nil {
		t.Fatalf("select allowed for blocked entry: %v", err)
	}
	if blockedAllowed {
		t.Error("expected allowed=false, got true")
	}
}
