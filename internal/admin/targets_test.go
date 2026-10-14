package admin

import "testing"

func TestReplyCommandsRequirePositiveTarget(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	for _, name := range []string{"del", "pin", "read", "unpin"} {
		in := commandInput("/" + name)
		in.ReplyMessageID = 123
		got := p.Decide(in)
		if got.Kind != Command || got.Command.Name != name || got.Command.TargetMessageID != 123 {
			t.Fatalf("reply target: %v", got)
		}
		for _, reply := range []int64{0, -1} {
			in.ReplyMessageID = reply
			if got := p.Decide(in); got.Kind != Reject {
				t.Fatalf("missing reply: %v", got)
			}
		}
		in.Text += " extra"
		in.ReplyMessageID = 123
		if got := p.Decide(in); got.Kind != Reject {
			t.Fatalf("unexpected reply arguments: %v", got)
		}
	}
	for _, target := range []string{"1", "2147483647"} {
		got := p.Decide(commandInput("/unpin " + target))
		if got.Kind != Command || got.Command.TargetMessageID <= 0 {
			t.Fatalf("explicit unpin: %v", got)
		}
	}
	for _, target := range []string{"0", "-1", "+1", "01", "2147483648", "9223372036854775808", "1 extra", "١"} {
		if got := p.Decide(commandInput("/unpin " + target)); got.Kind != Reject {
			t.Fatalf("invalid target: %v", got)
		}
	}
	in := commandInput("/unpin 123")
	in.ReplyMessageID = 123
	if got := p.Decide(in); got.Kind != Reject {
		t.Fatalf("ambiguous explicit plus reply: %v", got)
	}
}
