package config

import (
	"reflect"
	"testing"
)

func TestSecretTrustedAccess(t *testing.T) {
	var empty Secret
	if empty.Reveal() != "" || NewSecret("").Reveal() != "" {
		t.Fatal("zero and empty secrets must reveal an empty string")
	}
	canary := "synthetic-accessor-canary-1ab2496e"
	secret := NewSecret(canary)
	copy := secret
	if secret.Reveal() != canary || copy.Reveal() != canary {
		t.Fatal("trusted accessor must preserve exact token bytes across copies")
	}
	for _, data := range []string{"telegram:\n  token: " + canary + "\n", "telegram:\n  token: &token " + canary + "\n  <<: {token: *token}\n"} {
		values := accessEnv()
		delete(values, "WHATSGRAM_TELEGRAM_TOKEN")
		c, err := Load(writeYAML(t, data), mapLookup(values))
		if err != nil || c.Telegram.Token.Reveal() != canary {
			t.Fatal("YAML must populate the opaque token without changing its bytes")
		}
		values["WHATSGRAM_TELEGRAM_TOKEN"] = " \t" + canary + " "
		c, err = Load(writeYAML(t, data), mapLookup(values))
		if err != nil || c.Telegram.Token.Reveal() != values["WHATSGRAM_TELEGRAM_TOKEN"] {
			t.Fatal("environment must override YAML with exact token bytes")
		}
	}
}

func TestLoadRequiresStringTokenSource(t *testing.T) {
	for _, value := range []string{"123", "true", "{value: synthetic-token}", "[synthetic-token]"} {
		c, err := Load(writeYAML(t, "telegram:\n  token: "+value+"\n"), mapLookup(accessEnv()))
		if err == nil || !reflect.DeepEqual(c, Config{}) {
			t.Fatal("token source must be a YAML string even when replaced by environment")
		}
	}
}
