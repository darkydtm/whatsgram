package admin

import "testing"

func commandInput(text string) Input {
	return Input{Source: Telegram, ChatID: -100123, UserID: 42, HasUser: true, Text: text}
}

func TestSimpleCommandsAreConcreteAndNeverForwarded(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{7, 42}, "BridgeBot")
	for _, name := range []string{"help", "status", "sync"} {
		for _, text := range []string{"/" + name, "/" + name + "@bridgebot", " /" + name + "@BRIDGEBOT \t"} {
			got := p.Decide(commandInput(text))
			if got.Kind != Command || got.Command.Name != name {
				t.Fatalf("simple command %q: %v", text, got)
			}
		}
	}
	for _, text := range []string{"/unknown", "/HELP", "/", "/help extra", "/help@BridgeBot@BridgeBot", "/help@", "/help@OtherBot", "/other@OtherBot"} {
		if got := p.Decide(commandInput(text)); got.Kind != Ignore && got.Kind != Reject {
			t.Fatalf("command-like input forwarded %q: %v", text, got)
		}
	}
	in := commandInput("not a leading command")
	in.BotCommand = true
	if got := p.Decide(in); got.Kind != Reject {
		t.Fatalf("entity-only ambiguous command: %v", got)
	}
	for _, text := range []string{"hello /help", "ordinary", ""} {
		if got := p.Decide(commandInput(text)); got.Kind != Bridge {
			t.Fatalf("ordinary text: %v", got)
		}
	}
	in = commandInput("/status")
	in.UserID = 8
	if got := p.Decide(in); got.Kind != Reject {
		t.Fatalf("unauthorized command: %v", got)
	}
	if got := p.DecideAccountAction(commandInput("/help")); got.Kind != Reject {
		t.Fatalf("commands cannot become account actions: %v", got)
	}
	if got := p.DecideMutation(commandInput("/help"), 42, true); got.Kind != Reject {
		t.Fatalf("commands cannot become mutation data: %v", got)
	}
}
