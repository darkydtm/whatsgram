package config

import (
	"reflect"
	"strings"
	"testing"
)

const accessYAML = "telegram:\n  token: synthetic-secret\n  chat_id: -1001234567890\n  allowed_user_ids: [42]\n"

func TestLoadRejectsInvalidYAML(t *testing.T) {
	for _, tt := range []struct{ name, data string }{
		{"unknown root", accessYAML + "synthetic-secret: true\n"},
		{"unknown nested", "telegram:\n  token: synthetic-secret\n  synthetic-secret: true\n"},
		{"trailing document", accessYAML + "---\ntelegram:\n  token: other-secret\n"},
		{"trailing empty document", accessYAML + "---\n"},
		{"trailing malformed document", accessYAML + "---\n[synthetic-secret\n"},
		{"duplicate field", accessYAML + "telegram:\n  token: other-secret\n"},
		{"malformed line", "telegram: [synthetic-secret\n"},
		{"bad field type", accessYAML + "media:\n  workers: synthetic-secret\n"},
		{"bad duration", accessYAML + "media:\n  timeout: synthetic-secret\n"},
		{"numeric duration", accessYAML + "media:\n  timeout: 2000000000\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(writeYAML(t, tt.data), noEnv)
			if err == nil {
				t.Fatal("invalid YAML accepted")
			}
			if strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), "other-secret") || !reflect.DeepEqual(c, Config{}) {
				t.Fatal("failure must not return secret data")
			}
		})
	}
}

func TestLoadBoundsFile(t *testing.T) {
	const maxBytes = 1024 * 1024
	atLimit := accessYAML + "#" + strings.Repeat("x", maxBytes-len(accessYAML)-1)
	if _, err := Load(writeYAML(t, atLimit), noEnv); err != nil {
		t.Fatalf("size boundary rejected: %v", err)
	}
	if _, err := Load(writeYAML(t, atLimit+"x"), noEnv); err == nil {
		t.Fatal("oversized YAML accepted")
	}
}

func TestLoadReadErrorsAreGeneric(t *testing.T) {
	for _, path := range []string{t.TempDir() + "/synthetic-secret", t.TempDir()} {
		c, err := Load(path, noEnv)
		if err == nil || strings.Contains(err.Error(), "synthetic-secret") || !reflect.DeepEqual(c, Config{}) {
			t.Fatal("expected generic read error with zero config")
		}
	}
}
