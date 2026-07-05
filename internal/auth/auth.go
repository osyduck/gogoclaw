package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/identity"
	"gogoclaw/internal/store"
)

// GoogleCred is a one-time Google credential (used only by the Plan 4 auto driver).
type GoogleCred struct {
	Email        string
	Password     string
	Proxy        string // optional residential proxy; empty = none
	UseProxyPool bool   // route this account's AutoGLM API calls through the login proxy pool
}

// LoginDriver drives the consent step for a login. Manual = no-op (the UI opens
// the URL); auto (Plan 4) = a headless stealth browser. provider selects the
// driving flow (google = direct consent; zai = chat.z.ai broker). onStep, when
// non-nil, receives each timestamped progress line so the UI can show a live
// step-by-step terminal.
type LoginDriver interface {
	Drive(ctx context.Context, provider api.Provider, oauthURL string, cred *GoogleCred, onStep func(string)) error
}

// ManualDriver does nothing — the user completes consent in their own browser and
// the redirect to the callback finishes the flow.
type ManualDriver struct{}

func (ManualDriver) Drive(ctx context.Context, provider api.Provider, oauthURL string, cred *GoogleCred, onStep func(string)) error {
	return nil
}

// Session is the state of one in-flight or completed login.
type Session struct {
	State     string    `json:"state"`
	Status    string    `json:"status"` // pending | ok | error
	Email     string    `json:"email,omitempty"`
	Err       string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Steps     []string  `json:"steps,omitempty"`

	identity identity.Identity
	provider api.Provider
	order    []string // proxy failover order for this login (nil = no pool)
}

// AuthEngine coordinates login sessions.
type AuthEngine struct {
	api   *api.Client
	store store.Store
	bus   *events.Bus

	mu         sync.Mutex
	pending    map[string]*Session
	poolCursor int // round-robin index into the login proxy pool
}

func New(c *api.Client, st store.Store, bus *events.Bus) *AuthEngine {
	return &AuthEngine{api: c, store: st, bus: bus, pending: make(map[string]*Session)}
}

// StartLogin generates a device identity, obtains the Google OAuth URL, records a
// pending session, and dispatches the driver. It returns immediately; the callback
// completes the flow. For ManualDriver the returned oauthURL is what the UI opens.
func (e *AuthEngine) StartLogin(ctx context.Context, driver LoginDriver, provider api.Provider, cred *GoogleCred) (string, string, error) {
	id, err := identity.New()
	if err != nil {
		return "", "", fmt.Errorf("generate identity: %w", err)
	}
	var order []string
	if cred != nil && cred.UseProxyPool {
		order = e.nextProxyOrder()
	}
	var oauthURL, state string
	if err := e.tryVia(order, nil, func(c *api.Client) error {
		var e2 error
		oauthURL, state, e2 = c.OAuthURL(ctx, provider, id.DeviceID)
		return e2
	}); err != nil {
		return "", "", fmt.Errorf("oauth url: %w", err)
	}
	sess := &Session{State: state, Status: "pending", CreatedAt: time.Now(), identity: id, provider: provider, order: order}
	if cred != nil {
		// Auto/bulk logins know the target email up front; recording it lets the
		// UI show per-account progress and lets failures be attributed to a row.
		sess.Email = cred.Email
	}
	e.mu.Lock()
	e.pending[state] = sess
	e.mu.Unlock()

	onStep := func(line string) {
		e.mu.Lock()
		if s, ok := e.pending[state]; ok {
			s.Steps = append(s.Steps, line)
		}
		e.mu.Unlock()
	}
	go func() {
		if err := driver.Drive(context.Background(), provider, oauthURL, cred, onStep); err != nil {
			e.fail(state, fmt.Errorf("driver: %w", err))
		}
	}()
	return state, oauthURL, nil
}

// HandleCallback exchanges the OAuth code for tokens and persists the account.
func (e *AuthEngine) HandleCallback(ctx context.Context, code, state string) error {
	e.mu.Lock()
	sess, ok := e.pending[state]
	var status, sessErr string
	var provider api.Provider
	if ok {
		status, sessErr, provider = sess.Status, sess.Err, sess.provider
	}
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown login state %q", state)
	}

	// Idempotency guard: a reloaded/duplicate callback hit for a session that
	// already reached a terminal state must not re-exchange the (now-consumed)
	// OAuth code. Re-exchanging causes AutoGLM to reject the stale code, which
	// would otherwise flip a successful session's status from "ok" to "error"
	// and publish a spurious login:error event.
	switch status {
	case "ok":
		return nil
	case "error":
		return errors.New(sessErr)
	}

	step := func(line string) {
		e.mu.Lock()
		if s, ok := e.pending[state]; ok {
			s.Steps = append(s.Steps, line)
		}
		e.mu.Unlock()
	}
	var res api.LoginResult
	err := e.tryVia(sess.order, step, func(c *api.Client) error {
		var e2 error
		res, e2 = c.OAuthLogin(ctx, provider, sess.identity.DeviceID, code, state)
		return e2
	})
	if err != nil {
		e.fail(state, err)
		return err
	}
	claims, err := api.ParseClaims(res.AccessToken)
	if err != nil {
		e.fail(state, fmt.Errorf("parse access token: %w", err))
		return err
	}
	rclaims, _ := api.ParseClaims(res.RefreshToken) // best-effort refresh expiry

	now := time.Now()
	acct := store.Account{
		Email: claims.Email, UserID: res.UserID, DeviceID: sess.identity.DeviceID,
		AccessToken: res.AccessToken, RefreshToken: res.RefreshToken,
		AccessExpiresAt: time.Unix(claims.Exp, 0), RefreshExpiresAt: time.Unix(rclaims.Exp, 0),
		PrivPEM: sess.identity.PrivPEM, PubPEM: sess.identity.PubPEM,
		AddedAt: now, LastRefreshedAt: now, Status: store.StatusActive,
	}
	if err := e.store.Add(acct); err != nil {
		e.fail(state, fmt.Errorf("store account: %w", err))
		return err
	}
	// Best-effort: seed the credit balance now so it shows immediately, rather
	// than staying 0 until the background refresher first runs. A failure here
	// must not fail an otherwise-successful login.
	var w api.Wallet
	if err := e.tryVia(sess.order, nil, func(c *api.Client) error {
		var e2 error
		w, e2 = c.Wallets(ctx, res.AccessToken)
		return e2
	}); err != nil {
		log.Printf("balance %s: %v", claims.Email, err)
	} else if err := e.store.UpdateBalance(claims.Email, w.TotalBalance); err != nil {
		log.Printf("balance %s: store: %v", claims.Email, err)
	}

	e.mu.Lock()
	sess.Status = "ok"
	sess.Email = claims.Email
	e.mu.Unlock()
	e.bus.Publish(events.Event{Type: "login:ok", Email: claims.Email})
	return nil
}

// Status returns a snapshot of a session (without the private identity).
func (e *AuthEngine) Status(state string) (Session, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	sess, ok := e.pending[state]
	if !ok {
		return Session{}, false
	}
	snap := *sess
	snap.identity = identity.Identity{}
	// Copy the steps slice so callers never read it while Drive appends under lock.
	snap.Steps = append([]string(nil), sess.Steps...)
	return snap, true
}

func (e *AuthEngine) fail(state string, err error) {
	e.mu.Lock()
	var email string
	if sess, ok := e.pending[state]; ok {
		sess.Status = "error"
		sess.Err = err.Error()
		email = sess.Email
	}
	e.mu.Unlock()
	e.bus.Publish(events.Event{Type: "login:error", Email: email, Detail: err.Error()})
}

// nextProxyOrder snapshots the login proxy pool rotated by a round-robin cursor,
// so successive accounts in a batch start on different proxies. Returns nil when
// the pool is empty or unreadable (the flow then runs unproxied).
func (e *AuthEngine) nextProxyOrder() []string {
	proxies, err := e.store.GetLoginProxies()
	if err != nil || len(proxies) == 0 {
		return nil
	}
	e.mu.Lock()
	start := e.poolCursor % len(proxies)
	e.poolCursor++
	e.mu.Unlock()
	order := make([]string, 0, len(proxies))
	for i := 0; i < len(proxies); i++ {
		order = append(order, proxies[(start+i)%len(proxies)])
	}
	return order
}

// tryVia runs call, routing AutoGLM requests through each proxy in order until
// one succeeds or a non-retryable error occurs. An empty order calls once with
// the plain (direct) client. onStep, when non-nil, receives a progress line per
// attempt so the UI can show failover live.
func (e *AuthEngine) tryVia(order []string, onStep func(string), call func(*api.Client) error) error {
	if len(order) == 0 {
		return call(e.api)
	}
	var lastErr error
	for i, p := range order {
		client, err := e.api.WithProxy(p)
		if err != nil {
			lastErr = err
			continue
		}
		if onStep != nil {
			onStep(fmt.Sprintf("attempt %d/%d via proxy %s", i+1, len(order), proxyHost(p)))
		}
		lastErr = call(client)
		if lastErr == nil {
			return nil
		}
		if !retryable(lastErr) {
			return lastErr
		}
		if onStep != nil {
			onStep(fmt.Sprintf("proxy %s rejected (%v) — trying next", proxyHost(p), lastErr))
		}
	}
	return lastErr
}

// retryable reports whether an error should trigger failover to the next proxy:
// a 630014 verification failure (flagged IP), or a transport-level error (dead
// proxy). Any other business error is terminal.
func retryable(err error) bool {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code == api.CodeVerificationFailed
	}
	return true
}

// proxyHost returns the host:port of a proxy URL for display, dropping any
// embedded credentials. Falls back to the raw string if it doesn't parse.
func proxyHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}
