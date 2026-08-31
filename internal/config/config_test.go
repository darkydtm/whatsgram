package config

import "testing"

func TestLoadRequiresDatabase(t *testing.T) {
	for _, key := range []string{"DATABASE_URL", "WHATSAPP_VERIFY_TOKEN", "WHATSAPP_APP_SECRET", "WHATSAPP_ACCESS_TOKEN", "WHATSAPP_PHONE_NUMBER_ID", "TELEGRAM_BOT_TOKEN", "TELEGRAM_WEBHOOK_SECRET", "TELEGRAM_GROUP_ID", "TELEGRAM_ALLOWED_USER_IDS"} {
		t.Setenv(key, "")
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected missing configuration error")
	}
}
