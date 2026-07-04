package main

import (
	"context"
	"log"
	"net/http"
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

	pyPath := "python"
	if p, err := exec.LookPath("python"); err == nil {
		pyPath = p
	}
	mgr := sidecar.New(pyPath, ".", "127.0.0.1:31500")
	al := &autoLogin{mgr: mgr, driver: auth.NewAutoDriver(mgr.URL())}
	defer mgr.Stop()

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
