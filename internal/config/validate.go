package config

import (
	"errors"
	"strings"
	"time"
)

func (c Config) Validate() error {
	if strings.TrimSpace(c.Telegram.Token) == "" {
		return errors.New("invalid telegram.token")
	}
	if c.Telegram.ChatID >= 0 {
		return errors.New("invalid telegram.chat_id")
	}
	if len(c.Telegram.AllowedUserIDs) == 0 {
		return errors.New("invalid telegram.allowed_user_ids")
	}
	for _, id := range c.Telegram.AllowedUserIDs {
		if id <= 0 {
			return errors.New("invalid telegram.allowed_user_ids")
		}
	}
	if !validEndpoint(c.Telegram.Endpoint) {
		return errors.New("invalid telegram.endpoint")
	}
	if c.WhatsApp.PairingMode != "qr" && c.WhatsApp.PairingMode != "code" {
		return errors.New("invalid whatsapp.pairing_mode")
	}
	if c.WhatsApp.HistoryPolicy != "none" && c.WhatsApp.HistoryPolicy != "recent" {
		return errors.New("invalid whatsapp.history_policy")
	}
	if c.WhatsApp.Phone != "" || c.WhatsApp.PairingMode == "code" {
		if len(c.WhatsApp.Phone) < 7 || len(c.WhatsApp.Phone) > 15 || c.WhatsApp.Phone[0] == '0' {
			return errors.New("invalid whatsapp.phone")
		}
		for _, digit := range c.WhatsApp.Phone {
			if digit < '0' || digit > '9' {
				return errors.New("invalid whatsapp.phone")
			}
		}
	}
	if strings.TrimSpace(c.Database.Path) == "" {
		return errors.New("invalid database.path")
	}
	if strings.TrimSpace(c.Media.FFmpeg) == "" {
		return errors.New("invalid media.ffmpeg")
	}
	for _, limit := range []struct {
		field string
		value int64
		max   int64
	}{
		{"media.max_bytes", c.Media.MaxBytes, 10 * 1024 * 1024 * 1024},
		{"media.workers", int64(c.Media.Workers), 64},
		{"runtime.queue_capacity", int64(c.Runtime.QueueCapacity), 65536},
		{"runtime.max_attempts", int64(c.Runtime.MaxAttempts), 100},
		{"database.busy_timeout", int64(c.Database.BusyTimeout), int64(24 * time.Hour)},
		{"media.max_duration", int64(c.Media.MaxDuration), int64(24 * time.Hour)},
		{"media.timeout", int64(c.Media.Timeout), int64(24 * time.Hour)},
		{"runtime.operation_timeout", int64(c.Runtime.OperationTimeout), int64(24 * time.Hour)},
		{"runtime.retry_base", int64(c.Runtime.RetryBase), int64(24 * time.Hour)},
		{"runtime.retry_max", int64(c.Runtime.RetryMax), int64(24 * time.Hour)},
		{"runtime.shutdown_timeout", int64(c.Runtime.ShutdownTimeout), int64(24 * time.Hour)},
	} {
		if limit.value <= 0 || limit.value > limit.max {
			return errors.New("invalid " + limit.field)
		}
	}
	if c.Runtime.RetryBase > c.Runtime.RetryMax {
		return errors.New("invalid runtime retry limits")
	}
	return nil
}
