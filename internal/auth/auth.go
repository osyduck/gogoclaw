package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/identity"
	"gogoclaw/internal/store"
)

// GoogleCred is a one-time Google credential (used only by the Plan 4 auto driver).
type GoogleCred struct {
	Email    string
	Password string
	Proxy    string // optional residential proxy; empty = none
}

// LoginDriver drives the Google consent step for a login. Manual = no-op (the UI
// opens the URL); auto (Plan 4) = a headless stealth browser.
type LoginDriver interface {
	Drive(ctx context.Context, oauthURL string, cred *GoogleCred) error
}

// ManualDriver does nothing — the user completes consent in their own browser and
// the redirect to the callback finishes the flow.
type ManualDriver struct{}

func (ManualDriver) Drive(ctx context.Context, oauthURL string, cred *GoogleCred) error { return nil }

// Session is the state of one in-flight or completed login.
type Session struct {
	State     string    `json:"state"`
	Status    string    `json:"status"` // pending | ok | error
	Email     string    `json:"email,omitempty"`
	Err       string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	identity identity.Identity
}

// AuthEngine coordinates login sessions.
type AuthEngine struct {
	api   *api.Client
	store store.Store
	bus   *events.Bus

	mu      sync.Mutex
	pending map[string]*Session
}

func New(c *api.Client, st store.Store, bus *events.Bus) *AuthEngine {
	return &AuthEngine{api: c, store: st, bus: bus, pending: make(map[string]*Session)}
}

// StartLogin generates a device identity, obtains the Google OAuth URL, records a
// pending session, and dispatches the driver. It returns immediately; the callback
// completes the flow. For ManualDriver the returned oauthURL is what the UI opens.
func (e *AuthEngine) StartLogin(ctx context.Context, driver LoginDriver, cred *GoogleCred) (string, string, error) {
	id, err := identity.New()
	if err != nil {
		return "", "", fmt.Errorf("generate identity: %w", err)
	}
	oauthURL, state, err := e.api.GoogleOAuthURL(ctx, id.DeviceID)
	if err != nil {
		return "", "", fmt.Errorf("oauth url: %w", err)
	}
	sess := &Session{State: state, Status: "pending", CreatedAt: time.Now(), identity: id}
	if cred != nil {
		// Auto/bulk logins know the target email up front; recording it lets the
		// UI show per-account progress and lets failures be attributed to a row.
		sess.Email = cred.Email
	}
	e.mu.Lock()
	e.pending[state] = sess
	e.mu.Unlock()

	go func() {
		if err := driver.Drive(context.Background(), oauthURL, cred); err != nil {
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
	if ok {
		status, sessErr = sess.Status, sess.Err
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

	res, err := e.api.GoogleOAuthLogin(ctx, sess.identity.DeviceID, code, state)
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
	if w, err := e.api.Wallets(ctx, res.AccessToken); err != nil {
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
