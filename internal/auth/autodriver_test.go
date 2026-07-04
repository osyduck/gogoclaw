package auth

import (
	"context"
	"encoding/json"
	"fmt"
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

// ndjson writes the sidecar's newline-delimited step lines followed by a final
// done line, matching the real /drive stream.
func ndjson(w http.ResponseWriter, ok bool, reason string, steps ...string) {
	for _, s := range steps {
		fmt.Fprintf(w, "%s\n", mustJSON(map[string]any{"step": s}))
	}
	fmt.Fprintf(w, "%s\n", mustJSON(map[string]any{"done": true, "ok": ok, "reason": reason}))
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestAutoDriver_PostsCredsAndStreamsSteps(t *testing.T) {
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drive" || r.Method != "POST" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		ndjson(w, true, "", "[  0.0s] launch", "[  1.0s] enter email")
	}))
	defer srv.Close()

	var steps []string
	d := NewAutoDriver(srv.URL)
	err := d.Drive(context.Background(), api.ProviderZai, "https://accounts.google.com/o",
		&GoogleCred{Email: "a@x.com", Password: "pw"}, func(s string) { steps = append(steps, s) })
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["oauth_url"] != "https://accounts.google.com/o" || gotBody["email"] != "a@x.com" || gotBody["password"] != "pw" {
		t.Errorf("body = %v", gotBody)
	}
	if gotBody["provider"] != "zai" {
		t.Errorf("provider = %q, want zai", gotBody["provider"])
	}
	if len(steps) != 2 || !strings.Contains(steps[1], "enter email") {
		t.Errorf("steps = %v, want the two streamed lines", steps)
	}
}

func TestAutoDriver_FailureReasonBecomesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ndjson(w, false, "wrong password", "[  0.0s] enter password")
	}))
	defer srv.Close()
	err := NewAutoDriver(srv.URL).Drive(context.Background(), api.ProviderGoogle, "u",
		&GoogleCred{Email: "a@x.com", Password: "bad"}, nil)
	if err == nil || !strings.Contains(err.Error(), "wrong password") {
		t.Errorf("expected error containing reason, got %v", err)
	}
}

func TestAutoDriver_NilCredErrors(t *testing.T) {
	if err := NewAutoDriver("http://127.0.0.1:1").Drive(context.Background(), api.ProviderGoogle, "u", nil, nil); err == nil {
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
		ndjson(w, true, "")
	}))
	defer srv.Close()

	d := NewAutoDriver(srv.URL)
	const n = 6
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = d.Drive(context.Background(), api.ProviderGoogle, "u", &GoogleCred{Email: "a@x.com", Password: "pw"}, nil)
		}(i)
	}
	wg.Wait()

	if got := maxSeen.Load(); got > 3 {
		t.Errorf("max concurrency = %d, want <= 3", got)
	}
}
