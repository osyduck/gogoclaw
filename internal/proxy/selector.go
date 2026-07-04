// Package proxy is the local OpenAI/Anthropic-compatible LLM gateway that
// forwards requests to the AutoClaw upstream using a rotated account token.
package proxy

import (
	"errors"
	"sync"

	"gogoclaw/internal/store"
)

// ErrNoEligible means no account is currently usable (active + positive balance).
var ErrNoEligible = errors.New("no eligible accounts")

// selectorStore is the subset of store.Store the selector needs.
type selectorStore interface {
	List() ([]store.Account, error)
	GetProxyConfig() (store.ProxyConfig, error)
}

// Selector chooses an account per request according to the configured mode.
type Selector struct {
	st selectorStore

	mu     sync.Mutex
	cursor string // email of the currently pinned account
	count  int    // requests served on the current account (rotate_after_n)
}

func NewSelector(st selectorStore) *Selector { return &Selector{st: st} }

// eligible returns accounts that are active with positive balance, preserving
// store order (added_at).
func (s *Selector) eligible() ([]store.Account, error) {
	all, err := s.st.List()
	if err != nil {
		return nil, err
	}
	out := make([]store.Account, 0, len(all))
	for _, a := range all {
		if a.Status == store.StatusActive && a.Balance > 0 {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, ErrNoEligible
	}
	return out, nil
}

// indexOf returns the position of email in list, or -1.
func indexOf(list []store.Account, email string) int {
	for i, a := range list {
		if a.Email == email {
			return i
		}
	}
	return -1
}

// Pick returns the account to use for a new client request.
func (s *Selector) Pick() (store.Account, error) {
	cfg, err := s.st.GetProxyConfig()
	if err != nil {
		return store.Account{}, err
	}
	list, err := s.eligible()
	if err != nil {
		return store.Account{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx := indexOf(list, s.cursor)
	switch cfg.Mode {
	case "round_robin":
		if idx < 0 {
			idx = 0
		} else {
			idx = (idx + 1) % len(list)
		}
	case "rotate_after_n":
		n := cfg.N
		if n < 1 {
			n = 1
		}
		if idx < 0 {
			idx, s.count = 0, 1
		} else {
			s.count++
			if s.count > n {
				s.count = 1
				idx = (idx + 1) % len(list)
			}
		}
	default: // "sticky"
		if idx < 0 {
			idx = 0
		}
	}
	chosen := list[idx]
	s.cursor = chosen.Email
	return chosen, nil
}

// Next advances to the next eligible account not present in tried (failover).
func (s *Selector) Next(tried map[string]bool) (store.Account, error) {
	list, err := s.eligible()
	if err != nil {
		return store.Account{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	start := indexOf(list, s.cursor)
	if start < 0 {
		start = 0
	}
	for i := 1; i <= len(list); i++ {
		cand := list[(start+i)%len(list)]
		if !tried[cand.Email] {
			s.cursor = cand.Email
			s.count = 1
			return cand, nil
		}
	}
	return store.Account{}, errors.New("all eligible accounts exhausted")
}

// Current returns the email of the account most recently chosen ("" if none).
func (s *Selector) Current() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}
