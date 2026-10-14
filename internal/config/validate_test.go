package config

import (
	"strings"
	"testing"
)

func validConfig() Config {
	c := Default()
	c.Telegram.Token = NewSecret("synthetic-secret")
	c.Telegram.ChatID = -1001234567890
	c.Telegram.AllowedUserIDs = []int64{42}
	return c
}

func TestValidateRequiredAccess(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		change func(*Config)
	}{
		{"token", func(c *Config) { c.Telegram.Token = NewSecret("") }},
		{"blank token", func(c *Config) { c.Telegram.Token = NewSecret(" \t") }},
		{"zero chat", func(c *Config) { c.Telegram.ChatID = 0 }},
		{"positive chat", func(c *Config) { c.Telegram.ChatID = 42 }},
		{"empty allowlist", func(c *Config) { c.Telegram.AllowedUserIDs = nil }},
		{"zero user", func(c *Config) { c.Telegram.AllowedUserIDs = []int64{0} }},
		{"negative user", func(c *Config) { c.Telegram.AllowedUserIDs = []int64{-42} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.change(&c)
			if err := c.Validate(); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("expected secret-safe validation error")
			}
		})
	}
}
