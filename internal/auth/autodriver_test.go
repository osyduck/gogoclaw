package auth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gogoclaw/internal/api"
)

func TestAutoDriver_PostsCredsAndSucceeds(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drive" || r.Method != "POST" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	d := NewAutoDriver(srv.URL)
	err := d.Drive(context.Background(), api.ProviderZai, "https://accounts.google.com/o", &GoogleCred{Email: "a@x.com", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["oauth_url"] != "https://accounts.google.com/o" || gotBody["email"] != "a@x.com" || gotBody["password"] != "pw" {
		t.Errorf("body = %v", gotBody)
	}
	if gotBody["provider"] != "zai" {
		t.Errorf("provider = %q, want zai", gotBody["provider"])
	}
}

func TestAutoDriver_FailureReasonBecomesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "reason": "wrong password"})
	}))
	defer srv.Close()
	err := NewAutoDriver(srv.URL).Drive(context.Background(), api.ProviderGoogle, "u", &GoogleCred{Email: "a@x.com", Password: "bad"})
	if err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Errorf("expected error containing reason, got %v", err)
	}
}

func TestAutoDriver_NilCredErrors(t *testing.T) {
	if err := NewAutoDriver("http://127.0.0.1:1").Drive(context.Background(), api.ProviderGoogle, "u", nil); err == nil {
		t.Error("expected error for nil credentials")
	}
}

// TestAutoDriver_BoundsConcurrencyToThree ensures the AutoDriver itself caps
// concurrent /drive calls at 3, regardless of how many callers invoke Drive
// concurrently (the bulk handler's dispatch semaphore alone isn't enough,
// since StartLogin returns before the real Drive work happens). Without the
// semaphore in Drive, this test's recorded max concurrency would reach ~6.
func TestAutoDriver_BoundsConcurrencyToThree(t *testing.T) {
	var current atomic.Int32
	var maxSeen atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := current.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		current.Add(-1)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	d := NewAutoDriver(srv.URL)
	const n = 6
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = d.Drive(context.Background(), api.ProviderGoogle, "u", &GoogleCred{Email: "a@x.com", Password: "pw"})
		}(i)
	}
	wg.Wait()

	if got := maxSeen.Load(); got > 3 {
		t.Errorf("max concurrency = %d, want <= 3", got)
	}
}
