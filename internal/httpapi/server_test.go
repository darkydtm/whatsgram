package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestValidSignature(t *testing.T) {
	s := Server{Config: configForTest("secret")}
	body := []byte(`{"entry":[]}`)
	h := hmac.New(sha256.New, []byte("secret")); h.Write(body)
	if !s.validSignature(body, "sha256="+hex.EncodeToString(h.Sum(nil))) {
		t.Fatal("expected valid signature")
	}
	if s.validSignature(body, "sha256=00") {
		t.Fatal("expected invalid signature")
	}
}
