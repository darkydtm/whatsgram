package admin

import "strings"

type ParsedCommand struct {
	Name     string
	Mode     string
	Argument PrivateValue
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
	command := ParsedCommand{Name: verb}
	switch verb {
	case "help", "status", "sync":
		if len(fields) != 1 {
			return invalidCommand()
		}
	case "pair":
		if len(fields) == 2 && fields[1] == "qr" {
			command.Mode = "qr"
		} else if len(fields) == 3 && fields[1] == "code" && internationalDigits(fields[2]) {
			command.Mode = "code"
			command.Argument = privateValue(fields[2])
		} else {
			return invalidCommand()
		}
	default:
		return invalidCommand()
	}
	return Decision{Kind: Command, Code: "parsed-command", Reason: "Parsed intent requires executor validation; no action has run.", Command: command}
}

func internationalDigits(value string) bool {
	if len(value) < 7 || len(value) > 15 || value[0] == '0' {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func invalidCommand() Decision {
	return Decision{Kind: Reject, Code: "invalid-command", Reason: "Unknown command or invalid command syntax or context."}
}
