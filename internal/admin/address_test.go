package admin

import "testing"

func TestAddressedUsernameUsesLiteralASCIICaseMatching(t *testing.T) {
	for _, tc := range []struct{ username, foreign string }{
		{"K_Bot", "K_Bot"}, {"S_Bot", "ſ_Bot"},
	} {
		p, err := NewPolicy(-100123, []int64{42}, tc.username)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.Decide(commandInput("/help@" + tc.foreign)); got.Kind != Reject {
			t.Fatalf("non-ASCII username must not match trusted bot: %v", got)
		}
	}
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	for _, username := range []string{"@BridgeBot", "Bridge/Bot", "9BridgeBot", "Bot", "BridgeBot;"} {
		if got := p.Decide(commandInput("/help@" + username)); got.Kind != Reject {
			t.Fatalf("malformed username: %v", got)
		}
	}
}
