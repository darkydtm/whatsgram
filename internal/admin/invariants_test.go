package admin

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestInvalidCommandsAndAuthorizationDiagnosticsNeverContainPrivateInputs(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	private := "private-marker-15551234567"
	for _, text := range []string{"/" + private, "/pair code " + private, "/rename " + private, "/bind topic " + private + " 1", "/retry " + private, "/help@OtherBot " + private} {
		for _, actor := range []int64{42, 8} {
			in := commandInput(text)
			in.UserID = actor
			got := p.Decide(in)
			if got.Kind != Reject && got.Kind != Ignore {
				t.Fatal("expected rejection or ignore")
			}
			assertPrivateFormatting(t, private, got, &got, got.Command, &got.Command)
		}
	}
	in := commandInput("/pair code 15551234567")
	in.ThreadID = 123
	got := p.Decide(in)
	if got.Command.Argument.String() != "PrivateValue[redacted]" || got.Command.Argument.GoString() != "PrivateValue[redacted]" {
		t.Fatal("private string representation")
	}
	var value PrivateValue
	assertPrivateFormatting(t, private, value, &value)
	if output := fmt.Sprint(got.Code, got.Reason); strings.Contains(output, "15551234567") {
		t.Fatal("diagnostic leaks phone")
	}
}

func TestEveryCommandFamilyRequiresExactAdmission(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42, 7}, "BridgeBot")
	commands := []string{"/help", "/status", "/sync", "/pair qr", "/pair code 15551234567", "/del", "/pin", "/unpin", "/timer off", "/rename alias", "/rename-wa alias", "/read", "/retry operation-123 confirm", "/bind topic 15551234567@s.whatsapp.net 123", "/bind message operation-123 123"}
	for _, text := range commands {
		in := commandInput(text)
		in.ThreadID = 123
		in.ReplyMessageID = 456
		if got := p.Decide(in); got.Kind != Command {
			t.Fatalf("valid command family %q: %v", text, got)
		}
		for _, change := range []func(*Input){
			func(in *Input) { in.ChatID = -100124 },
			func(in *Input) { in.ChatID = 42 },
			func(in *Input) { in.UserID = 8 },
			func(in *Input) { in.UserID = 0 },
			func(in *Input) { in.HasUser = false },
			func(in *Input) { in.SenderChat = true },
		} {
			bad := in
			change(&bad)
			if got := p.Decide(bad); got.Kind != Reject || got.Command.Name != "" {
				t.Fatalf("admission bypass %q: %v", text, got)
			}
		}
		in.Source = WhatsApp
		if got := p.Decide(in); got.Kind != Bridge || got.Command.Name != "" {
			t.Fatalf("WhatsApp executed command %q: %v", text, got)
		}
	}
}

func TestPolicySupportsConcurrentReadOnlyDecisions(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42, 7}, "BridgeBot")
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if got := p.Decide(commandInput("/pair code 15551234567")); got.Kind != Command || got.Command.Argument.Reveal() != "15551234567" {
					t.Error("concurrent command decision")
				}
				if !p.CanDriveAccount(7) || !p.CanMutateOriginal(42, 42, false) {
					t.Error("concurrent policy decision")
				}
			}
		}()
	}
	wg.Wait()
}

func FuzzCommandNeverForwarded(f *testing.F) {
	for _, text := range []string{"", "help", "pair code 15551234567", "rename private alias", "help@OtherBot", "/", "\xff", "\nstatus", "retry operation-123 confirm"} {
		f.Add(text)
	}
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	f.Fuzz(func(t *testing.T, text string) {
		in := commandInput("/" + text)
		in.ThreadID = 123
		in.ReplyMessageID = 456
		if got := p.Decide(in); got.Kind == Bridge {
			t.Fatal("slash input forwarded")
		}
		in.Source = WhatsApp
		if got := p.Decide(in); got.Kind != Bridge || got.Command.Name != "" {
			t.Fatal("WhatsApp must remain data")
		}
	})
}
