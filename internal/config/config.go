package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL string
	HTTPAddr    string

	TelegramBotToken       string
	TelegramWebhookSecret  string
	TelegramWebhookURL     string
	TelegramGroupID        int64
	TelegramSystemThreadID int64
	TelegramAllowedUserIDs map[int64]bool
}

func Load() (Config, error) {
	config := Config{
		HTTPAddr:               os.Getenv("HTTP_ADDR"),
		TelegramAllowedUserIDs: make(map[int64]bool),
	}
	if config.HTTPAddr == "" {
		config.HTTPAddr = ":8080"
	}

	get := required
	var err error
	if config.DatabaseURL, err = get("DATABASE_URL"); err != nil {
		return Config{}, err
	}
	if config.TelegramBotToken, err = get("TELEGRAM_BOT_TOKEN"); err != nil {
		return Config{}, err
	}
	if config.TelegramWebhookSecret, err = get("TELEGRAM_WEBHOOK_SECRET"); err != nil {
		return Config{}, err
	}
	if config.TelegramWebhookURL, err = get("TELEGRAM_WEBHOOK_URL"); err != nil {
		return Config{}, err
	}

	groupID, err := get("TELEGRAM_GROUP_ID")
	if err != nil {
		return Config{}, err
	}
	config.TelegramGroupID, err = strconv.ParseInt(groupID, 10, 64)
	if err != nil {
		return Config{}, fmt.Errorf("TELEGRAM_GROUP_ID: %w", err)
	}
	if raw := os.Getenv("TELEGRAM_SYSTEM_THREAD_ID"); raw != "" {
		config.TelegramSystemThreadID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("TELEGRAM_SYSTEM_THREAD_ID: %w", err)
		}
	}

	allowedUsers, err := get("TELEGRAM_ALLOWED_USER_IDS")
	if err != nil {
		return Config{}, err
	}
	for _, rawID := range strings.Split(allowedUsers, ",") {
		id, parseErr := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		if parseErr != nil {
			return Config{}, fmt.Errorf("TELEGRAM_ALLOWED_USER_IDS: %w", parseErr)
		}
		config.TelegramAllowedUserIDs[id] = true
	}

	return config, nil
}

func required(key string) (string, error) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}
