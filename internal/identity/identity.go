package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
)

// Identity is a per-account device credential.
type Identity struct {
	DeviceID string
	PubPEM   string
	PrivPEM  string
}

// DeviceIDFromPub derives the AutoClaw device_id: SHA256 of the raw 32-byte
// ed25519 public key (SPKI-DER minus its 12-byte prefix), hex-encoded.
func DeviceIDFromPub(pub ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	if len(der) != 44 {
		return "", fmt.Errorf("unexpected SPKI length %d", len(der))
	}
	sum := sha256.Sum256(der[12:]) // strip 12-byte ed25519 SPKI prefix
	return hex.EncodeToString(sum[:]), nil
}

// New generates a fresh ed25519 keypair and derives its identity.
func New() (Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	deviceID, err := DeviceIDFromPub(pub)
	if err != nil {
		return Identity{}, err
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return Identity{}, err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return Identity{}, err
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	return Identity{DeviceID: deviceID, PubPEM: string(pubPEM), PrivPEM: string(privPEM)}, nil
}
