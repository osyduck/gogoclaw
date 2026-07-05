package store

import "time"

const (
	StatusActive        = "active"
	StatusRefreshFailed = "refresh_failed"
	StatusNeedsRelogin  = "needs_relogin"
)

// Account is a stored AutoClaw account with its live tokens and device identity.
type Account struct {
	Email            string
	UserID           string
	DeviceID         string
	AccessToken      string
	RefreshToken     string
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	PrivPEM          string
	PubPEM           string
	AddedAt          time.Time
	LastRefreshedAt  time.Time
	Status           string
	Balance          int // AutoClaw credit (total_balance across wallets)
}

// ProxyConfig holds the LLM-gateway rotation settings (single row).
type ProxyConfig struct {
	Mode   string // "sticky" | "round_robin" | "rotate_after_n"
	N      int    // request count per account for rotate_after_n
	APIKey string // client key required on /v1/*; empty = open
}

// Store persists accounts.
type Store interface {
	Add(Account) error
	List() ([]Account, error)
	Get(email string) (Account, error)
	UpdateTokens(email, access, refresh string, aexp, rexp time.Time) error
	SetStatus(email, status string) error
	UpdateBalance(email string, balance int) error
	Delete(email string) error
	GetProxyConfig() (ProxyConfig, error)
	SetProxyConfig(ProxyConfig) error
	GetLoginProxies() ([]string, error)
	SetLoginProxies([]string) error
}
