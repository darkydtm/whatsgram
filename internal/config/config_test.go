package config

import (
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Telegram.Token != "" || c.Telegram.ChatID != 0 || len(c.Telegram.AllowedUserIDs) != 0 || c.Telegram.Endpoint != "https://api.telegram.org" || c.Telegram.LocalAPI {
		t.Fatalf("unexpected telegram defaults")
	}
	if c.WhatsApp.PairingMode != "qr" || c.WhatsApp.Phone != "" || c.WhatsApp.HistoryPolicy != "none" {
		t.Fatalf("unexpected whatsapp defaults")
	}
	if c.Database.Path != "data/whatsgram.db" || c.Database.BusyTimeout != 5*time.Second {
		t.Fatalf("unexpected database defaults")
	}
	if c.Media.TempDir != "" || c.Media.FFmpeg != "ffmpeg" || c.Media.MaxBytes != 50*1024*1024 || c.Media.MaxDuration != 10*time.Minute || c.Media.Timeout != 2*time.Minute || c.Media.Workers != 2 {
		t.Fatalf("unexpected media defaults")
	}
	if c.Runtime.QueueCapacity != 256 || c.Runtime.OperationTimeout != 30*time.Second || c.Runtime.MaxAttempts != 5 || c.Runtime.RetryBase != time.Second || c.Runtime.RetryMax != time.Minute || c.Runtime.ShutdownTimeout != 15*time.Second {
		t.Fatalf("unexpected runtime defaults")
	}
}
