package config

import "testing"

func TestValidateEndpoint(t *testing.T) {
	for _, endpoint := range []string{
		"https://api.telegram.org", "https://gateway.example:8443/bot/", "http://localhost:8081", "http://127.0.0.1:8081", "http://[::1]:8081",
	} {
		c := validConfig()
		c.Telegram.Endpoint = endpoint
		if err := c.Validate(); err != nil {
			t.Fatalf("valid endpoint rejected: %v", err)
		}
	}
	for _, endpoint := range []string{
		"", "api.telegram.org", "ftp://localhost", "http://gateway.example", "http://localhost.example", "http://127.0.0.2", "https:///path", "https://user:synthetic-secret@gateway.example", "https://gateway.example?token=synthetic-secret", "https://gateway.example?", "https://gateway.example#synthetic-secret", "https://gateway.example#", "https://gateway.example:65536", "https://gateway.example:0", "https://gateway.example:wrong", "https:opaque", "https://[::1", "://synthetic-secret",
	} {
		c := validConfig()
		c.Telegram.Endpoint = endpoint
		if err := c.Validate(); err == nil || err.Error() != "invalid telegram.endpoint" {
			t.Fatal("expected generic endpoint error")
		}
	}
	c := validConfig()
	c.Telegram.LocalAPI = true
	c.Telegram.Endpoint = "http://remote.example"
	if c.Validate() == nil {
		t.Fatal("local mode must not permit remote HTTP")
	}
}

func TestValidatePairing(t *testing.T) {
	for _, tt := range []struct {
		mode, phone, history string
		valid                bool
	}{
		{"qr", "", "none", true},
		{"code", "15551234567", "recent", true},
		{"qr", "15551234567", "none", true},
		{"code", "", "none", false},
		{"code", "+15551234567", "none", false},
		{"code", "015551234567", "none", false},
		{"code", "1555 1234567", "none", false},
		{"code", "1555123abc", "none", false},
		{"code", "123456", "none", false},
		{"code", "1234567890123456", "none", false},
		{"qr", "bad", "none", false},
		{"unknown", "", "none", false},
		{"qr", "", "all", false},
		{"qr", "", "", false},
	} {
		c := validConfig()
		c.WhatsApp.PairingMode = tt.mode
		c.WhatsApp.Phone = tt.phone
		c.WhatsApp.HistoryPolicy = tt.history
		if (c.Validate() == nil) != tt.valid {
			t.Fatal("unexpected pairing validation result")
		}
	}
}
