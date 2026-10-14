package config

import (
	"testing"
)

func TestValidateEndpointAuthorities(t *testing.T) {
	for _, endpoint := range []string{
		"http://::1:8081", "http://::1", "https://2001:db8::1:443", "https://example.org::443",
		"http://localhost:", "https://example.org:", "http://[::1]:", "https://[2001:db8::1]:",
		"https://[::1::]", "https://[not-an-ip]", "http://[127.0.0.1]", "https://[2001:db8:1]",
		"http://localhost.example.org", "http://127.0.0.1.example.org", "http://[::ffff:127.0.0.1]",
		"http://127.1", "http://2130706433", "http://localhost@remote.example.org", "http://remote.example.org",
	} {
		t.Run(endpoint, func(t *testing.T) {
			c := validConfig()
			c.Telegram.Endpoint = endpoint
			if err := c.Validate(); err == nil || err.Error() != "invalid telegram.endpoint" {
				t.Fatal("malformed or remote HTTP authority must return a generic endpoint error")
			}
		})
	}
	for _, endpoint := range []string{
		"http://[::1]", "http://[::1]:8081", "http://localhost", "http://localhost:8081", "http://127.0.0.1:8081",
		"https://[2001:db8::1]", "https://[2001:db8::1]:443", "https://remote.example.org:443/path", "https://example.org",
	} {
		t.Run(endpoint, func(t *testing.T) {
			c := validConfig()
			c.Telegram.Endpoint = endpoint
			if err := c.Validate(); err != nil {
				t.Fatal("well-formed permitted authority must remain valid")
			}
		})
	}
}
