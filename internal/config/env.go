package config

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

func (c *Config) applyEnv(lookup func(string) (string, bool)) error {
	for _, field := range []struct {
		name   string
		target interface{}
	}{
		{"TELEGRAM_TOKEN", &c.Telegram.Token},
		{"TELEGRAM_CHAT_ID", &c.Telegram.ChatID},
		{"TELEGRAM_ALLOWED_USER_IDS", &c.Telegram.AllowedUserIDs},
		{"TELEGRAM_ENDPOINT", &c.Telegram.Endpoint},
		{"TELEGRAM_LOCAL_API", &c.Telegram.LocalAPI},
		{"WHATSAPP_PAIRING_MODE", &c.WhatsApp.PairingMode},
		{"WHATSAPP_PHONE", &c.WhatsApp.Phone},
		{"WHATSAPP_HISTORY_POLICY", &c.WhatsApp.HistoryPolicy},
		{"DATABASE_PATH", &c.Database.Path},
		{"DATABASE_BUSY_TIMEOUT", &c.Database.BusyTimeout},
		{"MEDIA_TEMP_DIR", &c.Media.TempDir},
		{"MEDIA_FFMPEG", &c.Media.FFmpeg},
		{"MEDIA_MAX_BYTES", &c.Media.MaxBytes},
		{"MEDIA_MAX_DURATION", &c.Media.MaxDuration},
		{"MEDIA_TIMEOUT", &c.Media.Timeout},
		{"MEDIA_WORKERS", &c.Media.Workers},
		{"RUNTIME_QUEUE_CAPACITY", &c.Runtime.QueueCapacity},
		{"RUNTIME_OPERATION_TIMEOUT", &c.Runtime.OperationTimeout},
		{"RUNTIME_MAX_ATTEMPTS", &c.Runtime.MaxAttempts},
		{"RUNTIME_RETRY_BASE", &c.Runtime.RetryBase},
		{"RUNTIME_RETRY_MAX", &c.Runtime.RetryMax},
		{"RUNTIME_SHUTDOWN_TIMEOUT", &c.Runtime.ShutdownTimeout},
	} {
		value, present := lookup("WHATSGRAM_" + field.name)
		if present {
			if err := setEnv(field.target, value); err != nil {
				return errors.New("invalid environment field WHATSGRAM_" + field.name)
			}
		}
	}
	return nil
}

func setEnv(target interface{}, value string) error {
	var err error
	switch target := target.(type) {
	case *string:
		*target = value
	case *int64:
		*target, err = strconv.ParseInt(value, 10, 64)
	case *int:
		*target, err = strconv.Atoi(value)
	case *bool:
		*target, err = strconv.ParseBool(value)
	case *time.Duration:
		*target, err = time.ParseDuration(value)
	case *[]int64:
		ids := make([]int64, 0)
		for _, part := range strings.Split(value, ",") {
			id, parseErr := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if parseErr != nil {
				return parseErr
			}
			ids = append(ids, id)
		}
		*target = ids
	}
	return err
}
