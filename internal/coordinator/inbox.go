package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/darkydtm/whatsgram/internal/admin"
	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/store"
	"github.com/darkydtm/whatsgram/internal/telegram"
	"github.com/go-telegram/bot/models"
)

func (c *Coordinator) RunInboxLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := c.StepInbox(ctx)
		if err != nil {
			if errors.Is(err, store.ErrNoWork) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(c.options.PollInterval):
					continue
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.options.PollInterval):
				continue
			}
		}
	}
}

func (c *Coordinator) StepInbox(ctx context.Context) error {
	now := time.Now()
	event, token, err := c.store.ClaimInbox(ctx, now, c.options.LeaseDuration)
	if err != nil {
		return err
	}

	switch event.Source {
	case domain.Telegram:
		return c.handleTelegramEvent(ctx, event, token, now)
	case domain.WhatsApp:
		return c.handleWhatsAppEvent(ctx, event, token, now)
	default:
		return c.completeInbox(ctx, event, token, now)
	}
}

func (c *Coordinator) handleTelegramEvent(ctx context.Context, e domain.Event, token string, now time.Time) error {
	in, rawText := parseTelegramInput(e)
	in.Text = normalizeCommandText(in.Text)

	decision := c.policy.Decide(in)
	if decision.Kind == admin.Command {
		_ = c.executor.Execute(ctx, decision.Command, in, e)
		return c.completeInbox(ctx, e, token, now)
	}

	if decision.Kind != admin.Bridge {
		return c.completeInbox(ctx, e, token, now)
	}

	chatID, _ := strconv.ParseInt(e.Chat, 10, 64)
	if chatID == 0 {
		chatID = c.cfg.Telegram.ChatID
	}
	threadID := e.TelegramThreadID

	chat, err := c.store.ChatByTopic(ctx, chatID, threadID)
	if err != nil || chat.State != store.ChatActive || chat.WhatsAppJID == "" {
		return c.completeInbox(ctx, e, token, now)
	}

	textContent := e.Text
	if textContent == nil && rawText != "" {
		textContent = &domain.TextContent{Text: rawText}
	}
	if textContent == nil {
		return c.completeInbox(ctx, e, token, now)
	}

	if textContent.ReplyTo != nil && textContent.ReplyTo.Platform != domain.WhatsApp {
		resolved := c.resolveReplyTo(ctx, domain.WhatsApp, *textContent)
		textContent = &resolved
	}

	sendID := c.waPort.AllocateSendID()
	c.recordBridgeSend(sendID)

	delivery := domain.Delivery{
		OperationKey:    fmt.Sprintf("tg:%d:%s", e.InboxID, sendID),
		Platform:        domain.WhatsApp,
		Kind:            "message.create",
		ChatID:          chat.ID,
		TopicGeneration: chat.Generation,
		ExpectedVersion: e.Version,
		Text:            textContent,
		WhatsAppSendID:  sendID,
		EchoActor:       chat.WhatsAppJID,
	}

	c.pendingCount.Add(1)
	return c.store.PlanEvent(ctx, e, token, []domain.Delivery{delivery}, now)
}

func (c *Coordinator) handleWhatsAppEvent(ctx context.Context, e domain.Event, token string, now time.Time) error {
	chat, err := c.store.Chat(ctx, e.Chat)
	if err != nil || chat.Topic == nil || chat.State != store.ChatActive {
		return c.completeInbox(ctx, e, token, now)
	}

	isFromMe := isWhatsAppFromMe(e, c.cfg.WhatsApp.Phone)
	targetID := ""
	if e.Target != nil {
		targetID = e.Target.ID
	}

	if isFromMe {
		if c.isBridgeDispatched(ctx, e, targetID) {
			return c.completeInbox(ctx, e, token, now)
		}

		formatted := formatWhatsAppText(e, true)
		if formatted == nil {
			return c.completeInbox(ctx, e, token, now)
		}
		if formatted.ReplyTo != nil && formatted.ReplyTo.Platform != domain.Telegram {
			resolved := c.resolveReplyTo(ctx, domain.Telegram, *formatted)
			formatted = &resolved
		}

		delivery := domain.Delivery{
			OperationKey:    fmt.Sprintf("wa:owner:%d:%s", e.InboxID, e.Key),
			Platform:        domain.Telegram,
			Kind:            "message.create",
			ChatID:          chat.ID,
			TopicGeneration: chat.Generation,
			ExpectedVersion: e.Version,
			Text:            formatted,
			EchoActor:       chat.WhatsAppJID,
		}

		c.pendingCount.Add(1)
		return c.store.PlanEvent(ctx, e, token, []domain.Delivery{delivery}, now)
	}

	formatted := formatWhatsAppText(e, false)
	if formatted == nil {
		return c.completeInbox(ctx, e, token, now)
	}
	if formatted.ReplyTo != nil && formatted.ReplyTo.Platform != domain.Telegram {
		resolved := c.resolveReplyTo(ctx, domain.Telegram, *formatted)
		formatted = &resolved
	}

	delivery := domain.Delivery{
		OperationKey:    fmt.Sprintf("wa:%d:%s", e.InboxID, e.Key),
		Platform:        domain.Telegram,
		Kind:            "message.create",
		ChatID:          chat.ID,
		TopicGeneration: chat.Generation,
		ExpectedVersion: e.Version,
		Text:            formatted,
		EchoActor:       chat.WhatsAppJID,
	}

	c.pendingCount.Add(1)
	return c.store.PlanEvent(ctx, e, token, []domain.Delivery{delivery}, now)
}

func (c *Coordinator) isBridgeDispatched(ctx context.Context, e domain.Event, targetID string) bool {
	if c.isKnownBridgeSend(e.Key, targetID) {
		return true
	}

	if e.Target != nil {
		part, err := c.store.Part(ctx, *e.Target)
		if err == nil && part.BridgeOwned {
			return true
		}

		matched, err := c.store.MatchEcho(ctx, store.Echo{
			Platform: domain.WhatsApp,
			Chat:     e.Chat,
			Actor:    e.Actor,
			EventID:  e.Target.ID,
			Kind:     string(e.Kind),
			Digest:   e.Version.Digest,
		})
		if err == nil && matched {
			return true
		}
	}
	return false
}

func (c *Coordinator) completeInbox(ctx context.Context, e domain.Event, token string, now time.Time) error {
	err := c.store.PlanEvent(ctx, e, token, nil, now)
	if err == nil {
		return nil
	}
	return c.store.QuarantineInbox(ctx, e.InboxID, token, now)
}

func parseTelegramInput(e domain.Event) (admin.Input, string) {
	chatID, _ := strconv.ParseInt(e.Chat, 10, 64)
	actorID, _ := strconv.ParseInt(e.Actor, 10, 64)
	threadID := e.TelegramThreadID
	var replyID int64
	text := ""

	if e.Text != nil {
		text = e.Text.Text
		if e.Text.ReplyTo != nil {
			replyID, _ = strconv.ParseInt(e.Text.ReplyTo.ID, 10, 64)
		}
	}

	if text == "" && len(e.Raw) > 0 {
		var update models.Update
		if json.Unmarshal(e.Raw, &update) == nil && update.Message != nil {
			if update.Message.Text != "" {
				text = update.Message.Text
			} else if update.Message.Caption != "" {
				text = update.Message.Caption
			}
			if threadID == 0 {
				threadID = int64(update.Message.MessageThreadID)
			}
			if replyID == 0 && update.Message.ReplyToMessage != nil {
				replyID = int64(update.Message.ReplyToMessage.ID)
			}
		}
	}

	if len(e.Metadata) > 0 {
		var meta telegram.Metadata
		if json.Unmarshal(e.Metadata, &meta) == nil {
			if threadID == 0 && meta.ThreadID > 0 {
				threadID = meta.ThreadID
			}
		}
	}

	isBotCmd := e.Kind == "telegram.command" || strings.HasPrefix(strings.TrimSpace(text), "/")

	in := admin.Input{
		Source:         admin.Telegram,
		ChatID:         chatID,
		UserID:         actorID,
		HasUser:        actorID > 0,
		ThreadID:       threadID,
		ReplyMessageID: replyID,
		Text:           text,
		BotCommand:     isBotCmd,
	}
	return in, text
}

func normalizeCommandText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "/pair" {
		return "/pair qr"
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 2 && fields[0] == "/pair" {
		if isDigits(fields[1]) {
			return "/pair code " + fields[1]
		}
	}
	if len(fields) == 3 && fields[0] == "/bind" {
		if isDigits(fields[1]) && strings.Contains(fields[2], "@") {
			return fmt.Sprintf("/bind topic %s %s", fields[2], fields[1])
		}
		if strings.Contains(fields[1], "@") && isDigits(fields[2]) {
			return fmt.Sprintf("/bind topic %s %s", fields[1], fields[2])
		}
	}
	return text
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isWhatsAppFromMe(e domain.Event, phone string) bool {
	if len(e.Raw) > 0 {
		var env struct {
			FromMe bool `json:"from_me"`
		}
		if json.Unmarshal(e.Raw, &env) == nil && env.FromMe {
			return true
		}
	}
	if phone != "" && strings.HasPrefix(e.Actor, phone) {
		return true
	}
	return false
}

func formatWhatsAppText(e domain.Event, isFromMe bool) *domain.TextContent {
	if e.Text == nil {
		return nil
	}
	if isFromMe {
		return prependText(e.Text, "You: ")
	}
	if strings.HasSuffix(e.Chat, "@g.us") {
		author := e.Actor
		if idx := strings.Index(author, "@"); idx != -1 {
			author = author[:idx]
		}
		return prependText(e.Text, fmt.Sprintf("[%s]: ", author))
	}
	return e.Text
}

func prependText(original *domain.TextContent, prefix string) *domain.TextContent {
	if original == nil {
		return &domain.TextContent{Text: prefix}
	}
	res := &domain.TextContent{
		Text:           prefix + original.Text,
		ReplyTo:        original.ReplyTo,
		ForwardLabel:   original.ForwardLabel,
		MentionTargets: original.MentionTargets,
	}
	shift := len(utf16.Encode([]rune(prefix)))
	for _, ent := range original.Entities {
		shifted := ent
		shifted.StartUTF16 += shift
		res.Entities = append(res.Entities, shifted)
	}
	return res
}
