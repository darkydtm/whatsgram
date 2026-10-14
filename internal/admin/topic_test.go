package admin

import (
	"strings"
	"testing"
	"time"
)

func TestTopicTimerAcceptsOnlyFixedDurationsInTopic(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	for literal, duration := range map[string]time.Duration{"off": 0, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "90d": 90 * 24 * time.Hour} {
		in := commandInput("/timer " + literal)
		in.ThreadID = 123
		got := p.Decide(in)
		if got.Kind != Command || got.Command.Timer != duration || got.Command.ThreadID != 123 {
			t.Fatalf("timer intent: %v", got)
		}
		in.ThreadID = 0
		if got := p.Decide(in); got.Kind != Reject {
			t.Fatalf("timer requires topic context: %v", got)
		}
	}
	for _, text := range []string{"/timer", "/timer 1h", "/timer OFF", "/timer 7d extra"} {
		in := commandInput(text)
		in.ThreadID = 123
		if got := p.Decide(in); got.Kind != Reject {
			t.Fatalf("invalid timer: %v", got)
		}
	}
}

func TestTopicRenameIsPrivateAndSeparatesLocalFromWhatsApp(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	for _, name := range []string{"rename", "rename-wa"} {
		label := "Private  alias 🍀"
		in := commandInput("/" + name + "  " + label + " ")
		in.ThreadID = 123
		got := p.Decide(in)
		if got.Kind != Command || got.Command.Name != name || got.Command.Argument.Reveal() != label || got.Command.ThreadID != 123 {
			t.Fatalf("rename intent: %v", got)
		}
		assertPrivateFormatting(t, label, got.Command, &got.Command, got, &got)
		in.ThreadID = 0
		if got := p.Decide(in); got.Kind != Reject {
			t.Fatalf("rename requires topic context: %v", got)
		}
		for _, label := range []string{"", strings.Repeat("a", 129), "bad\nname", "bad\tname", "bad\x00name", "bad\u202ename", string([]byte{0xff})} {
			in = commandInput("/" + name + " " + label)
			in.ThreadID = 123
			if got := p.Decide(in); got.Kind != Reject {
				t.Fatalf("invalid label: %v", got)
			}
		}
		for _, label := range []string{"a", strings.Repeat("🍀", 128)} {
			in = commandInput("/" + name + " " + label)
			in.ThreadID = 123
			if got := p.Decide(in); got.Kind != Command {
				t.Fatalf("Unicode label boundary: %v", got)
			}
		}
	}
}
