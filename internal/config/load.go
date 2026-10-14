package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"

	"go.yaml.in/yaml/v3"
)

func Load(path string, lookup func(string) (string, bool)) (Config, error) {
	c := Default()
	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return Config{}, errors.New("cannot read configuration")
		}
		data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
		file.Close()
		if err != nil {
			return Config{}, errors.New("cannot read configuration")
		}
		if len(data) > 1024*1024 {
			return Config{}, errors.New("configuration exceeds size limit")
		}
		var document yaml.Node
		if err := yaml.Unmarshal(data, &document); err != nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return Config{}, errors.New("invalid configuration YAML")
		}
		pending := []*yaml.Node{&document}
		for len(pending) > 0 {
			node := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if node.Tag == "!!null" {
				return Config{}, errors.New("invalid null configuration value")
			}
			pending = append(pending, node.Content...)
		}
		if !validYAMLIntegers(document.Content[0]) {
			return Config{}, errors.New("invalid configuration YAML integer")
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&c); err != nil {
			return Config{}, errors.New("invalid configuration YAML")
		}
		var extra yaml.Node
		if err := decoder.Decode(&extra); err != io.EOF {
			return Config{}, errors.New("invalid trailing configuration YAML")
		}
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if err := c.applyEnv(lookup); err != nil {
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
