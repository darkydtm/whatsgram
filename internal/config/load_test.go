package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.env")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func noEnv(string) (string, bool) { return "", false }

func equalConfig(a, b Config) bool {
	if a.Telegram.Token.Reveal() != b.Telegram.Token.Reveal() {
		return false
	}
	a.Telegram.Token, b.Telegram.Token = Secret{}, Secret{}
	return reflect.DeepEqual(a, b)
}

func TestLoadEnvFile(t *testing.T) {
	path := writeEnv(t, `WHATSGRAM_TELEGRAM_TOKEN=synthetic-secret
WHATSGRAM_TELEGRAM_CHAT_ID=-1001234567890
WHATSGRAM_TELEGRAM_ALLOWED_USER_IDS="42, 43, 42"
WHATSGRAM_TELEGRAM_ENDPOINT=http://localhost:8081///
WHATSGRAM_TELEGRAM_LOCAL_API=true
WHATSGRAM_WHATSAPP_PAIRING_MODE=code
WHATSGRAM_WHATSAPP_PHONE="15551234567"
WHATSGRAM_WHATSAPP_HISTORY_POLICY=recent
WHATSGRAM_DATABASE_PATH=custom/database.db
WHATSGRAM_DATABASE_BUSY_TIMEOUT=4s
WHATSGRAM_MEDIA_TEMP_DIR=custom/media
WHATSGRAM_MEDIA_FFMPEG=custom/ffmpeg
WHATSGRAM_MEDIA_MAX_BYTES=123456
WHATSGRAM_MEDIA_MAX_DURATION=4m
WHATSGRAM_MEDIA_TIMEOUT=20s
WHATSGRAM_MEDIA_WORKERS=3
WHATSGRAM_RUNTIME_QUEUE_CAPACITY=123
WHATSGRAM_RUNTIME_OPERATION_TIMEOUT=20s
WHATSGRAM_RUNTIME_MAX_ATTEMPTS=3
WHATSGRAM_RUNTIME_RETRY_BASE=2s
WHATSGRAM_RUNTIME_RETRY_MAX=20s
WHATSGRAM_RUNTIME_SHUTDOWN_TIMEOUT=10s
`)
	c, err := Load(path, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Telegram: Telegram{Token: NewSecret("synthetic-secret"), ChatID: -1001234567890, AllowedUserIDs: []int64{42, 43}, Endpoint: "http://localhost:8081", LocalAPI: true},
		WhatsApp: WhatsApp{PairingMode: "code", Phone: "15551234567", HistoryPolicy: "recent"},
		Database: Database{Path: "custom/database.db", BusyTimeout: 4 * time.Second},
		Media:    Media{TempDir: "custom/media", FFmpeg: "custom/ffmpeg", MaxBytes: 123456, MaxDuration: 4 * time.Minute, Timeout: 20 * time.Second, Workers: 3},
		Runtime:  Runtime{QueueCapacity: 123, OperationTimeout: 20 * time.Second, MaxAttempts: 3, RetryBase: 2 * time.Second, RetryMax: 20 * time.Second, ShutdownTimeout: 10 * time.Second},
	}
	if !equalConfig(c, want) {
		t.Fatal(".env overrides or normalization incorrect")
	}
}

func TestLoadEnvDefaults(t *testing.T) {
	c, err := Load(writeEnv(t, "WHATSGRAM_TELEGRAM_TOKEN=synthetic-secret\nWHATSGRAM_TELEGRAM_CHAT_ID=-1001234567890\nWHATSGRAM_TELEGRAM_ALLOWED_USER_IDS=42\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !equalConfig(c, validConfig()) {
		t.Fatal("omitted values must retain defaults")
	}
	if _, err := Load("", noEnv); err == nil {
		t.Fatal("loading defaults must still validate required access")
	}
}
