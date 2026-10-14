package admin

import (
	"strings"
	"testing"
)

func TestRetryRequiresExplicitDuplicateRiskConfirmation(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	for _, id := range []string{"operation-123", "A_1", strings.Repeat("a", 128)} {
		got := p.Decide(commandInput("/retry " + id + " confirm"))
		if got.Kind != Command || got.Command.Name != "retry" || !got.Command.ConfirmDuplicateRisk || got.Command.OperationID.Reveal() != id {
			t.Fatalf("retry intent: %v", got)
		}
		assertPrivateFormatting(t, id, got.Command, got)
	}
	for _, text := range []string{"/retry", "/retry operation-123", "/retry operation-123 CONFIRM", "/retry operation-123 confirm extra", "/retry ../path confirm", "/retry -id confirm", "/retry id;cmd confirm", "/retry " + strings.Repeat("a", 129) + " confirm"} {
		if got := p.Decide(commandInput(text)); got.Kind != Reject {
			t.Fatalf("unsafe retry: %v", got)
		}
	}
}
