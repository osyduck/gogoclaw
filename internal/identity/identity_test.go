package identity

import (
	"crypto/ed25519"
	"crypto/rand"
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
