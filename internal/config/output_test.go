package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestOutputRedactsSecrets(t *testing.T) {
	c := validConfig()
	for _, output := range []string{c.String(), c.GoString(), c.Telegram.String(), c.Telegram.GoString()} {
		if strings.Contains(output, c.Telegram.Token.Reveal()) || !strings.Contains(output, "redacted") {
			t.Fatal("direct string methods must be redacted")
		}
	}
	for _, value := range []interface{}{c, &c, c.Telegram, &c.Telegram} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			output := fmt.Sprintf(format, value)
			if strings.Contains(output, c.Telegram.Token.Reveal()) || !strings.Contains(output, "redacted") {
				t.Fatal("formatted configuration must be redacted")
			}
		}
		data, err := json.Marshal(value)
		if err != nil || strings.Contains(string(data), c.Telegram.Token.Reveal()) {
			t.Fatal("JSON must not expose token")
		}
	}
	c.Database.Path = c.Telegram.Token.Reveal()
	c.Telegram.Endpoint = "https://" + c.Telegram.Token.Reveal() + ".example"
	data, err := json.Marshal(c)
	if err != nil || strings.Contains(string(data), c.Telegram.Token.Reveal()) {
		t.Fatal("JSON configuration must redact even repeated secrets")
	}
}
