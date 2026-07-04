package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"gogoclaw/internal/auth"
	"gogoclaw/internal/events"
	"gogoclaw/internal/refresh"
	"gogoclaw/internal/store"
	"gogoclaw/web"
)

// Server owns the HTTP surface.
type Server struct {
	engine    *auth.AuthEngine
	refresher *refresh.Refresher
	store     store.Store
	bus       *events.Bus
}

func New(engine *auth.AuthEngine, refresher *refresh.Refresher, st store.Store, bus *events.Bus) *Server {
	return &Server{engine: engine, refresher: refresher, store: st, bus: bus}
}

// Handler builds the route mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/callback-google", s.handleCallback)
	mux.HandleFunc("POST /api/login/start", s.handleLoginStart)
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
		Mode string           `json:"mode"`
		Cred *auth.GoogleCred `json:"cred"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Mode == "auto" {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "auto login arrives in Plan 4"})
		return
	}
	state, url, err := s.engine.StartLogin(r.Context(), auth.ManualDriver{}, nil)
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
