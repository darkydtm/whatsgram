package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestLoadInvalidEnvironment(t *testing.T) {
	for _, tt := range []struct {
		field  string
		values []string
	}{
		{"TELEGRAM_TOKEN", []string{"", " \t"}},
		{"TELEGRAM_CHAT_ID", []string{"", "0", "42", "9223372036854775808", "synthetic-secret"}},
		{"TELEGRAM_ALLOWED_USER_IDS", []string{"", "0", "-1", "42,", ",42", "42,,43", "9223372036854775808", "synthetic-secret"}},
		{"TELEGRAM_ENDPOINT", []string{"", "http://remote.example", "https://user:synthetic-secret@remote.example"}},
		{"TELEGRAM_LOCAL_API", []string{"", "synthetic-secret"}},
		{"WHATSAPP_PAIRING_MODE", []string{"", "synthetic-secret"}},
		{"WHATSAPP_HISTORY_POLICY", []string{"", "all"}},
		{"DATABASE_PATH", []string{"", " \t"}},
		{"DATABASE_BUSY_TIMEOUT", []string{"", "0s", "-1s", "25h", "synthetic-secret", "9999999999999999999h"}},
		{"MEDIA_FFMPEG", []string{"", " \t"}},
		{"MEDIA_MAX_BYTES", []string{"", "0", "-1", "10737418241", "9223372036854775808", "synthetic-secret"}},
		{"MEDIA_MAX_DURATION", []string{"", "0s", "-1s", "25h", "synthetic-secret"}},
		{"MEDIA_TIMEOUT", []string{"", "0s", "-1s", "25h", "synthetic-secret"}},
		{"MEDIA_WORKERS", []string{"", "0", "-1", "65", "9223372036854775808", "synthetic-secret"}},
		{"RUNTIME_QUEUE_CAPACITY", []string{"", "0", "-1", "65537", "synthetic-secret"}},
		{"RUNTIME_OPERATION_TIMEOUT", []string{"", "0s", "-1s", "25h", "synthetic-secret"}},
		{"RUNTIME_MAX_ATTEMPTS", []string{"", "0", "-1", "101", "synthetic-secret"}},
		{"RUNTIME_RETRY_BASE", []string{"", "0s", "-1s", "25h", "2m", "synthetic-secret"}},
		{"RUNTIME_RETRY_MAX", []string{"", "0s", "-1s", "25h", "1ns", "synthetic-secret"}},
		{"RUNTIME_SHUTDOWN_TIMEOUT", []string{"", "0s", "-1s", "25h", "synthetic-secret"}},
	} {
		t.Run(tt.field, func(t *testing.T) {
			for _, value := range tt.values {
				values := map[string]string{"WHATSGRAM_" + tt.field: value}
				c, err := Load(writeEnv(t, accessEnvFile), mapLookup(values))
				if err == nil || !reflect.DeepEqual(c, Config{}) || strings.Contains(err.Error(), "synthetic-secret") {
					t.Fatal("invalid present environment must fail without secret output")
				}
			}
		})
	}
	values := accessEnv()
	values["WHATSGRAM_WHATSAPP_PAIRING_MODE"] = "code"
	values["WHATSGRAM_WHATSAPP_PHONE"] = ""
	if _, err := Load("", mapLookup(values)); err == nil {
		t.Fatal("empty phone in code mode must fail")
	}
}

func TestLoadEmptyOptionalEnvironment(t *testing.T) {
	path := writeEnv(t, accessEnvFile+`WHATSGRAM_WHATSAPP_PHONE="15551234567"
WHATSGRAM_MEDIA_TEMP_DIR="test/media"
WHATSGRAM_TELEGRAM_LOCAL_API="true"
`)
	c, err := Load(path, mapLookup(map[string]string{
		"WHATSGRAM_WHATSAPP_PHONE":     "",
		"WHATSGRAM_MEDIA_TEMP_DIR":     "",
		"WHATSGRAM_TELEGRAM_LOCAL_API": "false",
	}))
	if err != nil || c.WhatsApp.Phone != "" || c.Media.TempDir != "" || c.Telegram.LocalAPI {
		t.Fatal("present empty optional fields must clear .env values")
	}
}

func TestLoadNilLookup(t *testing.T) {
	values := accessEnv()
	for _, field := range []string{
		"TELEGRAM_TOKEN", "TELEGRAM_CHAT_ID", "TELEGRAM_ALLOWED_USER_IDS", "TELEGRAM_ENDPOINT", "TELEGRAM_LOCAL_API",
		"WHATSAPP_PAIRING_MODE", "WHATSAPP_PHONE", "WHATSAPP_HISTORY_POLICY", "DATABASE_PATH", "DATABASE_BUSY_TIMEOUT",
		"MEDIA_TEMP_DIR", "MEDIA_FFMPEG", "MEDIA_MAX_BYTES", "MEDIA_MAX_DURATION", "MEDIA_TIMEOUT", "MEDIA_WORKERS",
		"RUNTIME_QUEUE_CAPACITY", "RUNTIME_OPERATION_TIMEOUT", "RUNTIME_MAX_ATTEMPTS", "RUNTIME_RETRY_BASE", "RUNTIME_RETRY_MAX", "RUNTIME_SHUTDOWN_TIMEOUT",
	} {
		key := "WHATSGRAM_" + field
		old, present := os.LookupEnv(key)
		t.Cleanup(func() {
			if present {
				os.Setenv(key, old)
			} else {
				os.Unsetenv(key)
			}
		})
		if value, required := values[key]; required {
			if err := os.Setenv(key, value); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	c, err := Load("", nil)
	if err != nil || !equalConfig(c, validConfig()) {
		t.Fatal("nil lookup must read process environment")
	}
	if _, err := Load("", noEnv); err == nil {
		t.Fatal("injected lookup must not read process environment")
	}
}

func TestLocalAPIRetainsMediaLimits(t *testing.T) {
	values := accessEnv()
	values["WHATSGRAM_TELEGRAM_LOCAL_API"] = "true"
	values["WHATSGRAM_TELEGRAM_ENDPOINT"] = "http://localhost:8081"
	c, err := Load("", mapLookup(values))
	if err != nil || !reflect.DeepEqual(c.Media, Default().Media) {
		t.Fatal("local API must not raise media limits")
	}
	values["WHATSGRAM_MEDIA_MAX_BYTES"] = "0"
	if _, err := Load("", mapLookup(values)); err == nil {
		t.Fatal("local API must not allow unlimited media")
	}
}
