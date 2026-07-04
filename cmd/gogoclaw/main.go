package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"gogoclaw/internal/api"
	"gogoclaw/internal/auth"
	"gogoclaw/internal/events"
	"gogoclaw/internal/refresh"
	"gogoclaw/internal/server"
	"gogoclaw/internal/store"
)

const addr = "127.0.0.1:18432"

// buildHandler wires the app graph and returns the HTTP handler plus the refresher
// (whose Run loop the caller starts).
func buildHandler(st store.Store, c *api.Client, bus *events.Bus) (http.Handler, *refresh.Refresher) {
	engine := auth.New(c, st, bus)
	refresher := refresh.New(c, st, bus)
	srv := server.New(engine, refresher, st, bus)
	return srv.Handler(), refresher
}

func main() {
	st, err := store.Open("accounts.db")
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	bus := events.New()
	handler, refresher := buildHandler(st, api.NewClient(), bus)

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
