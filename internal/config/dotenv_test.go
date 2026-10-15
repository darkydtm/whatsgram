package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDotEnvValidParsing(t *testing.T) {
	content := `
# Comment line
  # Indented comment
export WHATSGRAM_TELEGRAM_TOKEN='literal-token'
WHATSGRAM_TELEGRAM_CHAT_ID=-1001234567890 # inline comment
WHATSGRAM_TELEGRAM_ALLOWED_USER_IDS="42, 43, 44" # user IDs
WHATSGRAM_TELEGRAM_ENDPOINT="http://localhost:8081///"
export WHATSGRAM_TELEGRAM_LOCAL_API=true
WHATSGRAM_WHATSAPP_PAIRING_MODE=code
WHATSGRAM_WHATSAPP_PHONE="15551234567"
WHATSGRAM_WHATSAPP_HISTORY_POLICY='recent'
WHATSGRAM_DATABASE_PATH=data/custom.db
WHATSGRAM_DATABASE_BUSY_TIMEOUT=10s
WHATSGRAM_MEDIA_TEMP_DIR='media/tmp'
WHATSGRAM_MEDIA_FFMPEG=ffmpeg
WHATSGRAM_MEDIA_MAX_BYTES=10485760
WHATSGRAM_MEDIA_MAX_DURATION=5m
WHATSGRAM_MEDIA_TIMEOUT=30s
WHATSGRAM_MEDIA_WORKERS=4
WHATSGRAM_RUNTIME_QUEUE_CAPACITY=512
WHATSGRAM_RUNTIME_OPERATION_TIMEOUT=45s
WHATSGRAM_RUNTIME_MAX_ATTEMPTS=10
WHATSGRAM_RUNTIME_RETRY_BASE=2s
WHATSGRAM_RUNTIME_RETRY_MAX=30s
WHATSGRAM_RUNTIME_SHUTDOWN_TIMEOUT=20s
`
	path := writeEnv(t, content)
	c, err := Load(path, noEnv)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	if c.Telegram.Token.Reveal() != "literal-token" {
		t.Fatalf("unexpected token: %s", c.Telegram.Token.Reveal())
	}
	if c.Telegram.ChatID != -1001234567890 {
		t.Fatalf("unexpected chat ID: %d", c.Telegram.ChatID)
	}
	if !reflect.DeepEqual(c.Telegram.AllowedUserIDs, []int64{42, 43, 44}) {
		t.Fatalf("unexpected allowed user IDs: %v", c.Telegram.AllowedUserIDs)
	}
	if c.Telegram.Endpoint != "http://localhost:8081" {
		t.Fatalf("unexpected endpoint: %s", c.Telegram.Endpoint)
	}
	if !c.Telegram.LocalAPI {
		t.Fatal("expected local API to be true")
	}
	if c.WhatsApp.PairingMode != "code" || c.WhatsApp.Phone != "15551234567" || c.WhatsApp.HistoryPolicy != "recent" {
		t.Fatal("unexpected WhatsApp settings")
	}
	if c.Database.Path != "data/custom.db" || c.Database.BusyTimeout != 10*time.Second {
		t.Fatal("unexpected database settings")
	}
	if c.Media.MaxBytes != 10485760 || c.Media.MaxDuration != 5*time.Minute || c.Media.Timeout != 30*time.Second || c.Media.Workers != 4 {
		t.Fatal("unexpected media settings")
	}
	if c.Runtime.QueueCapacity != 512 || c.Runtime.OperationTimeout != 45*time.Second || c.Runtime.MaxAttempts != 10 || c.Runtime.RetryBase != 2*time.Second || c.Runtime.RetryMax != 30*time.Second || c.Runtime.ShutdownTimeout != 20*time.Second {
		t.Fatal("unexpected runtime settings")
	}
}

func TestDotEnvQuotingAndEscaping(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"single literal backslash n", "FOO='line1\\nline2'", `line1\nline2`},
		{"single literal dollar", "FOO='$VALUE'", `$VALUE`},
		{"single literal double quotes", `FOO='"quoted"'`, `"quoted"`},
		{"double escape newline", `FOO="line1\nline2"`, "line1\nline2"},
		{"double escape tab", `FOO="col1\tcol2"`, "col1\tcol2"},
		{"double escape carriage return", `FOO="cr\r"`, "cr\r"},
		{"double escape quote", `FOO="inner \"quoted\" text"`, `inner "quoted" text`},
		{"double escape backslash", `FOO="path\\to\\file"`, `path\to\file`},
		{"unquoted trimmed", `FOO=  trimmed value   `, "trimmed value"},
		{"unquoted inline comment", `FOO=value # inline comment`, "value"},
		{"unquoted with hash without whitespace", `FOO=val#notcomment`, "val#notcomment"},
		{"unquoted url with fragment", `FOO=http://example.com/api#fragment`, "http://example.com/api#fragment"},
		{"empty unquoted", `FOO=`, ""},
		{"empty single quotes", `FOO=''`, ""},
		{"empty double quotes", `FOO=""`, ""},
		{"empty with comment", `FOO= # comment`, ""},
		{"export unquoted", `export FOO=bar`, "bar"},
		{"export single quoted", `export FOO='bar'`, "bar"},
		{"export double quoted", `export FOO="bar"`, "bar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := parseDotEnv([]byte(tt.input))
			if err != nil {
				t.Fatalf("unexpected parse error: %v", err)
			}
			if m["FOO"] != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, m["FOO"])
			}
		})
	}
}

func TestDotEnvInvalidLines(t *testing.T) {
	invalidCases := []struct {
		name  string
		input string
	}{
		{"no equals", "JUST_A_STRING"},
		{"no equals with spaces", "NO EQUALS HERE"},
		{"empty key", "=value"},
		{"whitespace key", "   =value"},
		{"key with space", "KEY WITH SPACE=value"},
		{"key with tab", "KEY\tNAME=value"},
		{"unclosed single quote", "KEY='unclosed"},
		{"unclosed double quote", "KEY=\"unclosed"},
		{"unclosed double quote escaped", `KEY="unclosed\"`},
		{"trailing characters single quote", "KEY='val'extra"},
		{"trailing characters double quote", `KEY="val"extra`},
	}

	for _, tt := range invalidCases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDotEnv([]byte(tt.input))
			if err == nil {
				t.Fatal("expected parse error for invalid line")
			}
			if err.Error() != "invalid configuration environment file" {
				t.Fatalf("expected generic syntax error, got %v", err)
			}
		})
	}
}

func TestDotEnvFileSizeLimit(t *testing.T) {
	const maxBytes = 1024 * 1024
	validHeader := "WHATSGRAM_TELEGRAM_TOKEN=synthetic-secret\nWHATSGRAM_TELEGRAM_CHAT_ID=-1001234567890\nWHATSGRAM_TELEGRAM_ALLOWED_USER_IDS=42\n#"
	paddingLen := maxBytes - len(validHeader)
	atLimit := validHeader + strings.Repeat("x", paddingLen)
	if len(atLimit) != maxBytes {
		t.Fatalf("expected %d bytes, got %d", maxBytes, len(atLimit))
	}
	path := writeEnv(t, atLimit)
	if _, err := Load(path, noEnv); err != nil {
		t.Fatalf("file at exactly 1 MiB limit failed: %v", err)
	}

	overLimit := atLimit + "x"
	overPath := writeEnv(t, overLimit)
	c, err := Load(overPath, noEnv)
	if err == nil {
		t.Fatal("oversized file must be rejected")
	}
	if err.Error() != "configuration exceeds size limit" {
		t.Fatalf("expected 'configuration exceeds size limit', got %v", err)
	}
	if !reflect.DeepEqual(c, Config{}) {
		t.Fatal("oversized file must return zero config")
	}
}

func TestDotEnvPrecedence(t *testing.T) {
	fileContent := `
WHATSGRAM_TELEGRAM_TOKEN=file-secret
WHATSGRAM_TELEGRAM_CHAT_ID=-1001111111111
WHATSGRAM_TELEGRAM_ALLOWED_USER_IDS="100, 200"
WHATSGRAM_MEDIA_TEMP_DIR="file/media"
`
	path := writeEnv(t, fileContent)

	envVars := map[string]string{
		"WHATSGRAM_TELEGRAM_TOKEN":   "env-secret",
		"WHATSGRAM_TELEGRAM_CHAT_ID": "-1002222222222",
	}

	c, err := Load(path, mapLookup(envVars))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c.Telegram.Token.Reveal() != "env-secret" {
		t.Fatalf("expected env-secret, got %s", c.Telegram.Token.Reveal())
	}
	if c.Telegram.ChatID != -1002222222222 {
		t.Fatalf("expected -1002222222222, got %d", c.Telegram.ChatID)
	}

	if !reflect.DeepEqual(c.Telegram.AllowedUserIDs, []int64{100, 200}) {
		t.Fatalf("expected [100 200], got %v", c.Telegram.AllowedUserIDs)
	}
	if c.Media.TempDir != "file/media" {
		t.Fatalf("expected 'file/media', got %s", c.Media.TempDir)
	}

	if c.Database.Path != Default().Database.Path {
		t.Fatalf("expected default database path %s, got %s", Default().Database.Path, c.Database.Path)
	}
	if c.Media.MaxBytes != Default().Media.MaxBytes {
		t.Fatalf("expected default max bytes %d, got %d", Default().Media.MaxBytes, c.Media.MaxBytes)
	}
}

func TestDotEnvErrorsDoNotLeakSecretsOrPaths(t *testing.T) {
	canarySecret := "canary-secret-value-abcdef123456"
	canaryPath := filepath.Join(t.TempDir(), "canary-dir-7890", "nonexistent-canary.env")

	c, err := Load(canaryPath, noEnv)
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
	if err.Error() != "cannot read configuration" {
		t.Fatalf("expected 'cannot read configuration', got %v", err)
	}
	if strings.Contains(err.Error(), "canary") {
		t.Fatal("error message leaks path")
	}
	if !reflect.DeepEqual(c, Config{}) {
		t.Fatal("failed load must return zero config")
	}

	c, err = Load(t.TempDir(), noEnv)
	if err == nil {
		t.Fatal("expected error for directory path")
	}
	if err.Error() != "cannot read configuration" {
		t.Fatalf("expected 'cannot read configuration', got %v", err)
	}
	if !reflect.DeepEqual(c, Config{}) {
		t.Fatal("failed load must return zero config")
	}

	syntaxErrFile := writeEnv(t, "SECRET="+canarySecret+"\nINVALID SYNTAX LINE "+canarySecret+"\n")
	c, err = Load(syntaxErrFile, noEnv)
	if err == nil {
		t.Fatal("expected error for invalid syntax")
	}
	if err.Error() != "invalid configuration environment file" {
		t.Fatalf("expected 'invalid configuration environment file', got %v", err)
	}
	if strings.Contains(err.Error(), canarySecret) {
		t.Fatal("error message leaks secret")
	}
	if !reflect.DeepEqual(c, Config{}) {
		t.Fatal("failed load must return zero config")
	}
}
