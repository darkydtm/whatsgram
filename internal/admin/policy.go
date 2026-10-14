package admin

import "errors"

const (
	Telegram = "telegram"
	WhatsApp = "whatsapp"
)

type Kind uint8

const (
	Ignore Kind = iota
	Bridge
	Command
	Reject
)

type Input struct {
	Source         string
	ChatID         int64
	UserID         int64
	HasUser        bool
	SenderChat     bool
	ThreadID       int64
	MessageID      int64
	ReplyMessageID int64
	Text           string
	BotCommand     bool
}

type Decision struct {
	Kind   Kind
	Code   string
	Reason string
}

type Policy struct {
	chatID      int64
	allowed     map[int64]struct{}
	botUsername string
}

func NewPolicy(chatID int64, allowed []int64, botUsername string) (Policy, error) {
	if chatID == 0 || len(allowed) == 0 {
		return Policy{}, errors.New("invalid admin policy")
	}
	p := Policy{chatID: chatID, allowed: make(map[int64]struct{}), botUsername: botUsername}
	for _, id := range allowed {
		if id <= 0 {
			return Policy{}, errors.New("invalid admin policy")
		}
		p.allowed[id] = struct{}{}
	}
	return p, nil
}

func (p Policy) DriverID() int64 {
	var driver int64
	for id := range p.allowed {
		if driver == 0 || id < driver {
			driver = id
		}
	}
	return driver
}

func (p Policy) CanDriveAccount(userID int64) bool {
	return userID > 0 && userID == p.DriverID()
}

func (p Policy) CanMutateOriginal(actorID, originUserID int64, explicitOverride bool) bool {
	_, authorized := p.allowed[actorID]
	return authorized && actorID > 0 && originUserID > 0 && (actorID == originUserID || explicitOverride)
}

func (p Policy) Decide(in Input) Decision {
	if in.Source == WhatsApp {
		return Decision{Kind: Bridge, Code: "source-data", Reason: "WhatsApp input is data, not management."}
	}
	if in.Source != Telegram {
		return Decision{Kind: Ignore, Code: "unknown-source", Reason: "Not a supported source."}
	}
	if p.chatID == 0 || in.ChatID != p.chatID {
		return Decision{Kind: Reject, Code: "wrong-chat", Reason: "Management requires the configured meta-group."}
	}
	if !in.HasUser || in.SenderChat || in.UserID <= 0 {
		return Decision{Kind: Reject, Code: "missing-actor", Reason: "A numeric user actor is required."}
	}
	if _, ok := p.allowed[in.UserID]; !ok {
		return Decision{Kind: Reject, Code: "unauthorized-actor", Reason: "The actor is not authorized."}
	}
	return Decision{Kind: Bridge, Code: "admitted", Reason: "Authorized data input."}
}
