package project

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Store provides CRUD operations for projects in the SQLite database.
// New projects use assigned_port for app subdomain routing. Legacy Docker
// columns are retained in existing databases but ignored by new code.
type Store struct {
	db *sql.DB
}

// NewStore creates a Store backed by the given database connection.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// projectColumns is the canonical SELECT column list used by all project reads.
const projectColumns = `id, name, status, assigned_port, dev_port, last_error, created_at`

// Create inserts a new project with the given name and an auto-assigned DEV+PROD
// port pair from the published allocation range. Returns ErrInvalidName,
// ErrReservedNameSuffix, ErrDuplicateName, or ErrNoPortAvailable on failure.
// Wraps pair allocation and INSERT in a transaction to prevent TOCTOU races on
// concurrent creates.
func (s *Store) Create(name string) (*Project, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	prodPort, devPort, err := s.nextAvailablePortPairTx(tx)
	if err != nil {
		return nil, err
	}

	id := uuid.New().String()
	_, err = tx.Exec(
		"INSERT INTO projects (id, name, status, assigned_port, dev_port) VALUES (?, ?, ?, ?, ?)",
		id, name, StatusStopped, prodPort, devPort,
	)
	if err != nil {
		if strings.Contains(err.Error(), "projects.name") {
			return nil, ErrDuplicateName
		}
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("insert project: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return s.Get(id)
}

// List returns all projects ordered by creation date (newest first).
func (s *Store) List() ([]*Project, error) {
	rows, err := s.db.Query(
		`SELECT ` + projectColumns + ` FROM projects ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	projects := []*Project{}
	for rows.Next() {
		p, err := scanInto(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// Get returns a single project by ID. Returns ErrNotFound if not found.
func (s *Store) Get(id string) (*Project, error) {
	row := s.db.QueryRow(
		`SELECT `+projectColumns+` FROM projects WHERE id = ?`, id,
	)
	p, err := scanInto(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

// Delete removes a project from the database by ID. Returns ErrNotFound if not found.
func (s *Store) Delete(id string) error {
	res, err := s.db.Exec("DELETE FROM projects WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetByName returns a single project by name. Returns ErrNotFound if not found.
// Used by the subdomain reverse proxy to look up projects from the Host header.
func (s *Store) GetByName(name string) (*Project, error) {
	row := s.db.QueryRow(`SELECT `+projectColumns+` FROM projects WHERE name = ?`, name)
	p, err := scanInto(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get project by name: %w", err)
	}
	return p, nil
}

// SetError updates a project to the error state with an error message.
func (s *Store) SetError(id string, errMsg string) error {
	_, err := s.db.Exec(
		"UPDATE projects SET status = ?, last_error = ? WHERE id = ?",
		StatusError, errMsg, id,
	)
	if err != nil {
		return fmt.Errorf("set error: %w", err)
	}
	return nil
}

// usedPortsTx reads every allocated port (PROD assigned_port + DEV dev_port)
// into a set, so the pair allocator never reuses a port held by either
// environment of any project.
func (s *Store) usedPortsTx(tx *sql.Tx) (map[int]bool, error) {
	rows, err := tx.Query("SELECT assigned_port, dev_port FROM projects")
	if err != nil {
		return nil, fmt.Errorf("query allocated ports: %w", err)
	}
	defer rows.Close()

	used := map[int]bool{}
	for rows.Next() {
		var assigned, dev sql.NullInt64
		if err := rows.Scan(&assigned, &dev); err != nil {
			return nil, fmt.Errorf("scan ports: %w", err)
		}
		if assigned.Valid {
			used[int(assigned.Int64)] = true
		}
		if dev.Valid {
			used[int(dev.Int64)] = true
		}
	}
	return used, rows.Err()
}

// nextAvailablePortPairTx finds the two lowest unused ports in
// PortRangeStart-PublishedPortRangeEnd and returns them as (prod, dev) with
// prod the lower of the pair. Fills gaps left by deleted projects. Returns
// ErrNoPortAvailable if fewer than two ports are free. Runs within a
// transaction so the read and the INSERT in Create are atomic.
func (s *Store) nextAvailablePortPairTx(tx *sql.Tx) (prod int, dev int, err error) {
	used, err := s.usedPortsTx(tx)
	if err != nil {
		return 0, 0, err
	}

	pair := make([]int, 0, 2)
	for port := PortRangeStart; port <= PublishedPortRangeEnd; port++ {
		if used[port] {
			continue
		}
		pair = append(pair, port)
		if len(pair) == 2 {
			return pair[0], pair[1], nil
		}
	}
	return 0, 0, ErrNoPortAvailable
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanInto reads a project row into a Project struct.
func scanInto(sc scanner) (*Project, error) {
	var p Project
	var assignedPort sql.NullInt64
	var devPort sql.NullInt64
	var lastError sql.NullString
	err := sc.Scan(
		&p.ID, &p.Name, &p.Status, &assignedPort, &devPort,
		&lastError, &p.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	if assignedPort.Valid {
		p.AssignedPort = int(assignedPort.Int64)
	}
	if devPort.Valid {
		p.DevPort = int(devPort.Int64)
	}
	p.LastError = lastError.String
	return &p, nil
}
