package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Claims holds the fields we read from an AutoClaw access-token JWT payload.
type Claims struct {
	Email    string // jti
	Exp      int64
	DeviceID string
}

// ParseClaims decodes (without verifying) the payload of a "Bearer <jwt>" token.
func ParseClaims(bearer string) (Claims, error) {
	tok := strings.TrimPrefix(bearer, "Bearer ")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return Claims{}, fmt.Errorf("not a JWT: %d segments", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("decode payload: %w", err)
	}
	var raw struct {
		Jti      string `json:"jti"`
		Exp      int64  `json:"exp"`
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Claims{}, fmt.Errorf("unmarshal payload: %w", err)
	}
	return Claims{Email: raw.Jti, Exp: raw.Exp, DeviceID: raw.DeviceID}, nil
}
