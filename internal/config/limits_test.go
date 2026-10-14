package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateResources(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Config)
	}{
		{"database path", func(c *Config) { c.Database.Path = " \t" }},
		{"database timeout zero", func(c *Config) { c.Database.BusyTimeout = 0 }},
		{"database timeout huge", func(c *Config) { c.Database.BusyTimeout = 25 * time.Hour }},
		{"ffmpeg", func(c *Config) { c.Media.FFmpeg = "" }},
		{"media bytes zero", func(c *Config) { c.Media.MaxBytes = 0 }},
		{"media bytes huge", func(c *Config) { c.Media.MaxBytes = 10*1024*1024*1024 + 1 }},
		{"media duration", func(c *Config) { c.Media.MaxDuration = -1 }},
		{"media timeout", func(c *Config) { c.Media.Timeout = 25 * time.Hour }},
		{"workers zero", func(c *Config) { c.Media.Workers = 0 }},
		{"workers huge", func(c *Config) { c.Media.Workers = 65 }},
		{"queue zero", func(c *Config) { c.Runtime.QueueCapacity = 0 }},
		{"queue huge", func(c *Config) { c.Runtime.QueueCapacity = 65537 }},
		{"operation timeout", func(c *Config) { c.Runtime.OperationTimeout = 0 }},
		{"attempts zero", func(c *Config) { c.Runtime.MaxAttempts = 0 }},
		{"attempts huge", func(c *Config) { c.Runtime.MaxAttempts = 101 }},
		{"retry base zero", func(c *Config) { c.Runtime.RetryBase = 0 }},
		{"retry max huge", func(c *Config) { c.Runtime.RetryMax = 25 * time.Hour }},
		{"retry relationship", func(c *Config) { c.Runtime.RetryBase = 2 * time.Minute }},
		{"shutdown timeout", func(c *Config) { c.Runtime.ShutdownTimeout = 0 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.change(&c)
			if err := c.Validate(); err == nil || strings.Contains(err.Error(), c.Telegram.Token.Reveal()) {
				t.Fatal("expected secret-safe limit error")
			}
		})
	}
	c := validConfig()
	c.Media.MaxBytes = 10 * 1024 * 1024 * 1024
	c.Media.Workers = 64
	c.Runtime.QueueCapacity = 65536
	c.Runtime.MaxAttempts = 100
	c.Runtime.RetryBase = 24 * time.Hour
	c.Runtime.RetryMax = 24 * time.Hour
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
