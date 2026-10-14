package admin

import "testing"

func TestAccountActionsRequireAdmissionAndDriver(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{7, 42}, "BridgeBot")
	valid := Input{Source: Telegram, ChatID: -100123, UserID: 7, HasUser: true}
	if got := p.DecideAccountAction(valid); got.Kind != Bridge {
		t.Fatalf("driver action: %v", got)
	}
	for _, in := range []Input{
		{Source: WhatsApp, UserID: 7, HasUser: true},
		{Source: Telegram, ChatID: -100124, UserID: 7, HasUser: true},
		{Source: Telegram, ChatID: -100123, UserID: 7, HasUser: true, SenderChat: true},
		{Source: Telegram, ChatID: -100123, UserID: 7},
		{Source: Telegram, ChatID: -100123, UserID: 8, HasUser: true},
		{Source: Telegram, ChatID: -100123, UserID: 42, HasUser: true},
	} {
		if got := p.DecideAccountAction(in); got.Kind != Reject {
			t.Fatalf("reaction/vote intent must visibly reject: %v", got)
		}
	}
}

func TestMutationDecisionRequiresAdmissionBeforeOwnership(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{7, 42}, "BridgeBot")
	in := Input{Source: Telegram, ChatID: -100123, UserID: 42, HasUser: true}
	if got := p.DecideMutation(in, 42, false); got.Kind != Bridge {
		t.Fatalf("owner edit: %v", got)
	}
	if got := p.DecideMutation(in, 7, false); got.Kind != Reject {
		t.Fatalf("other author: %v", got)
	}
	if got := p.DecideMutation(in, 7, true); got.Kind != Bridge {
		t.Fatalf("audited override: %v", got)
	}
	for _, change := range []func(*Input){
		func(in *Input) { in.Source = WhatsApp },
		func(in *Input) { in.ChatID = 42 },
		func(in *Input) { in.UserID = 8 },
		func(in *Input) { in.HasUser = false },
		func(in *Input) { in.SenderChat = true },
	} {
		bad := in
		change(&bad)
		if got := p.DecideMutation(bad, 42, true); got.Kind != Reject {
			t.Fatalf("unauthorized edit bypass: %v", got)
		}
	}
}
