package config

import (
	"os"
	"strings"
	"testing"
)

func TestExampleConfig(t *testing.T) {
	const path = "../../config.example.yaml"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `token: ""`) {
		t.Fatal("example must not contain a token")
	}
	if _, err := Load(path, noEnv); err == nil {
		t.Fatal("example must require an injected token")
	}
	c, err := Load(path, mapLookup(accessEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Telegram.ChatID != -1001234567890 || c.Telegram.AllowedUserIDs[0] != 42 || c.WhatsApp.HistoryPolicy != "none" || c.Media.MaxBytes != Default().Media.MaxBytes {
		t.Fatal("unexpected example values")
	}
}

func TestMinimalExampleWithInjectedEnv(t *testing.T) {
	c, err := Load(writeYAML(t, "{}\n"), mapLookup(accessEnv()))
	if err != nil || c.Database.Path != "data/whatsgram.db" {
		t.Fatal("empty map must support env-only access configuration")
	}
}
