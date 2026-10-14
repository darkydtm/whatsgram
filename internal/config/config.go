package config

import "time"

type Config struct {
	Telegram Telegram `yaml:"telegram"`
	WhatsApp WhatsApp `yaml:"whatsapp"`
	Database Database `yaml:"database"`
	Media    Media    `yaml:"media"`
	Runtime  Runtime  `yaml:"runtime"`
}

type Telegram struct {
	Token          string  `yaml:"token" json:"-"`
	ChatID         int64   `yaml:"chat_id"`
	AllowedUserIDs []int64 `yaml:"allowed_user_ids"`
	Endpoint       string  `yaml:"endpoint"`
	LocalAPI       bool    `yaml:"local_api"`
}

type WhatsApp struct {
	PairingMode   string `yaml:"pairing_mode"`
	Phone         string `yaml:"phone"`
	HistoryPolicy string `yaml:"history_policy"`
}

type Database struct {
	Path        string        `yaml:"path"`
	BusyTimeout time.Duration `yaml:"busy_timeout"`
}

type Media struct {
	TempDir     string        `yaml:"temp_dir"`
	FFmpeg      string        `yaml:"ffmpeg"`
	MaxBytes    int64         `yaml:"max_bytes"`
	MaxDuration time.Duration `yaml:"max_duration"`
	Timeout     time.Duration `yaml:"timeout"`
	Workers     int           `yaml:"workers"`
}

type Runtime struct {
	QueueCapacity    int           `yaml:"queue_capacity"`
	OperationTimeout time.Duration `yaml:"operation_timeout"`
	MaxAttempts      int           `yaml:"max_attempts"`
	RetryBase        time.Duration `yaml:"retry_base"`
	RetryMax         time.Duration `yaml:"retry_max"`
	ShutdownTimeout  time.Duration `yaml:"shutdown_timeout"`
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
