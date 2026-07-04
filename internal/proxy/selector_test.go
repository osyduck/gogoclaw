package proxy

import (
	"testing"
	"time"

	"gogoclaw/internal/store"
)

// memStore is an in-memory Store for proxy tests.
type memStore struct {
	accts []store.Account
	cfg   store.ProxyConfig
}

func (m *memStore) List() ([]store.Account, error)             { return m.accts, nil }
func (m *memStore) GetProxyConfig() (store.ProxyConfig, error) { return m.cfg, nil }

// Unused Store methods so *memStore satisfies the full store.Store interface.
func (m *memStore) Add(store.Account) error                                         { return nil }
func (m *memStore) Get(string) (store.Account, error)                               { return store.Account{}, nil }
func (m *memStore) UpdateTokens(string, string, string, time.Time, time.Time) error { return nil }
func (m *memStore) SetStatus(string, string) error                                  { return nil }
func (m *memStore) UpdateBalance(string, int) error                                 { return nil }
func (m *memStore) Delete(string) error                                             { return nil }
func (m *memStore) SetProxyConfig(store.ProxyConfig) error                          { return nil }

func acct(email string, bal int, status string) store.Account {
	return store.Account{Email: email, Balance: bal, Status: status}
}

func eligiblePool() []store.Account {
	return []store.Account{
		acct("a@x.com", 10, store.StatusActive),
		acct("b@x.com", 0, store.StatusActive),        // ineligible: zero balance
		acct("c@x.com", 10, store.StatusNeedsRelogin), // ineligible: status
		acct("d@x.com", 10, store.StatusActive),
	}
}

func TestPickSkipsIneligible(t *testing.T) {
	m := &memStore{accts: eligiblePool(), cfg: store.ProxyConfig{Mode: "sticky", N: 5}}
	s := NewSelector(m)
	got, err := s.Pick()
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "a@x.com" {
		t.Fatalf("sticky first pick = %s, want a@x.com", got.Email)
	}
	// Sticky stays put.
	got, _ = s.Pick()
	if got.Email != "a@x.com" {
		t.Fatalf("sticky second pick = %s, want a@x.com", got.Email)
	}
}

func TestPickRoundRobin(t *testing.T) {
	m := &memStore{accts: eligiblePool(), cfg: store.ProxyConfig{Mode: "round_robin"}}
	s := NewSelector(m)
	var seq []string
	for i := 0; i < 4; i++ {
		g, _ := s.Pick()
		seq = append(seq, g.Email)
	}
	// Only a@ and d@ are eligible; round-robin alternates.
	want := []string{"a@x.com", "d@x.com", "a@x.com", "d@x.com"}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("round-robin seq = %v, want %v", seq, want)
		}
	}
}

func TestPickRotateAfterN(t *testing.T) {
	m := &memStore{accts: eligiblePool(), cfg: store.ProxyConfig{Mode: "rotate_after_n", N: 2}}
	s := NewSelector(m)
	var seq []string
	for i := 0; i < 5; i++ {
		g, _ := s.Pick()
		seq = append(seq, g.Email)
	}
	want := []string{"a@x.com", "a@x.com", "d@x.com", "d@x.com", "a@x.com"}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("rotate-after-2 seq = %v, want %v", seq, want)
		}
	}
}

func TestNextFailover(t *testing.T) {
	m := &memStore{accts: eligiblePool(), cfg: store.ProxyConfig{Mode: "sticky"}}
	s := NewSelector(m)
	first, _ := s.Pick() // a@x.com
	tried := map[string]bool{first.Email: true}
	nxt, err := s.Next(tried)
	if err != nil {
		t.Fatal(err)
	}
	if nxt.Email != "d@x.com" {
		t.Fatalf("failover = %s, want d@x.com", nxt.Email)
	}
	tried[nxt.Email] = true
	if _, err := s.Next(tried); err == nil {
		t.Fatal("expected exhaustion error when all eligible tried")
	}
}

func TestPickEmptyPool(t *testing.T) {
	m := &memStore{accts: nil, cfg: store.ProxyConfig{Mode: "sticky"}}
	s := NewSelector(m)
	if _, err := s.Pick(); err != ErrNoEligible {
		t.Fatalf("empty pool err = %v, want ErrNoEligible", err)
	}
}
