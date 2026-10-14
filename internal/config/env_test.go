package config

import (
	"reflect"
	"testing"
	"time"
)

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

func accessEnv() map[string]string {
	return map[string]string{
		"WHATSGRAM_TELEGRAM_TOKEN":            "synthetic-secret",
		"WHATSGRAM_TELEGRAM_CHAT_ID":          "-1001234567890",
		"WHATSGRAM_TELEGRAM_ALLOWED_USER_IDS": "42",
	}
}

func TestLoadEnvPrecedence(t *testing.T) {
	values := map[string]string{
		"WHATSGRAM_TELEGRAM_TOKEN":            "env-secret",
		"WHATSGRAM_TELEGRAM_CHAT_ID":          "-1009876543210",
		"WHATSGRAM_TELEGRAM_ALLOWED_USER_IDS": " 43, 44,43 ",
		"WHATSGRAM_TELEGRAM_ENDPOINT":         "http://127.0.0.1:8082///",
		"WHATSGRAM_TELEGRAM_LOCAL_API":        "true",
		"WHATSGRAM_WHATSAPP_PAIRING_MODE":     "code",
		"WHATSGRAM_WHATSAPP_PHONE":            "15551234567",
		"WHATSGRAM_WHATSAPP_HISTORY_POLICY":   "recent",
		"WHATSGRAM_DATABASE_PATH":             "env/database.db",
		"WHATSGRAM_DATABASE_BUSY_TIMEOUT":     "6s",
		"WHATSGRAM_MEDIA_TEMP_DIR":            "env/media",
		"WHATSGRAM_MEDIA_FFMPEG":              "env/ffmpeg",
		"WHATSGRAM_MEDIA_MAX_BYTES":           "654321",
		"WHATSGRAM_MEDIA_MAX_DURATION":        "5m",
		"WHATSGRAM_MEDIA_TIMEOUT":             "25s",
		"WHATSGRAM_MEDIA_WORKERS":             "4",
		"WHATSGRAM_RUNTIME_QUEUE_CAPACITY":    "234",
		"WHATSGRAM_RUNTIME_OPERATION_TIMEOUT": "25s",
		"WHATSGRAM_RUNTIME_MAX_ATTEMPTS":      "4",
		"WHATSGRAM_RUNTIME_RETRY_BASE":        "3s",
		"WHATSGRAM_RUNTIME_RETRY_MAX":         "25s",
		"WHATSGRAM_RUNTIME_SHUTDOWN_TIMEOUT":  "12s",
	}
	path := writeYAML(t, accessYAML+`whatsapp:
  pairing_mode: qr
  phone: "15557654321"
  history_policy: none
database:
  path: yaml/database.db
  busy_timeout: 4s
media:
  temp_dir: yaml/media
  ffmpeg: yaml/ffmpeg
  max_bytes: 123456
  max_duration: 4m
  timeout: 20s
  workers: 3
runtime:
  queue_capacity: 123
  operation_timeout: 20s
  max_attempts: 3
  retry_base: 2s
  retry_max: 20s
  shutdown_timeout: 10s
`)
	want := Config{
		Telegram: Telegram{Token: "env-secret", ChatID: -1009876543210, AllowedUserIDs: []int64{43, 44}, Endpoint: "http://127.0.0.1:8082", LocalAPI: true},
		WhatsApp: WhatsApp{PairingMode: "code", Phone: "15551234567", HistoryPolicy: "recent"},
		Database: Database{Path: "env/database.db", BusyTimeout: 6 * time.Second},
		Media:    Media{TempDir: "env/media", FFmpeg: "env/ffmpeg", MaxBytes: 654321, MaxDuration: 5 * time.Minute, Timeout: 25 * time.Second, Workers: 4},
		Runtime:  Runtime{QueueCapacity: 234, OperationTimeout: 25 * time.Second, MaxAttempts: 4, RetryBase: 3 * time.Second, RetryMax: 25 * time.Second, ShutdownTimeout: 12 * time.Second},
	}
	for _, source := range []string{path, ""} {
		c, err := Load(source, mapLookup(values))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(c, want) {
			t.Fatal("environment must override YAML and defaults")
		}
	}
}
