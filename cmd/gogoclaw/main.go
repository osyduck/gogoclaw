package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"gogoclaw/internal/api"
	"gogoclaw/internal/auth"
	"gogoclaw/internal/events"
	"gogoclaw/internal/proxy"
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

// choosePython selects the interpreter to run the sidecar. It prefers one that
// can import cloakbrowser (full stealth capability); failing that it falls back
// to any interpreter that can import aiohttp, so the sidecar still starts and
// the UI reports a precise "No module named 'cloakbrowser'" per account rather
// than failing opaquely. `full` reports whether the chosen interpreter has
// cloakbrowser. Returns ("", false) when no candidate can even run the sidecar.
//
// The two tiers matter because on a machine with several Pythons, the first one
// on PATH (e.g. an unrelated venv) may have aiohttp but not cloakbrowser, while
// a different interpreter has the stealth stack — picking by aiohttp alone would
// silently choose the wrong one.
func choosePython(candidates []string, canImport func(py, mod string) bool) (py string, full bool) {
	seen := map[string]bool{}
	uniq := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		uniq = append(uniq, c)
	}
	for _, c := range uniq {
		if canImport(c, "cloakbrowser") {
			return c, true
		}
	}
	for _, c := range uniq {
		if canImport(c, "aiohttp") {
			return c, false
		}
	}
	return "", false
}

// pythonCandidates lists python executables to try, in priority order: an
// explicit override, the usual PATH names, the Windows launcher, then every
// interpreter the launcher knows about (so an interpreter that has cloakbrowser
// but isn't on PATH is still discovered).
func pythonCandidates() []string {
	c := []string{os.Getenv("GOGOCLAW_PYTHON"), "python", "python3", "py"}
	return append(c, pyLauncherPaths()...)
}

// pyLauncherPaths returns the interpreter paths reported by the Windows
// `py -0p` launcher. Best-effort: nil when py is absent (e.g. non-Windows).
func pyLauncherPaths() []string {
	out, err := exec.Command("py", "-0p").Output()
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		// Lines look like " -V:3.14 *        C:\...\python.exe"; the path is the
		// last whitespace-separated field.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		last := fields[len(fields)-1]
		if strings.Contains(last, string(os.PathSeparator)) || strings.HasSuffix(strings.ToLower(last), ".exe") {
			paths = append(paths, last)
		}
	}
	return paths
}

// canImport reports whether the given python executable can import a module,
// i.e. is a real interpreter with that dependency installed.
func canImport(py, mod string) bool {
	return exec.Command(py, "-c", "import "+mod).Run() == nil
}

// buildHandler wires the app graph and returns the HTTP handler plus the refresher
// (whose Run loop the caller starts).
func buildHandler(st store.Store, c *api.Client, bus *events.Bus, al server.AutoLogin) (http.Handler, *refresh.Refresher) {
	engine := auth.New(c, st, bus)
	refresher := refresh.New(c, st, bus)
	gw := proxy.New(st, api.BaseURL)
	srv := server.New(engine, refresher, st, bus, al, gw)
	return srv.Handler(), refresher
}

func main() {
	st, err := store.Open("accounts.db")
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	bus := events.New()

	var al server.AutoLogin
	if py, full := choosePython(pythonCandidates(), canImport); py == "" {
		log.Println("auto-login disabled: no Python with aiohttp found; " +
			"run `pip install -r sidecar/requirements.txt` and/or set GOGOCLAW_PYTHON")
	} else {
		if full {
			log.Printf("auto-login enabled via %s", py)
		} else {
			log.Printf("auto-login degraded: %s has aiohttp but not cloakbrowser — stealth logins will fail. "+
				"Install it into that interpreter (`pip install -r sidecar/requirements.txt`) "+
				"or set GOGOCLAW_PYTHON to one that has cloakbrowser", py)
		}
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
