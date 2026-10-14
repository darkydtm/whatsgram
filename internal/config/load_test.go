package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func noEnv(string) (string, bool) { return "", false }

func TestLoadYAML(t *testing.T) {
	path := writeYAML(t, `telegram:
  token: synthetic-secret
  chat_id: -1001234567890
  allowed_user_ids: [42, 43, 42]
  endpoint: http://localhost:8081///
  local_api: true
whatsapp:
  pairing_mode: code
  phone: "15551234567"
  history_policy: recent
database:
  path: custom/database.db
  busy_timeout: 4s
media:
  temp_dir: custom/media
  ffmpeg: custom/ffmpeg
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
	c, err := Load(path, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Telegram: Telegram{Token: "synthetic-secret", ChatID: -1001234567890, AllowedUserIDs: []int64{42, 43}, Endpoint: "http://localhost:8081", LocalAPI: true},
		WhatsApp: WhatsApp{PairingMode: "code", Phone: "15551234567", HistoryPolicy: "recent"},
		Database: Database{Path: "custom/database.db", BusyTimeout: 4 * time.Second},
		Media:    Media{TempDir: "custom/media", FFmpeg: "custom/ffmpeg", MaxBytes: 123456, MaxDuration: 4 * time.Minute, Timeout: 20 * time.Second, Workers: 3},
		Runtime:  Runtime{QueueCapacity: 123, OperationTimeout: 20 * time.Second, MaxAttempts: 3, RetryBase: 2 * time.Second, RetryMax: 20 * time.Second, ShutdownTimeout: 10 * time.Second},
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatal("YAML overrides or normalization incorrect")
	}
}

func TestLoadYAMLDefaults(t *testing.T) {
	c, err := Load(writeYAML(t, "telegram:\n  token: synthetic-secret\n  chat_id: -1001234567890\n  allowed_user_ids: [42]\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c, validConfig()) {
		t.Fatal("omitted values must retain defaults")
	}
	if _, err := Load("", noEnv); err == nil {
		t.Fatal("loading defaults must still validate required access")
	}
}
