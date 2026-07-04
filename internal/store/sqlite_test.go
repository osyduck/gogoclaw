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
