package identity

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNew_DeviceIDShape(t *testing.T) {
	id, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if len(id.DeviceID) != 64 {
		t.Errorf("device_id length = %d, want 64", len(id.DeviceID))
	}
	if _, err := hex.DecodeString(id.DeviceID); err != nil {
		t.Errorf("device_id not hex: %v", err)
	}
	if !strings.Contains(id.PrivPEM, "PRIVATE KEY") || !strings.Contains(id.PubPEM, "PUBLIC KEY") {
		t.Errorf("PEMs malformed")
	}
}

func TestNew_Unique(t *testing.T) {
	a, _ := New()
	b, _ := New()
	if a.DeviceID == b.DeviceID {
		t.Errorf("two identities share a device_id")
	}
}

func TestDeviceIDFromPub_Deterministic(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	first, err := DeviceIDFromPub(pub)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := DeviceIDFromPub(pub)
	if first != second {
		t.Errorf("non-deterministic: %s != %s", first, second)
	}
	if len(first) != 64 {
		t.Errorf("length = %d", len(first))
	}
}

// knownEd25519SPKIPrefix is the fixed 12-byte ASN.1 prefix that
// x509.MarshalPKIXPublicKey prepends to every raw ed25519 public key to
// produce its 44-byte SPKI DER encoding: 302a300506032b6570032100.
var knownEd25519SPKIPrefix = []byte{
	0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00,
}

func TestDeviceIDFromPub_PinsSPKIPrefixOffset(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if len(der) != 44 {
		t.Fatalf("SPKI DER length = %d, want 44", len(der))
	}
	if !bytes.Equal(der[:12], knownEd25519SPKIPrefix) {
		t.Fatalf("SPKI prefix = %x, want %x", der[:12], knownEd25519SPKIPrefix)
	}

	wantSum := sha256.Sum256(der[12:])
	want := hex.EncodeToString(wantSum[:])

	got, err := DeviceIDFromPub(pub)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("DeviceIDFromPub = %s, want %s (sha256 of der[12:])", got, want)
	}
}
