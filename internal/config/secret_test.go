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
	for _, data := range []string{
		"WHATSGRAM_TELEGRAM_TOKEN=" + canary + "\n",
		"export WHATSGRAM_TELEGRAM_TOKEN='" + canary + "'\n",
	} {
		values := accessEnv()
		delete(values, "WHATSGRAM_TELEGRAM_TOKEN")
		c, err := Load(writeEnv(t, data), mapLookup(values))
		if err != nil || c.Telegram.Token.Reveal() != canary {
			t.Fatal(".env must populate the opaque token without changing its bytes")
		}
		values["WHATSGRAM_TELEGRAM_TOKEN"] = " \t" + canary + " "
		c, err = Load(writeEnv(t, data), mapLookup(values))
		if err != nil || c.Telegram.Token.Reveal() != values["WHATSGRAM_TELEGRAM_TOKEN"] {
			t.Fatal("environment must override .env with exact token bytes")
		}
	}
}

func TestLoadRequiresValidTokenSource(t *testing.T) {
	for _, value := range []string{"=synthetic-token", "INVALID_LINE"} {
		c, err := Load(writeEnv(t, value+"\n"), mapLookup(accessEnv()))
		if err == nil || !reflect.DeepEqual(c, Config{}) {
			t.Fatal("invalid .env line must fail without returning partial config")
		}
	}
}
