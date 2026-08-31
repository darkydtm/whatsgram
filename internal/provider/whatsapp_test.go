package provider

import (
	"encoding/json"
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
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

func TestChatKeyDropsDevice(t *testing.T) {
	jid, err := parseJID("123456789:2@s.whatsapp.net")
	if err != nil {
		t.Fatal(err)
	}
	if got := chatKey(jid); got != "123456789@s.whatsapp.net" {
		t.Fatalf("chat key = %q", got)
	}
}

func TestMessageBody(t *testing.T) {
	if got := messageBody(&waE2E.Message{Conversation: proto.String("hello")}); got != "hello" {
		t.Fatalf("message body = %q, want hello", got)
	}
	if got := messageBody(&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("hello")}}); got != "hello" {
		t.Fatalf("extended message body = %q, want hello", got)
	}
	if got := messageBody(&waE2E.Message{DeviceSentMessage: &waE2E.DeviceSentMessage{
		Message: &waE2E.Message{Conversation: proto.String("hello")},
	}}); got != "hello" {
		t.Fatalf("nested message body = %q, want hello", got)
	}
}

func TestJSONMessageKeepsParsedMessageWithoutRawMessage(t *testing.T) {
	event := &events.Message{
		Info:    types.MessageInfo{ID: "message-id", PushName: "Alice"},
		Message: &waE2E.Message{Conversation: proto.String("hello")},
	}
	payload, err := jsonMessage(event)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name string `json:"name"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Alice" || got.Body != "hello" {
		t.Fatalf("payload = %+v", got)
	}
}
