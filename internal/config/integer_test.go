package config

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestLoadRejectsNonintegerYAML(t *testing.T) {
	fields := []struct{ section, key, env, valid string }{
		{"telegram", "chat_id", "TELEGRAM_CHAT_ID", "-42"},
		{"telegram", "allowed_user_ids", "TELEGRAM_ALLOWED_USER_IDS", "42"},
		{"media", "max_bytes", "MEDIA_MAX_BYTES", "50"},
		{"media", "workers", "MEDIA_WORKERS", "2"},
		{"runtime", "queue_capacity", "RUNTIME_QUEUE_CAPACITY", "256"},
		{"runtime", "max_attempts", "RUNTIME_MAX_ATTEMPTS", "5"},
	}
	for _, field := range fields {
		for _, value := range []string{"42.9", "2.0", "1e2", "-1e100", "9223372036854775808", "-9223372036854775809", "!!int 1e2", "\"42\""} {
			t.Run(field.key+"/"+value, func(t *testing.T) {
				if field.key == "chat_id" && value == "42.9" {
					value = "-1001234567890.9"
				}
				if field.key == "allowed_user_ids" {
					value = "[" + value + "]"
				}
				data := field.section + ":\n  " + field.key + ": " + value + "\n"
				for _, override := range []bool{false, true} {
					values := accessEnv()
					if override {
						values["WHATSGRAM_"+field.env] = field.valid
					} else {
						delete(values, "WHATSGRAM_"+field.env)
					}
					c, err := Load(writeYAML(t, data), mapLookup(values))
					if err == nil || !reflect.DeepEqual(c, Config{}) || strings.Contains(err.Error(), value) {
						t.Fatal("noninteger or out-of-range YAML must fail without partial config or source output")
					}
				}
			})
		}
	}
}

func TestLoadIntegerAliasesAndMerges(t *testing.T) {
	for _, data := range []string{
		"telegram:\n  chat_id: &id -1001234567890.9\n  allowed_user_ids: [*id]\n",
		"telegram:\n  allowed_user_ids: &ids [42.9]\n  <<: {allowed_user_ids: *ids}\n",
		"media: &limits\n  workers: 2.9\n  <<: *limits\n",
		"media:\n  <<: {max_bytes: 50.9}\n",
		"runtime:\n  <<: [{queue_capacity: 256.9}, {max_attempts: 5.9}]\n",
		"telegram:\n  <<: {chat_id: -1e100, allowed_user_ids: [42]}\n",
		"telegram: &access\n  chat_id: -1001234567890.9\n<<: {telegram: *access}\n",
		"media:\n  workers: &workers 2.9\nruntime:\n  max_attempts: *workers\n",
	} {
		c, err := Load(writeYAML(t, data), mapLookup(accessEnv()))
		if err == nil || !reflect.DeepEqual(c, Config{}) {
			t.Fatal("aliases and merged integer fields must be checked before environment overrides")
		}
	}
}

func TestLoadIntegerSpellingsAndBoundaries(t *testing.T) {
	for _, value := range []string{"42", "+42", "0x2a", "0o52", "052", "0b101010", "4_2", "9223372036854775807"} {
		values := accessEnv()
		delete(values, "WHATSGRAM_TELEGRAM_ALLOWED_USER_IDS")
		c, err := Load(writeYAML(t, "telegram:\n  allowed_user_ids: ["+value+"]\n"), mapLookup(values))
		if err != nil || len(c.Telegram.AllowedUserIDs) != 1 {
			t.Fatal("legal YAML integer spelling must load")
		}
	}
	values := accessEnv()
	delete(values, "WHATSGRAM_TELEGRAM_CHAT_ID")
	c, err := Load(writeYAML(t, "telegram:\n  chat_id: -9223372036854775808\n"), mapLookup(values))
	if err != nil || c.Telegram.ChatID != -9223372036854775808 {
		t.Fatal("minimum int64 chat ID must load exactly")
	}
	maxInt := strconv.FormatUint(uint64(^uint(0)>>1), 10)
	values = accessEnv()
	values["WHATSGRAM_MEDIA_WORKERS"] = "2"
	if _, err := Load(writeYAML(t, "media:\n  workers: "+maxInt+"\n"), mapLookup(values)); err != nil {
		t.Fatal("in-range integer may be replaced before semantic limit validation")
	}
	tooLarge := strconv.FormatUint(uint64(^uint(0)>>1)+1, 10)
	if c, err := Load(writeYAML(t, "media:\n  workers: "+tooLarge+"\n"), mapLookup(values)); err == nil || !reflect.DeepEqual(c, Config{}) {
		t.Fatal("source int range must be enforced before environment overrides")
	}
}
