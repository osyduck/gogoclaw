// Package sidecar manages the Python CloakBrowser sidecar subprocess.
package sidecar

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Manager spawns and health-checks the stealth-login sidecar on demand.
type Manager struct {
	python    string // python executable
	scriptDir string // working dir from which `python -m sidecar` is run
	addr      string // host:port the sidecar listens on
	http      *http.Client

	mu  sync.Mutex
	cmd *exec.Cmd
}

func New(python, scriptDir, addr string) *Manager {
	return &Manager{
		python: python, scriptDir: scriptDir, addr: addr,
		http: &http.Client{Timeout: 2 * time.Second},
	}
}

func (m *Manager) URL() string { return "http://" + m.addr }

func (m *Manager) healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL()+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := m.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Ensure guarantees a healthy sidecar, spawning `python -m sidecar` if needed.
func (m *Manager) Ensure(ctx context.Context) error {
	if m.healthy(ctx) {
		return nil
	}
	// Hold the lock across the spawn+poll (not just the spawn) so a second
	// concurrent Ensure() blocks on the re-check below instead of racing to
	// spawn its own sidecar process. The long hold is intentional.
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.healthy(ctx) { // re-check under lock
		return nil
	}
	cmd := exec.Command(m.python, "-m", "sidecar")
	cmd.Dir = m.scriptDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn sidecar: %w", err)
	}
	m.cmd = cmd

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			m.cmd = nil
			return fmt.Errorf("sidecar startup canceled: %w", ctx.Err())
		}
		if m.healthy(ctx) {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	m.cmd = nil
	return fmt.Errorf("sidecar did not become healthy at %s", m.addr)
}

// Stop terminates the spawned sidecar, if any.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		_ = m.cmd.Wait()
		m.cmd = nil
	}
}
