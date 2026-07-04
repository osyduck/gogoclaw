package refresh

import (
	"context"
	"errors"
	"log"
	"time"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/store"
)

// Tunables (vars so tests can shrink them).
var (
	TickInterval  = time.Minute
	RefreshMargin = 5 * time.Minute
)

// Refresher keeps stored access tokens fresh.
type Refresher struct {
	api   *api.Client
	store store.Store
	bus   *events.Bus
	Now   func() time.Time
}

func New(c *api.Client, st store.Store, bus *events.Bus) *Refresher {
	return &Refresher{api: c, store: st, bus: bus, Now: time.Now}
}

// Run refreshes due accounts once immediately, then every TickInterval until ctx ends.
func (r *Refresher) Run(ctx context.Context) {
	r.refreshDue(ctx)
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.refreshDue(ctx)
		}
	}
}

// refreshDue refreshes every account whose access token is within RefreshMargin of
// expiry and is not already flagged needs_relogin.
func (r *Refresher) refreshDue(ctx context.Context) {
	accts, err := r.store.List()
	if err != nil {
		log.Printf("refresh: list accounts: %v", err)
		return
	}
	cutoff := r.Now().Add(RefreshMargin)
	for _, a := range accts {
		if a.Status == store.StatusNeedsRelogin {
			continue
		}
		if a.AccessExpiresAt.After(cutoff) {
			continue
		}
		if err := r.RefreshOne(ctx, a.Email); err != nil {
			log.Printf("refresh %s: %v", a.Email, err)
		}
	}
}

// RefreshOne refreshes a single account and classifies any failure.
func (r *Refresher) RefreshOne(ctx context.Context, email string) error {
	acct, err := r.store.Get(email)
	if err != nil {
		return err
	}
	newAccess, newRefresh, err := r.api.Refresh(ctx, acct.DeviceID, acct.AccessToken, acct.RefreshToken)
	if err != nil {
		var apiErr *api.APIError
		if errors.As(err, &apiErr) {
			_ = r.store.SetStatus(email, store.StatusNeedsRelogin)
			r.bus.Publish(events.Event{Type: "refresh:failed", Email: email, Detail: "needs_relogin"})
		} else {
			_ = r.store.SetStatus(email, store.StatusRefreshFailed)
			r.bus.Publish(events.Event{Type: "refresh:failed", Email: email, Detail: "transient"})
		}
		return err
	}
	ac, err := api.ParseClaims(newAccess)
	if err != nil {
		return err
	}
	rc, _ := api.ParseClaims(newRefresh)
	if err := r.store.UpdateTokens(email, newAccess, newRefresh, time.Unix(ac.Exp, 0), time.Unix(rc.Exp, 0)); err != nil {
		return err
	}
	r.syncBalance(ctx, email, newAccess)
	r.bus.Publish(events.Event{Type: "refresh:ok", Email: email})
	return nil
}

// syncBalance best-effort fetches the account's AutoClaw credit and stores it.
// A failure here must not fail the refresh itself, so it only logs.
func (r *Refresher) syncBalance(ctx context.Context, email, accessToken string) {
	w, err := r.api.Wallets(ctx, accessToken)
	if err != nil {
		log.Printf("balance %s: %v", email, err)
		return
	}
	if err := r.store.UpdateBalance(email, w.TotalBalance); err != nil {
		log.Printf("balance %s: store: %v", email, err)
	}
}

// RefreshAll refreshes every stored account regardless of expiry.
func (r *Refresher) RefreshAll(ctx context.Context) error {
	accts, err := r.store.List()
	if err != nil {
		return err
	}
	for _, a := range accts {
		if err := r.RefreshOne(ctx, a.Email); err != nil {
			log.Printf("refresh-all %s: %v", a.Email, err)
		}
	}
	return nil
}
