package provider

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestParseJID(t *testing.T) {
	if _, err := parseJID("123456789@s.whatsapp.net"); err != nil {
		t.Fatalf("expected valid JID: %v", err)
	}
	if _, err := parseJID("not-a-jid"); err == nil {
		t.Fatal("expected invalid JID error")
	}
}

func TestMessageBody(t *testing.T) {
	if got := messageBody(&waE2E.Message{Conversation: proto.String("hello")}); got != "hello" {
		t.Fatalf("message body = %q, want hello", got)
	}
	if got := messageBody(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("hello")}}); got != "hello" {
		t.Fatalf("extended message body = %q, want hello", got)
	}
}
