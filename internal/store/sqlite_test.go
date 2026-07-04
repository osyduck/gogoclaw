package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteStore {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleAccount() Account {
	now := time.Now().Truncate(time.Second)
	return Account{
		Email: "a@example.com", UserID: "u1", DeviceID: "dev1",
		AccessToken: "Bearer a", RefreshToken: "Bearer r",
		AccessExpiresAt: now.Add(24 * time.Hour), RefreshExpiresAt: now.Add(720 * time.Hour),
		PrivPEM: "PRIV", PubPEM: "PUB", AddedAt: now, LastRefreshedAt: now,
		Status: StatusActive,
	}
}

func TestAddAndGet(t *testing.T) {
	s := newTestStore(t)
	want := sampleAccount()
	if err := s.Add(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != want.Email || got.AccessToken != want.AccessToken || got.Status != StatusActive {
		t.Errorf("got = %+v", got)
	}
	if !got.AccessExpiresAt.Equal(want.AccessExpiresAt) {
		t.Errorf("aexp = %v want %v", got.AccessExpiresAt, want.AccessExpiresAt)
	}
}

func TestAdd_ReplacesDuplicate(t *testing.T) {
	s := newTestStore(t)
	a := sampleAccount()
	_ = s.Add(a)
	a.AccessToken = "Bearer a2"
	if err := s.Add(a); err != nil {
		t.Fatalf("re-add should upsert, got %v", err)
	}
	list, _ := s.List()
	if len(list) != 1 {
		t.Errorf("len = %d, want 1", len(list))
	}
	if list[0].AccessToken != "Bearer a2" {
		t.Errorf("token not updated: %s", list[0].AccessToken)
	}
}

func TestUpdateTokensAndStatus(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(sampleAccount())
	newAexp := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	if err := s.UpdateTokens("a@example.com", "Bearer na", "Bearer nr", newAexp, newAexp); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus("a@example.com", StatusNeedsRelogin); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("a@example.com")
	if got.AccessToken != "Bearer na" || got.Status != StatusNeedsRelogin {
		t.Errorf("got = %+v", got)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(sampleAccount())
	if err := s.Delete("a@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("a@example.com"); err == nil {
		t.Error("expected error getting deleted account")
	}
}

func TestUpdateTokens_ClearsFailedStatus(t *testing.T) {
	s := newTestStore(t)
	a := sampleAccount()
	a.Status = StatusRefreshFailed
	_ = s.Add(a)
	newAexp := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	newRexp := time.Now().Add(720 * time.Hour).Truncate(time.Second)
	if err := s.UpdateTokens("a@example.com", "Bearer na", "Bearer nr", newAexp, newRexp); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusActive {
		t.Errorf("status = %q, want %q", got.Status, StatusActive)
	}
	if got.AccessToken != "Bearer na" || got.RefreshToken != "Bearer nr" {
		t.Errorf("tokens not updated: %+v", got)
	}
	if !got.AccessExpiresAt.Equal(newAexp) || !got.RefreshExpiresAt.Equal(newRexp) {
		t.Errorf("expiries not updated: %+v", got)
	}
}

// TestOpen_EnablesWALAndBusyTimeout guards against regressing the
// concurrency-hardening DSN params: a background token refresher writes
// concurrently with dashboard reads/login writes, and without WAL +
// busy_timeout that produces intermittent SQLITE_BUSY errors.
//
// busy_timeout is a per-connection pragma (not persisted to the file), so
// it must be read back on the store's own *sql.DB (same package, no public
// accessor needed) rather than a freshly opened connection. journal_mode
// is persisted in the database file header, so a second connection on the
// same file would also observe "wal" — but we read both from the store's
// own db for a single, unambiguous check of what Open actually configured.
func TestOpen_EnablesWALAndBusyTimeout(t *testing.T) {
	s := newTestStore(t)

	var journalMode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	var busyTimeout int
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want %d", busyTimeout, 5000)
	}
}

// TestOpen_MemoryDSNStillParses ensures the pragma DSN suffix doesn't break
// opening a store whose path has no filesystem file (the driver strips the
// "?..." suffix before opening regardless of the ":memory:" special path).
func TestOpen_MemoryDSNStillParses(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer s.Close()
	if err := s.Add(sampleAccount()); err != nil {
		t.Fatalf("Add on in-memory store: %v", err)
	}
}

func TestMissingEmail_ReturnsNotFoundError(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get("nope@example.com"); err == nil {
		t.Error("Get: expected error for missing email")
	}
	if err := s.UpdateTokens("nope@example.com", "a", "r", time.Now(), time.Now()); err == nil {
		t.Error("UpdateTokens: expected error for missing email")
	}
	if err := s.SetStatus("nope@example.com", StatusActive); err == nil {
		t.Error("SetStatus: expected error for missing email")
	}
}
