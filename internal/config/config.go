package config

import "time"

type Config struct {
	Telegram Telegram
	WhatsApp WhatsApp
	Database Database
	Media    Media
	Runtime  Runtime
}

type Telegram struct {
	Token          Secret `json:"-"`
	ChatID         int64
	AllowedUserIDs []int64
	Endpoint       string
	LocalAPI       bool
}

type WhatsApp struct {
	PairingMode   string
	Phone         string
	HistoryPolicy string
}

type Database struct {
	Path        string
	BusyTimeout time.Duration
}

type Media struct {
	TempDir     string
	FFmpeg      string
	MaxBytes    int64
	MaxDuration time.Duration
	Timeout     time.Duration
	Workers     int
}

type Runtime struct {
	QueueCapacity    int
	OperationTimeout time.Duration
	MaxAttempts      int
	RetryBase        time.Duration
	RetryMax         time.Duration
	ShutdownTimeout  time.Duration
}

func Default() Config {
	return Config{
		Telegram: Telegram{Endpoint: "https://api.telegram.org"},
		WhatsApp: WhatsApp{PairingMode: "qr", HistoryPolicy: "none"},
		Database: Database{Path: "data/whatsgram.db", BusyTimeout: 5 * time.Second},
		Media:    Media{FFmpeg: "ffmpeg", MaxBytes: 50 * 1024 * 1024, MaxDuration: 10 * time.Minute, Timeout: 2 * time.Minute, Workers: 2},
		Runtime:  Runtime{QueueCapacity: 256, OperationTimeout: 30 * time.Second, MaxAttempts: 5, RetryBase: time.Second, RetryMax: time.Minute, ShutdownTimeout: 15 * time.Second},
	}
}
