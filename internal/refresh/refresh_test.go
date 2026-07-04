package refresh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gogoclaw/internal/api"
	"gogoclaw/internal/events"
	"gogoclaw/internal/store"
)

// tok builds a minimal unsigned JWT whose payload decodes to the given claims.
func tok(jti string, exp int64) string {
	payload, _ := json.Marshal(map[string]any{"jti": jti, "exp": exp, "device_id": "dev"})
	return "Bearer h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

var jwtNew = tok("a@example.com", 1799999999)

func seed(t *testing.T, st store.Store, aexp time.Time) {
	now := time.Now()
	err := st.Add(store.Account{
		Email: "a@example.com", UserID: "u", DeviceID: "dev",
		AccessToken: "Bearer old-a", RefreshToken: "Bearer old-r",
		AccessExpiresAt: aexp, RefreshExpiresAt: now.Add(720 * time.Hour),
		PrivPEM: "p", PubPEM: "P", AddedAt: now, LastRefreshedAt: now, Status: store.StatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newRefresher(t *testing.T, h http.HandlerFunc) (*Refresher, store.Store) {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	st, err := store.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(api.NewClientWithBase(srv.URL), st, events.New()), st
}

func TestRefreshOne_Success(t *testing.T) {
	r, st := newRefresher(t, func(w http.ResponseWriter, req *http.Request) {
		var data map[string]any
		switch req.URL.Path {
		case "/userapi/v1/refresh":
			data = map[string]any{"access_token": jwtNew, "refresh_token": jwtNew}
		case "/agent-assetmgr/api/v2/wallets":
			data = map[string]any{"total_balance": 2300}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS", "data": data})
	})
	seed(t, st, time.Now().Add(time.Hour))
	if err := r.RefreshOne(context.Background(), "a@example.com"); err != nil {
		t.Fatal(err)
	}
	acct, _ := st.Get("a@example.com")
	if acct.AccessToken != jwtNew || acct.Status != store.StatusActive {
		t.Errorf("acct = %+v", acct)
	}
	if acct.AccessExpiresAt.Unix() != 1799999999 {
		t.Errorf("aexp = %d", acct.AccessExpiresAt.Unix())
	}
	if acct.Balance != 2300 {
		t.Errorf("balance = %d, want 2300 synced after refresh", acct.Balance)
	}
}

// TestRefreshOne_SucceedsWhenBalanceFetchFails guards that a failing wallets
// call (best-effort) does not fail an otherwise-successful token refresh.
func TestRefreshOne_SucceedsWhenBalanceFetchFails(t *testing.T) {
	r, st := newRefresher(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/agent-assetmgr/api/v2/wallets" {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("boom"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"access_token": jwtNew, "refresh_token": jwtNew}})
	})
	seed(t, st, time.Now().Add(time.Hour))
	if err := r.RefreshOne(context.Background(), "a@example.com"); err != nil {
		t.Fatalf("refresh should succeed despite balance failure: %v", err)
	}
	acct, _ := st.Get("a@example.com")
	if acct.Status != store.StatusActive || acct.AccessToken != jwtNew {
		t.Errorf("acct = %+v", acct)
	}
}

func TestRefreshOne_APIErrorMarksNeedsRelogin(t *testing.T) {
	r, st := newRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 40100, "msg": "refresh token expired", "data": nil})
	})
	seed(t, st, time.Now().Add(time.Hour))
	if err := r.RefreshOne(context.Background(), "a@example.com"); err == nil {
		t.Fatal("expected error")
	}
	acct, _ := st.Get("a@example.com")
	if acct.Status != store.StatusNeedsRelogin {
		t.Errorf("status = %q, want needs_relogin", acct.Status)
	}
}

func TestRefreshOne_TransientMarksRefreshFailed(t *testing.T) {
	r, st := newRefresher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	})
	seed(t, st, time.Now().Add(time.Hour))
	if err := r.RefreshOne(context.Background(), "a@example.com"); err == nil {
		t.Fatal("expected error")
	}
	acct, _ := st.Get("a@example.com")
	if acct.Status != store.StatusRefreshFailed {
		t.Errorf("status = %q, want refresh_failed", acct.Status)
	}
}

func TestRefreshDue_OnlyRefreshesExpiring(t *testing.T) {
	calls := 0
	r, st := newRefresher(t, func(w http.ResponseWriter, req *http.Request) {
		// Count only token-refresh hits; a successful refresh also does a
		// best-effort wallets GET, which is not what this test is measuring.
		if req.URL.Path == "/userapi/v1/refresh" {
			calls++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "SUCCESS",
			"data": map[string]any{"access_token": jwtNew, "refresh_token": jwtNew}})
	})
	// Fixed "now"; account expires in 2 minutes, margin is 5 minutes → due.
	fixed := time.Unix(1_700_000_000, 0)
	r.Now = func() time.Time { return fixed }
	seed(t, st, fixed.Add(2*time.Minute))
	r.refreshDue(context.Background())
	if calls != 1 {
		t.Errorf("expected 1 refresh call, got %d", calls)
	}
	// A far-future account is not due.
	_ = st.Delete("a@example.com")
	seed(t, st, fixed.Add(48*time.Hour))
	calls = 0
	r.refreshDue(context.Background())
	if calls != 0 {
		t.Errorf("expected 0 refresh calls for non-due account, got %d", calls)
	}
}
