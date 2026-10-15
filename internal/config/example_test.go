package config

import (
	"os"
	"strings"
	"testing"
)

func TestExampleConfig(t *testing.T) {
	const path = "../../.env.example"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `WHATSGRAM_TELEGRAM_TOKEN=""`) {
		t.Fatal("example must not contain a token")
	}
	if _, err := Load(path, noEnv); err == nil {
		t.Fatal("example must require an injected token")
	}
	c, err := Load(path, mapLookup(map[string]string{
		"WHATSGRAM_TELEGRAM_TOKEN": "synthetic-secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Telegram.ChatID != -1001234567890 || len(c.Telegram.AllowedUserIDs) != 1 || c.Telegram.AllowedUserIDs[0] != 42 || c.WhatsApp.HistoryPolicy != "none" || c.Media.MaxBytes != Default().Media.MaxBytes {
		t.Fatal("unexpected example values")
	}
	if !equalConfig(c, validConfig()) {
		t.Fatal("example with injected token must match expected default configuration")
	}
}

func TestMinimalExampleWithInjectedEnv(t *testing.T) {
	c, err := Load(writeEnv(t, ""), mapLookup(accessEnv()))
	if err != nil || c.Database.Path != "data/whatsgram.db" {
		t.Fatal("empty env must support env-only access configuration")
	}
}
