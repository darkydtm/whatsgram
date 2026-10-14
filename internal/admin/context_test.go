package admin

import "testing"

func TestCommandsRejectNegativeNormalizedContext(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	for _, text := range []string{"/help", "/pair qr", "/timer off", "/rename alias", "/bind message operation-1 123"} {
		for _, negativeReply := range []bool{false, true} {
			in := commandInput(text)
			in.ThreadID = 123
			if negativeReply {
				in.ReplyMessageID = -1
			} else {
				in.ThreadID = -1
			}
			if got := p.Decide(in); got.Kind != Reject {
				t.Fatalf("negative normalized context: %v", got)
			}
		}
	}
}
