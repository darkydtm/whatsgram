package admin

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type ParsedCommand struct {
	Name            string
	Mode            string
	Argument        PrivateValue
	TargetMessageID int64
	ThreadID        int64
	Timer           time.Duration
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
	case "del", "pin", "read":
		if len(fields) != 1 || in.ReplyMessageID <= 0 {
			return invalidCommand()
		}
		command.TargetMessageID = in.ReplyMessageID
	case "unpin":
		if len(fields) == 1 && in.ReplyMessageID > 0 {
			command.TargetMessageID = in.ReplyMessageID
		} else if len(fields) == 2 && in.ReplyMessageID == 0 {
			id, ok := positiveID(fields[1])
			if !ok {
				return invalidCommand()
			}
			command.TargetMessageID = id
		} else {
			return invalidCommand()
		}
	case "timer":
		if len(fields) != 2 || in.ThreadID <= 0 {
			return invalidCommand()
		}
		switch fields[1] {
		case "off":
		case "24h":
			command.Timer = 24 * time.Hour
		case "7d":
			command.Timer = 7 * 24 * time.Hour
		case "90d":
			command.Timer = 90 * 24 * time.Hour
		default:
			return invalidCommand()
		}
		command.ThreadID = in.ThreadID
	case "rename", "rename-wa":
		label := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		if in.ThreadID <= 0 || !validLabel(label) {
			return invalidCommand()
		}
		command.ThreadID = in.ThreadID
		command.Argument = privateValue(label)
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

func positiveID(value string) (int64, bool) {
	if value == "" || value[0] < '1' || value[0] > '9' {
		return 0, false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(value, 10, 32)
	return id, err == nil
}

func validLabel(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 128 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}

func invalidCommand() Decision {
	return Decision{Kind: Reject, Code: "invalid-command", Reason: "Unknown command or invalid command syntax or context."}
}
