package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"gogoclaw/internal/api"
	"gogoclaw/internal/auth"
	"gogoclaw/internal/events"
	"gogoclaw/internal/refresh"
	"gogoclaw/internal/store"
	"gogoclaw/web"
)

// AutoLogin drives stealth auto-logins via the sidecar (Plan 4). Nil disables auto mode.
type AutoLogin interface {
	Ensure(ctx context.Context) error
	Driver() auth.LoginDriver
}

// Server owns the HTTP surface.
type Server struct {
	engine    *auth.AuthEngine
	refresher *refresh.Refresher
	store     store.Store
	bus       *events.Bus
	autoLogin AutoLogin
}

func New(engine *auth.AuthEngine, refresher *refresh.Refresher, st store.Store, bus *events.Bus, autoLogin AutoLogin) *Server {
	return &Server{engine: engine, refresher: refresher, store: st, bus: bus, autoLogin: autoLogin}
}

// Handler builds the route mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/callback-google", s.handleCallback)
	mux.HandleFunc("GET /auth/callback-zai", s.handleCallback)
	mux.HandleFunc("POST /api/login/start", s.handleLoginStart)
	mux.HandleFunc("POST /api/login/bulk", s.handleBulkLogin)
	mux.HandleFunc("GET /api/login/status", s.handleLoginStatus)
	mux.HandleFunc("GET /api/accounts", s.handleAccounts)
	mux.HandleFunc("POST /api/accounts/refresh-all", s.handleRefreshAll)
	mux.HandleFunc("POST /api/accounts/{email}/refresh", s.handleRefreshOne)
	mux.HandleFunc("DELETE /api/accounts/{email}", s.handleDelete)
	mux.HandleFunc("GET /api/events", s.handleSSE)
	mux.Handle("/", http.FileServerFS(web.DistFS()))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode     string           `json:"mode"`
		Provider string           `json:"provider"`
		Cred     *auth.GoogleCred `json:"cred"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	provider, err := api.ParseProvider(req.Provider)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Mode == "auto" {
		if s.autoLogin == nil {
			writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "auto login not configured"})
			return
		}
		if err := s.autoLogin.Ensure(r.Context()); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		state, url, err := s.engine.StartLogin(r.Context(), s.autoLogin.Driver(), provider, req.Cred)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"state": state, "oauth_url": url})
		return
	}
	state, url, err := s.engine.StartLogin(r.Context(), auth.ManualDriver{}, provider, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": state, "oauth_url": url})
}

func (s *Server) handleLoginStatus(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.engine.Status(r.URL.Query().Get("state"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown state"})
		return
	}
	writeJSON(w, http.StatusOK, sess)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	w.Header().Set("content-type", "text/html; charset=utf-8")
	if err := s.engine.HandleCallback(r.Context(), code, state); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "<h1>Login failed</h1><p>%s</p>", err.Error())
		return
	}
	_, _ = w.Write([]byte("<h1>Login successful</h1><p>You can close this tab.</p>"))
}

type accountView struct {
	Email            string `json:"email"`
	UserID           string `json:"user_id"`
	Status           string `json:"status"`
	AccessExpiresAt  int64  `json:"access_expires_at"`
	RefreshExpiresAt int64  `json:"refresh_expires_at"`
	LastRefreshedAt  int64  `json:"last_refreshed_at"`
	AddedAt          int64  `json:"added_at"`
	Balance          int    `json:"balance"`
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	accts, err := s.store.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]accountView, 0, len(accts))
	for _, a := range accts {
		out = append(out, accountView{
			Email: a.Email, UserID: a.UserID, Status: a.Status,
			AccessExpiresAt: unixOrZero(a.AccessExpiresAt), RefreshExpiresAt: unixOrZero(a.RefreshExpiresAt),
			LastRefreshedAt: unixOrZero(a.LastRefreshedAt), AddedAt: unixOrZero(a.AddedAt),
			Balance: a.Balance,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// unixOrZero converts t to a Unix timestamp, returning 0 for the zero value
// instead of the large negative number time.Time{}.Unix() would otherwise produce.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func (s *Server) handleRefreshOne(w http.ResponseWriter, r *http.Request) {
	email := r.PathValue("email")
	if err := s.refresher.RefreshOne(r.Context(), email); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "account not found"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleRefreshAll(w http.ResponseWriter, r *http.Request) {
	if err := s.refresher.RefreshAll(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	email := r.PathValue("email")
	if err := s.store.Delete(email); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.bus.Publish(events.Event{Type: "account:deleted", Email: email})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("connection", "keep-alive")
	ch, unsub := s.bus.Subscribe()
	defer unsub()
	// Initial comment so clients (and tests) know the subscription is live.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	keepalive := time.NewTicker(30 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handleBulkLogin(w http.ResponseWriter, r *http.Request) {
	if s.autoLogin == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "auto login not configured"})
		return
	}
	var req struct {
		Provider string            `json:"provider"`
		Accounts []auth.GoogleCred `json:"accounts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Accounts) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected non-empty accounts array"})
		return
	}
	provider, err := api.ParseProvider(req.Provider)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := s.autoLogin.Ensure(r.Context()); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	type startedItem struct {
		Email string `json:"email"`
		State string `json:"state"`
	}
	type errItem struct {
		Email string `json:"email"`
		Error string `json:"error"`
	}
	var (
		mu      sync.Mutex
		started = []startedItem{}
		errs    = []errItem{}
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 3) // bounded concurrency
	)
	driver := s.autoLogin.Driver()
	for i := range req.Accounts {
		cred := req.Accounts[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			state, _, err := s.engine.StartLogin(r.Context(), driver, provider, &cred)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, errItem{Email: cred.Email, Error: err.Error()})
				return
			}
			started = append(started, startedItem{Email: cred.Email, State: state})
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"started": started, "errors": errs})
}
