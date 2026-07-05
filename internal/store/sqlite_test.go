package store

import (
	"database/sql"
	"errors"
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

func TestUpdateBalance(t *testing.T) {
	s := newTestStore(t)
	_ = s.Add(sampleAccount())
	// A freshly added account defaults to zero credit.
	if got, _ := s.Get("a@example.com"); got.Balance != 0 {
		t.Errorf("initial balance = %d, want 0", got.Balance)
	}
	if err := s.UpdateBalance("a@example.com", 2300); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("a@example.com")
	if got.Balance != 2300 {
		t.Errorf("balance = %d, want 2300", got.Balance)
	}
	if err := s.UpdateBalance("missing@example.com", 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateBalance missing: got %v, want ErrNotFound", err)
	}
}

// TestAdd_PreservesBalanceOnReLogin guards that re-adding an account (the
// upsert path taken on re-login) does not reset its stored credit to zero.
func TestAdd_PreservesBalanceOnReLogin(t *testing.T) {
	s := newTestStore(t)
	a := sampleAccount()
	_ = s.Add(a)
	_ = s.UpdateBalance(a.Email, 999)
	a.AccessToken = "Bearer new-token"
	if err := s.Add(a); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(a.Email)
	if got.Balance != 999 {
		t.Errorf("balance = %d, want preserved 999", got.Balance)
	}
	if got.AccessToken != "Bearer new-token" {
		t.Errorf("token not updated on re-login: %s", got.AccessToken)
	}
}

// TestOpen_MigratesLegacyDBWithoutBalanceColumn opens a database created by a
// pre-credit build (no balance column) and verifies Open adds the column
// idempotently rather than failing on the missing/duplicate column.
func TestOpen_MigratesLegacyDBWithoutBalanceColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE accounts (
	  email TEXT PRIMARY KEY, user_id TEXT NOT NULL, device_id TEXT NOT NULL,
	  access_token TEXT NOT NULL, refresh_token TEXT NOT NULL,
	  access_expires_at INTEGER NOT NULL, refresh_expires_at INTEGER NOT NULL,
	  priv_pem TEXT NOT NULL, pub_pem TEXT NOT NULL,
	  added_at INTEGER NOT NULL, last_refreshed_at INTEGER NOT NULL, status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts VALUES
	  ('old@x.com','u','d','a','r',1,2,'p','P',3,4,'active')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	defer s.Close()
	got, err := s.Get("old@x.com")
	if err != nil {
		t.Fatalf("Get after migration: %v", err)
	}
	if got.Balance != 0 {
		t.Errorf("legacy balance = %d, want 0 default", got.Balance)
	}
	if err := s.UpdateBalance("old@x.com", 42); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Get("old@x.com"); got.Balance != 42 {
		t.Errorf("balance after update = %d, want 42", got.Balance)
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

func TestGet_MissingIsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get("nobody@example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get missing: got %v, want ErrNotFound", err)
	}
	if err := s.SetStatus("nobody@example.com", StatusActive); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetStatus missing: got %v, want ErrNotFound", err)
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

func TestLoginProxies_RoundTrip(t *testing.T) {
	s := newTestStore(t)

	got, err := s.GetLoginProxies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("default = %v, want empty", got)
	}

	want := []string{"http://u:p@1.2.3.4:8080", "socks5://5.6.7.8:1080"}
	if err := s.SetLoginProxies(want); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetLoginProxies()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("round-trip = %v, want %v", got, want)
	}
}

func TestLoginProxies_DropsBlankLines(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetLoginProxies([]string{"http://a:8080", "", "   "}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetLoginProxies()
	if len(got) != 1 || got[0] != "http://a:8080" {
		t.Fatalf("got %v, want one non-blank entry", got)
	}
}

func TestOpen_MigratesLoginProxyPoolTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE accounts (
	  email TEXT PRIMARY KEY, user_id TEXT NOT NULL, device_id TEXT NOT NULL,
	  access_token TEXT NOT NULL, refresh_token TEXT NOT NULL,
	  access_expires_at INTEGER NOT NULL, refresh_expires_at INTEGER NOT NULL,
	  priv_pem TEXT NOT NULL, pub_pem TEXT NOT NULL,
	  added_at INTEGER NOT NULL, last_refreshed_at INTEGER NOT NULL, status TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	defer s.Close()
	if _, err := s.GetLoginProxies(); err != nil {
		t.Fatalf("GetLoginProxies after migration: %v", err)
	}
	if err := s.SetLoginProxies([]string{"http://a:8080"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetLoginProxies(); len(got) != 1 {
		t.Errorf("got %v, want 1", got)
	}
}

func TestProxyConfigDefaultsAndRoundTrip(t *testing.T) {
	s := newTestStore(t)

	// Defaults when never set.
	got, err := s.GetProxyConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.Mode != "round_robin" || got.N != 5 || got.APIKey != "" {
		t.Fatalf("defaults = %+v", got)
	}

	// Round-trip.
	want := ProxyConfig{Mode: "rotate_after_n", N: 3, APIKey: "secret"}
	if err := s.SetProxyConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetProxyConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round-trip = %+v, want %+v", got, want)
	}
}
