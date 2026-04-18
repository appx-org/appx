package auth

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"golang.org/x/crypto/bcrypt"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE sessions (token TEXT PRIMARY KEY, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, expires_at DATETIME);
	`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// testStore creates a Store with bcrypt.MinCost for fast tests.
func testStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore(testDB(t))
	s.SetBcryptCost(bcrypt.MinCost)
	return s
}

func TestIsPasswordSet_Empty(t *testing.T) {
	s := testStore(t)
	set, err := s.IsPasswordSet()
	if err != nil {
		t.Fatal(err)
	}
	if set {
		t.Error("expected password not set on empty DB")
	}
}

func TestSetAndCheckPassword(t *testing.T) {
	s := testStore(t)

	if err := s.SetPassword("correct-horse-battery"); err != nil {
		t.Fatal(err)
	}

	set, err := s.IsPasswordSet()
	if err != nil {
		t.Fatal(err)
	}
	if !set {
		t.Error("expected password to be set")
	}

	ok, err := s.CheckPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected correct password to pass")
	}

	ok, err = s.CheckPassword("wrong-password-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected wrong password to fail")
	}
}

func TestSetPassword_Overwrite(t *testing.T) {
	s := testStore(t)

	s.SetPassword("first-long-password")
	s.SetPassword("second-long-password")

	ok, _ := s.CheckPassword("first-long-password")
	if ok {
		t.Error("old password should not work")
	}

	ok, _ = s.CheckPassword("second-long-password")
	if !ok {
		t.Error("new password should work")
	}
}

func TestSetPassword_TooShort(t *testing.T) {
	s := testStore(t)
	if err := s.SetPassword("short"); err == nil {
		t.Error("expected error for password shorter than minPasswordLen")
	}
	if err := s.SetPassword(""); err == nil {
		t.Error("expected error for empty password")
	}
	// Exactly at the minimum should succeed.
	if err := s.SetPassword("exactly12chr"); err != nil {
		t.Errorf("expected 12-char password to be accepted: %v", err)
	}
}

func TestGeneratePassword(t *testing.T) {
	s := testStore(t)
	pw1, err := s.GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}
	pw2, err := s.GeneratePassword()
	if err != nil {
		t.Fatal(err)
	}

	if len(pw1) != 32 {
		t.Errorf("expected 32-char hex, got %d chars", len(pw1))
	}
	if pw1 == pw2 {
		t.Error("generated passwords should be unique")
	}
}

func TestCreateAndValidateSession(t *testing.T) {
	s := testStore(t)

	token, err := s.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 {
		t.Errorf("expected 64-char hex token, got %d chars", len(token))
	}

	if !s.ValidSession(token) {
		t.Error("newly created session should be valid")
	}

	if s.ValidSession("nonexistent") {
		t.Error("nonexistent session should be invalid")
	}
}

func TestValidSession_Expired(t *testing.T) {
	s := testStore(t)

	// Insert an already-expired session directly (hash the token as the store does)
	_, err := s.db.Exec(
		"INSERT INTO sessions (token, expires_at) VALUES (?, ?)",
		hashToken("expired-token"),
		time.Now().Add(-1*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}

	if s.ValidSession("expired-token") {
		t.Error("expired session should be invalid")
	}
}

func TestDeleteAllSessions(t *testing.T) {
	s := testStore(t)

	// Create two sessions.
	token1, err := s.CreateSession()
	if err != nil {
		t.Fatal(err)
	}
	token2, err := s.CreateSession()
	if err != nil {
		t.Fatal(err)
	}

	if !s.ValidSession(token1) || !s.ValidSession(token2) {
		t.Fatal("sessions should be valid before delete")
	}

	s.DeleteAllSessions()

	if s.ValidSession(token1) {
		t.Error("token1 should be invalid after DeleteAllSessions")
	}
	if s.ValidSession(token2) {
		t.Error("token2 should be invalid after DeleteAllSessions")
	}
}

func TestValidSession_SlidesExpiry(t *testing.T) {
	s := testStore(t)

	// Insert a session that expires in 1 hour (much less than sessionDuration).
	s.db.Exec("INSERT INTO sessions (token, expires_at) VALUES (?, ?)",
		hashToken("slide-token"), time.Now().Add(1*time.Hour))

	if !s.ValidSession("slide-token") {
		t.Fatal("session should be valid")
	}

	// After validation, the expiry should have been extended to ~30 days from now.
	var expires time.Time
	s.db.QueryRow("SELECT expires_at FROM sessions WHERE token = ?",
		hashToken("slide-token")).Scan(&expires)

	// The new expiry should be at least 29 days from now (allows for test execution time).
	if time.Until(expires) < 29*24*time.Hour {
		t.Errorf("expected expiry extended to ~30 days, got %v from now", time.Until(expires))
	}
}

func TestCleanExpiredSessions(t *testing.T) {
	s := testStore(t)

	// Insert one valid and one expired session (using hashed tokens as stored in DB)
	s.db.Exec("INSERT INTO sessions (token, expires_at) VALUES (?, ?)", hashToken("valid"), time.Now().Add(24*time.Hour))
	s.db.Exec("INSERT INTO sessions (token, expires_at) VALUES (?, ?)", hashToken("expired"), time.Now().Add(-1*time.Hour))

	s.CleanExpiredSessions()

	if !s.ValidSession("valid") {
		t.Error("valid session should survive cleanup")
	}
	if s.ValidSession("expired") {
		t.Error("expired session should be cleaned up")
	}
}
