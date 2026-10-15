package config

import (
	"os"
	"strings"
)

func Load(path string, lookup func(string) (string, bool)) (Config, error) {
	c := Default()
	var envMap map[string]string
	if path != "" {
		parsed, err := parseDotEnvFile(path)
		if err != nil {
			return Config{}, err
		}
		envMap = parsed
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	overlayLookup := func(key string) (string, bool) {
		if val, ok := lookup(key); ok {
			return val, true
		}
		if envMap != nil {
			if val, ok := envMap[key]; ok {
				return val, true
			}
		}
		return "", false
	}
	if err := c.applyEnv(overlayLookup); err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	c.Telegram.Endpoint = strings.TrimRight(c.Telegram.Endpoint, "/")
	seen := make(map[int64]bool, len(c.Telegram.AllowedUserIDs))
	ids := make([]int64, 0, len(c.Telegram.AllowedUserIDs))
	for _, id := range c.Telegram.AllowedUserIDs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	c.Telegram.AllowedUserIDs = ids
	return c, nil
}
