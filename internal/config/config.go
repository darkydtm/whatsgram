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

	WhatsAppVerifyToken   string
	WhatsAppAppSecret     string
	WhatsAppAccessToken   string
	WhatsAppPhoneNumberID string
	WhatsAppAPIVersion    string

	TelegramBotToken       string
	TelegramWebhookSecret  string
	TelegramGroupID        int64
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
	if config.WhatsAppVerifyToken, err = get("WHATSAPP_VERIFY_TOKEN"); err != nil {
		return Config{}, err
	}
	if config.WhatsAppAppSecret, err = get("WHATSAPP_APP_SECRET"); err != nil {
		return Config{}, err
	}
	if config.WhatsAppAccessToken, err = get("WHATSAPP_ACCESS_TOKEN"); err != nil {
		return Config{}, err
	}
	if config.WhatsAppPhoneNumberID, err = get("WHATSAPP_PHONE_NUMBER_ID"); err != nil {
		return Config{}, err
	}
	config.WhatsAppAPIVersion = os.Getenv("WHATSAPP_API_VERSION")
	if config.WhatsAppAPIVersion == "" {
		config.WhatsAppAPIVersion = "v21.0"
	}
	if config.TelegramBotToken, err = get("TELEGRAM_BOT_TOKEN"); err != nil {
		return Config{}, err
	}
	if config.TelegramWebhookSecret, err = get("TELEGRAM_WEBHOOK_SECRET"); err != nil {
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
