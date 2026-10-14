package admin

import (
	"fmt"
	"strings"
	"testing"
)

func TestPolicyRejectsInvalidChatAllowlistAndTrustedUsername(t *testing.T) {
	for _, tc := range []struct {
		chat     int64
		allowed  []int64
		username string
	}{
		{0, []int64{7}, "BridgeBot"}, {7, []int64{7}, "BridgeBot"},
		{-100123, nil, "BridgeBot"}, {-100123, []int64{0}, "BridgeBot"},
		{-100123, []int64{7, -1}, "BridgeBot"},
		{-100123, []int64{7}, ""}, {-100123, []int64{7}, "@BridgeBot"},
		{-100123, []int64{7}, " BridgeBot"}, {-100123, []int64{7}, "BridgeBot "},
		{-100123, []int64{7}, "Bot"}, {-100123, []int64{7}, "9BridgeBot"},
		{-100123, []int64{7}, "Bridge/Bot"}, {-100123, []int64{7}, strings.Repeat("a", 33)},
	} {
		p, err := NewPolicy(tc.chat, tc.allowed, tc.username)
		if err == nil || err.Error() != "invalid admin policy" || p.DriverID() != 0 {
			t.Fatalf("invalid policy accepted or unsafe error: %v", err)
		}
		for _, format := range []string{"%v", "%+v", "%#v", "%q", "%z"} {
			if strings.Contains(fmt.Sprintf(format, err), "BridgeBot") {
				t.Fatal("constructor error leaks input")
			}
		}
	}
	for _, username := range []string{"A_bot", "BridgeBot", strings.Repeat("a", 32)} {
		if _, err := NewPolicy(-100123, []int64{7, 7}, username); err != nil {
			t.Fatal(err)
		}
	}
}
