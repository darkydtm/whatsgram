package admin

import "testing"

func TestWhatsAppSlashTextIsData(t *testing.T) {
	policy, _ := NewPolicy(-100123, []int64{7}, "BridgeBot")
	for _, text := range []string{"/del", "/pair code 15551234567", "ordinary", ""} {
		got := policy.Decide(Input{Source: WhatsApp, Text: text, BotCommand: true, SenderChat: true})
		if got.Kind != Bridge {
			t.Fatalf("WhatsApp data must not execute or require Telegram identity: %v", got)
		}
	}
}

func TestZeroPolicyFailsClosed(t *testing.T) {
	var policy Policy
	if got := policy.Decide(Input{Source: Telegram, HasUser: true, UserID: 7}); got.Kind != Reject {
		t.Fatalf("zero policy: %v", got)
	}
}

func TestAdmissionRequiresExactTelegramChatAndNumericActor(t *testing.T) {
	policy, err := NewPolicy(-100123, []int64{42, 7}, "BridgeBot")
	if err != nil {
		t.Fatal(err)
	}
	valid := Input{Source: Telegram, ChatID: -100123, UserID: 42, HasUser: true, Text: "ordinary text"}
	if got := policy.Decide(valid); got.Kind != Bridge {
		t.Fatalf("authorized message: %v", got)
	}
	cases := []struct {
		name   string
		change func(*Input)
	}{
		{"wrong chat", func(in *Input) { in.ChatID = -100124 }},
		{"private chat", func(in *Input) { in.ChatID = 42 }},
		{"unauthorized actor", func(in *Input) { in.UserID = 43 }},
		{"missing actor", func(in *Input) { in.HasUser = false }},
		{"anonymous actor", func(in *Input) { in.SenderChat = true }},
		{"zero actor", func(in *Input) { in.UserID = 0 }},
		{"negative actor", func(in *Input) { in.UserID = -42 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := valid
			tc.change(&in)
			if got := policy.Decide(in); got.Kind != Reject || got.Code == "" || got.Reason == "" {
				t.Fatalf("expected fixed rejection: %v", got)
			}
		})
	}
	for _, source := range []string{"", "Telegram", "other"} {
		in := valid
		in.Source = source
		if got := policy.Decide(in); got.Kind != Ignore {
			t.Fatalf("unknown source: %v", got)
		}
	}
}
