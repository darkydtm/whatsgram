package admin

import "strings"

type ParsedCommand struct {
	Name string
}

func (p Policy) parseCommand(in Input) Decision {
	text := strings.TrimSpace(in.Text)
	if !strings.HasPrefix(text, "/") {
		return invalidCommand()
	}
	fields := strings.Fields(text)
	verb := strings.TrimPrefix(fields[0], "/")
	if strings.Count(verb, "@") > 1 {
		return invalidCommand()
	}
	if name, username, addressed := strings.Cut(verb, "@"); addressed {
		if username == "" {
			return invalidCommand()
		}
		if !strings.EqualFold(username, p.botUsername) {
			return Decision{Kind: Ignore, Code: "other-bot", Reason: "The command addresses another bot."}
		}
		verb = name
	}
	switch verb {
	case "help", "status", "sync":
		if len(fields) != 1 {
			return invalidCommand()
		}
	default:
		return invalidCommand()
	}
	return Decision{Kind: Command, Code: "parsed-command", Reason: "Parsed intent requires executor validation; no action has run.", Command: ParsedCommand{Name: verb}}
}

func invalidCommand() Decision {
	return Decision{Kind: Reject, Code: "invalid-command", Reason: "Unknown command or invalid command syntax or context."}
}
