package admin

import (
	"strings"
	"testing"
)

func TestBindTopicAcceptsLimitedCanonicalJIDAndPositiveTopic(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	for _, jid := range []string{"15551234567@s.whatsapp.net", "1234567@s.whatsapp.net", "123456789012345@s.whatsapp.net", "1234567890@g.us", "15551234567-1234567890@g.us", "12345678901234567890@lid"} {
		got := p.Decide(commandInput("/bind topic " + jid + " 123"))
		if got.Kind != Command || got.Command.Name != "bind" || got.Command.Mode != "topic" || got.Command.Argument.Reveal() != jid || got.Command.ThreadID != 123 {
			t.Fatalf("topic binding: %v", got)
		}
		assertPrivateFormatting(t, jid, got.Command, got)
	}
	for _, jid := range []string{"+15551234567@s.whatsapp.net", "05551234567@s.whatsapp.net", "123456@s.whatsapp.net", "15551234567:2@s.whatsapp.net", "15551234567@S.WHATSAPP.NET", "1234567890@broadcast", "1234567890@newsletter", "123@g.us@evil", "123-456-789@g.us", "0@lid", strings.Repeat("1", 21) + "@lid", "../path"} {
		if got := p.Decide(commandInput("/bind topic " + jid + " 123")); got.Kind != Reject {
			t.Fatalf("invalid JID: %v", got)
		}
	}
	for _, id := range []string{"0", "-1", "+1", "01", "2147483648", "123 extra"} {
		if got := p.Decide(commandInput("/bind topic 15551234567@s.whatsapp.net " + id)); got.Kind != Reject {
			t.Fatalf("invalid topic: %v", got)
		}
	}
}

func TestBindMessageAcceptsOperationAndNativeTelegramMessageID(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	got := p.Decide(commandInput("/bind message operation-123 456"))
	if got.Kind != Command || got.Command.Mode != "message" || got.Command.OperationID.Reveal() != "operation-123" || got.Command.TargetMessageID != 456 {
		t.Fatalf("message binding: %v", got)
	}
	for _, text := range []string{"/bind", "/bind message", "/bind message operation-123", "/bind message operation-123 0", "/bind message ../path 123", "/bind message operation-123 ABCDEF", "/bind message operation-123 2147483648", "/bind unknown operation-123 123", "/bind topic 15551234567@s.whatsapp.net", "/bind message operation-123 123 extra"} {
		if got := p.Decide(commandInput(text)); got.Kind != Reject {
			t.Fatalf("invalid binding: %v", got)
		}
	}
}
