package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"WARN", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"info", slog.LevelInfo},
		{"unknown", slog.LevelInfo},
		{"", slog.LevelInfo},
	}

	for _, tc := range tests {
		if got := parseLogLevel(tc.input); got != tc.expected {
			t.Errorf("parseLogLevel(%q) = %v, expected %v", tc.input, got, tc.expected)
		}
	}
}

func TestLoggerRedactsSecrets(t *testing.T) {
	var buf bytes.Buffer
	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			key := strings.ToLower(a.Key)
			if strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password") {
				return slog.String(a.Key, "[redacted]")
			}
			return a
		},
	}
	logger := slog.New(slog.NewJSONHandler(&buf, opts))

	logger.InfoContext(context.Background(), "test message",
		"token", "super-secret-telegram-token",
		"secret_val", "wa-private-key",
		"user_password", "my-password",
		"regular_field", "safe-value",
	)

	out := buf.String()
	if strings.Contains(out, "super-secret-telegram-token") {
		t.Fatal("logger failed to redact token")
	}
	if strings.Contains(out, "wa-private-key") {
		t.Fatal("logger failed to redact secret")
	}
	if strings.Contains(out, "my-password") {
		t.Fatal("logger failed to redact password")
	}
	if !strings.Contains(out, "[redacted]") {
		t.Fatal("logger did not output [redacted]")
	}
	if !strings.Contains(out, "safe-value") {
		t.Fatal("logger lost unredacted safe value")
	}
}
