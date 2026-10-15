package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestOutputInvalidVerbsHideTokenBytes(t *testing.T) {
	canary := "synthetic-fmt-canary-3f8d6b920ca1"
	c := validConfig()
	c.Telegram.Token = NewSecret(canary)
	direct := []interface{}{c, &c, c.Telegram, &c.Telegram, c.Telegram.Token, &c.Telegram.Token, []Config{c}, map[string]Config{"config": c}, struct{ Config Config }{c}, []Telegram{c.Telegram}, []Secret{c.Telegram.Token}}
	for _, value := range direct {
		for _, nested := range []interface{}{
			value,
			[]interface{}{value},
			map[string]interface{}{"config": value},
			struct{ Value interface{} }{value},
			&struct{ Value interface{} }{value},
			[]interface{}{map[string]interface{}{"nested": []interface{}{value}}},
		} {
			for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%w", "%+w", "%#w", "%d", "%p", "%z", "%*v", "%.*v", ""} {
				outputs := []string{fmt.Sprintf(format, nested), fmt.Errorf(format, nested).Error()}
				for i, output := range outputs {
					if bytes.Contains([]byte(output), []byte(canary)) {
						t.Errorf("token bytes leaked: type=%T format=%q function=%d", nested, format, i)
					}
				}
			}
			data, err := json.Marshal(nested)
			if err != nil || bytes.Contains(data, []byte(canary)) {
				t.Error("JSON exposed token bytes or failed")
			}
		}
	}
}

func TestLoadMalformedEnvHidesTokenBytes(t *testing.T) {
	canary := "synthetic-parser-canary-b9f87126d0a4"
	for _, data := range []string{
		"WHATSGRAM_TELEGRAM_TOKEN\n",
		"WHATSGRAM_TELEGRAM_TOKEN=\"" + canary + "\n",
		"WHATSGRAM_TELEGRAM_TOKEN='" + canary + "\n",
		"WHATSGRAM_TELEGRAM_TOKEN=\"" + canary + "\"extra\n",
		"WHATSGRAM_TELEGRAM_TOKEN='" + canary + "'extra\n",
		"=value\n",
		canary + "\n",
	} {
		c, err := Load(writeEnv(t, data), mapLookup(accessEnv()))
		if err == nil || !reflect.DeepEqual(c, Config{}) || bytes.Contains([]byte(err.Error()), []byte(canary)) {
			t.Fatal("malformed .env must return zero config without token bytes")
		}
	}
}
