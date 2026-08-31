package httpapi

import "github.com/darky/whatsgram/internal/config"

func configForTest(secret string) config.Config { return config.Config{WhatsAppAppSecret: secret} }
