package config

import (
	"errors"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

func Load(path string, lookup func(string) (string, bool)) (Config, error) {
	c := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, errors.New("cannot read configuration")
		}
		if err := yaml.Unmarshal(data, &c); err != nil {
			return Config{}, errors.New("invalid configuration YAML")
		}
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
