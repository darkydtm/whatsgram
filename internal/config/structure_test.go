package config

import (
	"strings"
	"testing"
)

func TestLoadRejectsUnsafeYAMLStructures(t *testing.T) {
	for _, tt := range []struct{ name, data string }{
		{"sequence root", "[synthetic-secret]\n"},
		{"alias cycle", accessYAML + "media: &cycle\n  ffmpeg: *cycle\n"},
		{"alias unknown field", accessYAML + "database: &defaults\n  path: data/ok.db\n  synthetic-secret: true\nmedia:\n  <<: *defaults\n"},
		{"merge unknown field", accessYAML + "media:\n  <<: {synthetic-secret: true}\n"},
		{"duplicate merged field", accessYAML + "media:\n  <<: {workers: 2, workers: 3}\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeYAML(t, tt.data), noEnv)
			if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("unsafe YAML structure accepted or source exposed")
			}
		})
	}
}
