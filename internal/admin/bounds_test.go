package admin

import (
	"strings"
	"testing"
)

func TestCommandInputIsBoundedAndRejectsInvalidUTF8(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	for _, text := range []string{"/help" + strings.Repeat(" ", 4096), "/rename name" + string([]byte{0xff}), "/help@" + string([]byte{0xff})} {
		in := commandInput(text)
		in.ThreadID = 123
		if got := p.Decide(in); got.Kind != Reject {
			t.Fatalf("invalid or oversize command should reject: %v", got)
		}
	}
	if got := p.Decide(commandInput("/help" + strings.Repeat(" ", 4091))); got.Kind != Command {
		t.Fatalf("command byte boundary: %v", got)
	}
}

func TestNormalizedReplyAndTopicContextStayInTelegramIDBounds(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	for _, text := range []string{"/del", "/read", "/pin", "/unpin"} {
		in := commandInput(text)
		in.ReplyMessageID = 2147483648
		if got := p.Decide(in); got.Kind != Reject {
			t.Fatalf("out-of-bounds reply: %v", got)
		}
		in.ReplyMessageID = 2147483647
		if got := p.Decide(in); got.Kind != Command {
			t.Fatalf("reply boundary: %v", got)
		}
	}
	for _, text := range []string{"/timer off", "/rename alias", "/rename-wa alias"} {
		in := commandInput(text)
		in.ThreadID = 2147483648
		if got := p.Decide(in); got.Kind != Reject {
			t.Fatalf("out-of-bounds topic: %v", got)
		}
		in.ThreadID = 2147483647
		if got := p.Decide(in); got.Kind != Command {
			t.Fatalf("topic boundary: %v", got)
		}
	}
}
