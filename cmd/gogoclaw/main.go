package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"gogoclaw/internal/api"
	"gogoclaw/internal/auth"
	"gogoclaw/internal/events"
	"gogoclaw/internal/refresh"
	"gogoclaw/internal/server"
	"gogoclaw/internal/sidecar"
	"gogoclaw/internal/store"
)

const addr = "127.0.0.1:18432"

// autoLogin adapts the sidecar manager + AutoDriver to server.AutoLogin.
type autoLogin struct {
	mgr    *sidecar.Manager
	driver *auth.AutoDriver
}

func (a *autoLogin) Ensure(ctx context.Context) error { return a.mgr.Ensure(ctx) }
func (a *autoLogin) Driver() auth.LoginDriver         { return a.driver }

// pickPython returns the first candidate for which canImport reports it can
// actually run the sidecar (i.e. import aiohttp), skipping empty candidates.
// Returns "" if none work.
func pickPython(candidates []string, canImport func(string) bool) string {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if canImport(c) {
			return c
		}
	}
	return ""
}

// pythonCandidates lists python executables to try, in priority order: an
// explicit override, then the usual PATH names.
func pythonCandidates() []string {
	return []string{os.Getenv("GOGOCLAW_PYTHON"), "python", "python3"}
}

// canImportAiohttp reports whether the given python executable can import
// aiohttp, i.e. is a real interpreter capable of running the sidecar (as
// opposed to e.g. a broken/incomplete venv shim).
func canImportAiohttp(py string) bool {
	return exec.Command(py, "-c", "import aiohttp").Run() == nil
}

// buildHandler wires the app graph and returns the HTTP handler plus the refresher
// (whose Run loop the caller starts).
func buildHandler(st store.Store, c *api.Client, bus *events.Bus, al server.AutoLogin) (http.Handler, *refresh.Refresher) {
	engine := auth.New(c, st, bus)
	refresher := refresh.New(c, st, bus)
	srv := server.New(engine, refresher, st, bus, al)
	return srv.Handler(), refresher
}

func main() {
	st, err := store.Open("accounts.db")
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	bus := events.New()

	var al server.AutoLogin
	if py := pickPython(pythonCandidates(), canImportAiohttp); py == "" {
		log.Println("auto-login disabled: no Python with aiohttp found; set GOGOCLAW_PYTHON")
	} else {
		mgr := sidecar.New(py, ".", "127.0.0.1:31500")
		al = &autoLogin{mgr: mgr, driver: auth.NewAutoDriver(mgr.URL())}
		defer mgr.Stop()
	}

	handler, refresher := buildHandler(st, api.NewClient(), bus, al)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go refresher.Run(ctx)

	httpSrv := &http.Server{Addr: addr, Handler: handler}
	go func() {
		<-ctx.Done()
		_ = httpSrv.Shutdown(context.Background())
	}()

	log.Printf("GogoClaw listening on http://%s", addr)
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
	log.Println("GogoClaw stopped")
}
