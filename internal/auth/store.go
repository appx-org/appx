// Package auth implements password authentication and session management for
// the appx server. The security design follows these steps:
//
// 1. Password storage: the user's password is hashed with bcrypt
//    (DefaultCost) before being stored in the settings table. bcrypt is
//    deliberately slow, which makes offline brute-force attacks expensive.
//    Plaintext passwords are never written to disk.
//
// 2. Session token generation: on successful login, 32 bytes are read from
//    crypto/rand (the OS CSPRNG). This gives 256 bits of entropy — far more
//    than needed to make tokens unguessable even with a trillion guesses per
//    second.
//
// 3. Token hashing: only the SHA-256 hash of the token is stored in the
//    sessions table. SHA-256 (not bcrypt) is appropriate here because the
//    token already has high entropy — bcrypt's slowness is only needed for
//    low-entropy secrets like passwords. If the database is leaked, the hashes
//    cannot be reversed to recover valid tokens.
//
// 4. Cookie security: the raw token is sent to the browser as a cookie with
//    HttpOnly (no JS access), Secure (HTTPS only), and SameSite=Lax (CSRF
//    protection combined with JSON content-type enforcement) flags set.
//
// 5. Validation: on each protected request the middleware reads the cookie,
//    SHA-256-hashes it, and looks up the hash in the database. If a matching,
//    non-expired row is found, the request is allowed through.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// sessionDuration defines how long a login session remains valid (30 days).
const sessionDuration = 30 * 24 * time.Hour

// bcryptCost is the bcrypt work factor. 12 is the OWASP 2023 recommendation —
// more resistant to brute-force than the Go default of 10. Existing hashes
// (stored at any cost) continue to validate correctly because bcrypt embeds the
// cost in the hash string.
const bcryptCost = 12

// minPasswordLen is the minimum acceptable length for a user-supplied password.
const minPasswordLen = 12

// Store handles password and session persistence in the SQLite settings and
// sessions tables. It is the single data-access layer for all auth operations.
type Store struct {
	db         *sql.DB
	bcryptCost int // defaults to bcryptCost (12); tests may lower to bcrypt.MinCost
}

// NewStore creates a Store backed by the given SQLite database connection.
// The database must already have the settings and sessions tables (created by migrations).
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, bcryptCost: bcryptCost}
}

// SetBcryptCost overrides the bcrypt work factor. Only use in tests with
// bcrypt.MinCost to avoid 200ms per SetPassword/CheckPassword call.
func (s *Store) SetBcryptCost(cost int) {
	s.bcryptCost = cost
}

// IsPasswordSet checks whether a bcrypt password hash exists in the settings table.
// Used at startup to decide whether to generate and print an initial password.
func (s *Store) IsPasswordSet() (bool, error) {
	var val string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = 'password_hash'").Scan(&val)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// SetPassword hashes the plaintext password with bcrypt and upserts it into the
// settings table under the "password_hash" key. Overwrites any existing password.
// Returns an error if the password is shorter than minPasswordLen. The auto-generated
// initial password always satisfies this requirement.
func (s *Store) SetPassword(password string) error {
	if len(password) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	_, err = s.db.Exec(
		"INSERT INTO settings (key, value) VALUES ('password_hash', ?) ON CONFLICT(key) DO UPDATE SET value = ?",
		string(hash), string(hash),
	)
	return err
}

// CheckPassword compares a plaintext password against the stored bcrypt hash.
// Returns true if the password matches. Used by the login handler.
func (s *Store) CheckPassword(password string) (bool, error) {
	var hash string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = 'password_hash'").Scan(&hash)
	if err != nil {
		return false, err
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil, nil
}

// GeneratePassword returns a cryptographically random 32-character hex string
// suitable for use as an initial auto-generated password on first server run.
func (s *Store) GeneratePassword() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// hashToken returns the SHA-256 hex digest of a raw session token.
// Tokens are stored hashed in the sessions table so that a database leak
// does not directly expose valid session credentials.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CreateSession generates a new 64-character hex session token, stores its
// SHA-256 hash in the sessions table with a 30-day expiry, and returns the
// raw token to be set as a cookie.
func (s *Store) CreateSession() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	tokenHash := hashToken(token)
	expires := time.Now().Add(sessionDuration)

	_, err := s.db.Exec("INSERT INTO sessions (token, expires_at) VALUES (?, ?)", tokenHash, expires)
	if err != nil {
		return "", err
	}
	return token, nil
}

// ValidSession checks whether the given raw token corresponds to a non-expired
// session in the database. On success, the session expiry is extended by
// sessionDuration (sliding window) so active sessions are never cut off while
// abandoned sessions expire sooner. Used by the auth middleware on every
// protected request.
func (s *Store) ValidSession(token string) bool {
	tokenHash := hashToken(token)
	var expires time.Time
	err := s.db.QueryRow("SELECT expires_at FROM sessions WHERE token = ?", tokenHash).Scan(&expires)
	if err != nil {
		return false
	}
	if !time.Now().Before(expires) {
		return false
	}
	// Slide the expiry forward on each use.
	s.db.Exec("UPDATE sessions SET expires_at = ? WHERE token = ?",
		time.Now().Add(sessionDuration), tokenHash)
	return true
}

// DeleteSession removes a single session from the database by its raw token.
// Called during logout to immediately invalidate the user's session.
func (s *Store) DeleteSession(token string) {
	s.db.Exec("DELETE FROM sessions WHERE token = ?", hashToken(token))
}

// DeleteAllSessions removes every session from the database, effectively
// logging out all active users. Must be called when the password is changed
// to ensure compromised sessions cannot persist.
func (s *Store) DeleteAllSessions() {
	s.db.Exec("DELETE FROM sessions")
}

// CleanExpiredSessions deletes all sessions whose expires_at is in the past.
// Called at startup and periodically (every hour) by the server to prevent
// unbounded growth of the sessions table.
func (s *Store) CleanExpiredSessions() {
	s.db.Exec("DELETE FROM sessions WHERE expires_at < ?", time.Now())
}

// GetSetting retrieves a value from the settings table by key. Returns an
// empty string (not an error) if the key does not exist. Used for generic
// configuration like the Anthropic API key.
func (s *Store) GetSetting(key string) (string, error) {
	var val string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SetSetting upserts a key-value pair in the settings table. Used to store
// configuration like the Anthropic API key.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		"INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?",
		key, value, value,
	)
	return err
}

// DeleteSetting removes a key from the settings table. Returns no error if
// the key does not exist.
func (s *Store) DeleteSetting(key string) error {
	_, err := s.db.Exec("DELETE FROM settings WHERE key = ?", key)
	return err
}
