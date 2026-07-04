package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
  email             TEXT PRIMARY KEY,
  user_id           TEXT NOT NULL,
  device_id         TEXT NOT NULL,
  access_token      TEXT NOT NULL,
  refresh_token     TEXT NOT NULL,
  access_expires_at INTEGER NOT NULL,
  refresh_expires_at INTEGER NOT NULL,
  priv_pem          TEXT NOT NULL,
  pub_pem           TEXT NOT NULL,
  added_at          INTEGER NOT NULL,
  last_refreshed_at INTEGER NOT NULL,
  status            TEXT NOT NULL
);`

// SQLiteStore is a pure-Go SQLite-backed Store.
type SQLiteStore struct{ db *sql.DB }

// pragmaDSN appends the modernc.org/sqlite _pragma DSN params that enable
// WAL journaling and a busy timeout, so concurrent readers/writers (e.g. a
// background token refresher writing alongside dashboard reads) don't hit
// SQLITE_BUSY instead of blocking briefly. Works for both file paths and
// ":memory:" since the driver strips the "?..." suffix before opening.
func pragmaDSN(path string) string {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
}

func Open(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", pragmaDSN(path))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) Close() error { return s.db.Close() }

func (s *SQLiteStore) Add(a Account) error {
	_, err := s.db.Exec(`
INSERT INTO accounts (email,user_id,device_id,access_token,refresh_token,
  access_expires_at,refresh_expires_at,priv_pem,pub_pem,added_at,last_refreshed_at,status)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(email) DO UPDATE SET
  user_id=excluded.user_id, device_id=excluded.device_id,
  access_token=excluded.access_token, refresh_token=excluded.refresh_token,
  access_expires_at=excluded.access_expires_at, refresh_expires_at=excluded.refresh_expires_at,
  priv_pem=excluded.priv_pem, pub_pem=excluded.pub_pem,
  last_refreshed_at=excluded.last_refreshed_at, status=excluded.status`,
		a.Email, a.UserID, a.DeviceID, a.AccessToken, a.RefreshToken,
		a.AccessExpiresAt.Unix(), a.RefreshExpiresAt.Unix(), a.PrivPEM, a.PubPEM,
		a.AddedAt.Unix(), a.LastRefreshedAt.Unix(), a.Status)
	return err
}

func scanAccount(sc interface{ Scan(...any) error }) (Account, error) {
	var a Account
	var aexp, rexp, added, refreshed int64
	err := sc.Scan(&a.Email, &a.UserID, &a.DeviceID, &a.AccessToken, &a.RefreshToken,
		&aexp, &rexp, &a.PrivPEM, &a.PubPEM, &added, &refreshed, &a.Status)
	if err != nil {
		return Account{}, err
	}
	a.AccessExpiresAt = time.Unix(aexp, 0)
	a.RefreshExpiresAt = time.Unix(rexp, 0)
	a.AddedAt = time.Unix(added, 0)
	a.LastRefreshedAt = time.Unix(refreshed, 0)
	return a, nil
}

const selectCols = `email,user_id,device_id,access_token,refresh_token,
  access_expires_at,refresh_expires_at,priv_pem,pub_pem,added_at,last_refreshed_at,status`

func (s *SQLiteStore) Get(email string) (Account, error) {
	row := s.db.QueryRow(`SELECT `+selectCols+` FROM accounts WHERE email=?`, email)
	a, err := scanAccount(row)
	if err == sql.ErrNoRows {
		return Account{}, fmt.Errorf("account %q not found", email)
	}
	return a, err
}

func (s *SQLiteStore) List() ([]Account, error) {
	rows, err := s.db.Query(`SELECT ` + selectCols + ` FROM accounts ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpdateTokens(email, access, refresh string, aexp, rexp time.Time) error {
	res, err := s.db.Exec(`UPDATE accounts SET access_token=?, refresh_token=?,
  access_expires_at=?, refresh_expires_at=?, last_refreshed_at=?, status=? WHERE email=?`,
		access, refresh, aexp.Unix(), rexp.Unix(), time.Now().Unix(), StatusActive, email)
	if err != nil {
		return err
	}
	return mustAffect(res, email)
}

func (s *SQLiteStore) SetStatus(email, status string) error {
	res, err := s.db.Exec(`UPDATE accounts SET status=? WHERE email=?`, status, email)
	if err != nil {
		return err
	}
	return mustAffect(res, email)
}

func (s *SQLiteStore) Delete(email string) error {
	_, err := s.db.Exec(`DELETE FROM accounts WHERE email=?`, email)
	return err
}

func mustAffect(res sql.Result, email string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("account %q not found", email)
	}
	return nil
}
