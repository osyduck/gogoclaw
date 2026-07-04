package api

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
)

const (
	BaseURL  = "https://autoglm-api.autoglm.ai"
	SourceID = "autoclaw"

	appID  = "100003"
	appKey = "38d2391985e2369a5fb8227d8e6cd5e5"

	// Exported so the proxy package can build the same client-identity headers.
	Version = "1.10.3"
	Product = "autoclaw"
	TM      = "win"
	Channel = "official"
	Lang    = "en"

	UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) autoclaw/1.10.3 Chrome/130.0.6723.191 Electron/33.4.11 Safari/537.36"
)

// sign returns the x-auth-sign value for a unix-seconds timestamp.
func sign(ts int64) string {
	sum := md5.Sum([]byte(fmt.Sprintf("%s&%d&%s", appID, ts, appKey)))
	return hex.EncodeToString(sum[:])
}

// randUUID returns a random RFC-4122 v4 UUID.
func randUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// signHeaders builds the full set of signed request headers for a timestamp.
func signHeaders(ts int64) http.Header {
	h := http.Header{}
	h.Set("content-type", "application/json")
	h.Set("x-auth-appid", appID)
	h.Set("x-auth-timestamp", strconv.FormatInt(ts, 10))
	h.Set("x-auth-sign", sign(ts))
	h.Set("x-trace-id", randUUID())
	h.Set("x-version", Version)
	h.Set("x-tm", TM)
	h.Set("x-product", Product)
	h.Set("x-channel", Channel)
	h.Set("x-lang", Lang)
	h.Set("user-agent", UserAgent)
	return h
}
