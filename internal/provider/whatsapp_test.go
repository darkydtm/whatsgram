package provider

import "testing"

func TestParseJID(t *testing.T) {
	if _, err := parseJID("123456789@s.whatsapp.net"); err != nil {
		t.Fatalf("expected valid JID: %v", err)
	}
	if _, err := parseJID("not-a-jid"); err == nil {
		t.Fatal("expected invalid JID error")
	}
}
