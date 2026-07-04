package api

import "testing"

const sampleAccessToken = "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
	"eyJ1c2VyX2lkIjo5NTc5NiwiZGV2aWNlX2lkIjoiZWQ3NmVkMTAzZWFlNjk1ZjU1NDllMmZmNzhjYTNjZmViNjdlMmQ5ZWRlNjkwYzdjNmM1MGNlMWZkNjQxNjU2NSIsInNvdXJjZV9pZCI6ImF1dG9jbGF3YWNjZXNzX3Rva2VuIiwiZ3VpZCI6IiIsImlzX2d1ZXN0IjpmYWxzZSwicG93ZXIiOjAsImV4cCI6MTc4MzE5MjkxOCwiaWF0IjoxNzgzMTA2NTE4LCJqdGkiOiJldm1zbmlwZUBnbWFpbC5jb20ifQ." +
	"7kHC2ivutZ6WteqMB2bwpuwFMwEzNvZ4BOSUneXxKIg"

func TestParseClaims(t *testing.T) {
	c, err := ParseClaims(sampleAccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "evmsnipe@gmail.com" {
		t.Errorf("Email = %q", c.Email)
	}
	if c.Exp != 1783192918 {
		t.Errorf("Exp = %d", c.Exp)
	}
	if c.DeviceID != "ed76ed103eae695f5549e2ff78ca3cfeb67e2d9ede690c7c6c50ce1fd6416565" {
		t.Errorf("DeviceID = %q", c.DeviceID)
	}
}

func TestParseClaims_Malformed(t *testing.T) {
	if _, err := ParseClaims("Bearer not.a.jwt.at.all"); err == nil {
		t.Error("expected error for malformed token")
	}
}
