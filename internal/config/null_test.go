package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoadRejectsNullValues(t *testing.T) {
	for _, data := range []string{
		accessYAML + "media:\n  ffmpeg: null\n",
		accessYAML + "runtime:\n  retry_base: null\n",
		accessYAML + "database: null\n",
		accessYAML + "whatsapp:\n  history_policy: null\n",
		"null\n",
		"\n# empty YAML\n",
	} {
		c, err := Load(writeYAML(t, data), mapLookup(accessEnv()))
		if err == nil || !reflect.DeepEqual(c, Config{}) || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatal("explicit null or empty YAML must not silently inherit defaults")
		}
	}
}
