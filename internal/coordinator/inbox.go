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
	in.Text = c.normalizeCommandText(in.Text)

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

	retryDelay := c.cfg.Runtime.RetryBase
	if retryDelay <= 0 {
		retryDelay = 100 * time.Millisecond
	}

	if e.Kind == "telegram.reaction" || e.Kind == domain.ReactionSet {
		actorID, _ := strconv.ParseInt(e.Actor, 10, 64)
		if !c.policy.CanDriveAccount(actorID) {
			return c.completeInbox(ctx, e, token, now)
		}

		targetID := ""
		emoji := ""
		if e.Target != nil {
			targetID = e.Target.ID
		}
		if e.Text != nil {
			emoji = e.Text.Text
		}
		if targetID == "" && len(e.Raw) > 0 {
			var update models.Update
			if json.Unmarshal(e.Raw, &update) == nil && update.MessageReaction != nil {
				targetID = strconv.Itoa(update.MessageReaction.MessageID)
				for _, rx := range update.MessageReaction.NewReaction {
					if rx.Type == models.ReactionTypeTypeEmoji && rx.ReactionTypeEmoji != nil {
						emoji = rx.ReactionTypeEmoji.Emoji
						break
					}
				}
			}
		}
		if targetID == "" {
			return c.completeInbox(ctx, e, token, now)
		}

		parts, err := c.store.FindPartsByRemote(ctx, domain.Telegram, e.Chat, targetID)
		if err != nil {
			return c.store.DeferInbox(ctx, e.InboxID, token, now.Add(retryDelay), now)
		}

		var waPart *domain.DeliveredPart
		for i := range parts {
			if parts[i].Ref.Platform == domain.WhatsApp && !parts[i].Deleted {
				waPart = &parts[i]
				break
			}
		}
		if waPart == nil {
			return c.completeInbox(ctx, e, token, now)
		}

		sendID := c.waPort.AllocateSendID()
		c.recordBridgeSend(sendID)

		if e.Target == nil {
			if err := c.waPort.SetOwnReaction(ctx, waPart.Ref, emoji, sendID); err != nil {
				return err
			}
			return c.completeInbox(ctx, e, token, now)
		}

		delivery := domain.Delivery{
			OperationKey:    fmt.Sprintf("tg:reaction:%d:%s", e.InboxID, sendID),
			Platform:        domain.WhatsApp,
			Kind:            "reaction.set",
			ChatID:          chat.ID,
			TopicGeneration: chat.Generation,
			ExpectedVersion: e.Version,
			Target:          &waPart.Ref,
			Text:            &domain.TextContent{Text: emoji},
			WhatsAppSendID:  sendID,
			EchoActor:       chat.WhatsAppJID,
		}

		c.pendingCount.Add(1)
		return c.store.PlanEvent(ctx, e, token, []domain.Delivery{delivery}, now)
	}

	if e.Kind == domain.MessageRevoke {
		if e.Target == nil {
			return c.completeInbox(ctx, e, token, now)
		}

		parts, err := c.store.FindParts(ctx, *e.Target)
		if err != nil {
			return c.store.DeferInbox(ctx, e.InboxID, token, now.Add(retryDelay), now)
		}

		var waPart *domain.DeliveredPart
		var tgPart *domain.DeliveredPart
		for i := range parts {
			if parts[i].Ref.Platform == domain.WhatsApp && !parts[i].Deleted {
				waPart = &parts[i]
			}
			if parts[i].Ref.Platform == domain.Telegram && !parts[i].Deleted {
				tgPart = &parts[i]
			}
		}
		if waPart == nil || !waPart.BridgeOwned {
			return c.completeInbox(ctx, e, token, now)
		}

		actorID, _ := strconv.ParseInt(e.Actor, 10, 64)
		originAuthorID, _ := strconv.ParseInt(e.Target.Author, 10, 64)
		if tgPart != nil && tgPart.Ref.Author != "" {
			if origID, err := strconv.ParseInt(tgPart.Ref.Author, 10, 64); err == nil && origID > 0 {
				originAuthorID = origID
			}
		}
		if !c.policy.CanMutateOriginal(actorID, originAuthorID, c.policy.CanDriveAccount(actorID)) {
			return c.completeInbox(ctx, e, token, now)
		}

		sendID := c.waPort.AllocateSendID()
		c.recordBridgeSend(sendID)

		delivery := domain.Delivery{
			OperationKey:    fmt.Sprintf("tg:revoke:%d:%s", e.InboxID, sendID),
			Platform:        domain.WhatsApp,
			Kind:            "message.revoke",
			ChatID:          chat.ID,
			TopicGeneration: chat.Generation,
			ExpectedVersion: e.Version,
			Target:          &waPart.Ref,
			WhatsAppSendID:  sendID,
			EchoActor:       chat.WhatsAppJID,
		}

		c.pendingCount.Add(1)
		return c.store.PlanEvent(ctx, e, token, []domain.Delivery{delivery}, now)
	}

	if e.Kind == domain.MessageEdit {
		if e.Target == nil {
			return c.completeInbox(ctx, e, token, now)
		}

		parts, err := c.store.FindParts(ctx, *e.Target)
		if err != nil {
			return c.store.DeferInbox(ctx, e.InboxID, token, now.Add(retryDelay), now)
		}

		var waPart *domain.DeliveredPart
		var tgPart *domain.DeliveredPart
		for i := range parts {
			if parts[i].Ref.Platform == domain.WhatsApp && !parts[i].Deleted {
				waPart = &parts[i]
			}
			if parts[i].Ref.Platform == domain.Telegram && !parts[i].Deleted {
				tgPart = &parts[i]
			}
		}
		if waPart == nil || !waPart.BridgeOwned {
			return c.completeInbox(ctx, e, token, now)
		}

		actorID, _ := strconv.ParseInt(e.Actor, 10, 64)
		originAuthorID, _ := strconv.ParseInt(e.Target.Author, 10, 64)
		if tgPart != nil && tgPart.Ref.Author != "" {
			if origID, err := strconv.ParseInt(tgPart.Ref.Author, 10, 64); err == nil && origID > 0 {
				originAuthorID = origID
			}
		}
		if !c.policy.CanMutateOriginal(actorID, originAuthorID, c.policy.CanDriveAccount(actorID)) {
			return c.completeInbox(ctx, e, token, now)
		}

		msgState, err := c.store.Message(ctx, *e.Target)
		if err == nil {
			if msgState.Tombstone {
				return c.completeInbox(ctx, e, token, now)
			}
			if !msgState.CreatedAt.IsZero() && now.Sub(msgState.CreatedAt) > 15*time.Minute {
				return c.completeInbox(ctx, e, token, now)
			}
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

		opKey := fmt.Sprintf("tg:edit:%d:%s", e.InboxID, sendID)
		if strings.Contains(string(e.Metadata), "caption") || strings.Contains(e.Key, "caption") {
			opKey = fmt.Sprintf("tg:edit:caption:%d:%s", e.InboxID, sendID)
		}

		delivery := domain.Delivery{
			OperationKey:    opKey,
			Platform:        domain.WhatsApp,
			Kind:            "message.edit",
			ChatID:          chat.ID,
			TopicGeneration: chat.Generation,
			ExpectedVersion: e.Version,
			Target:          &waPart.Ref,
			Text:            textContent,
			WhatsAppSendID:  sendID,
			EchoActor:       chat.WhatsAppJID,
		}

		c.pendingCount.Add(1)
		return c.store.PlanEvent(ctx, e, token, []domain.Delivery{delivery}, now)
	}

	textContent := e.Text
	if textContent == nil && rawText != "" {
		textContent = &domain.TextContent{Text: rawText}
	}
	if textContent == nil {
		reason := "unknown format"
		if len(e.Metadata) > 0 {
			var m struct {
				Reason string `json:"reason"`
			}
			if json.Unmarshal(e.Metadata, &m) == nil && m.Reason != "" {
				reason = m.Reason
			}
		}
		fallbackNotice := fmt.Sprintf("[Unsupported media: %s]", reason)
		if reason == "animated-sticker" || strings.Contains(e.Key, "sticker") {
			fallbackNotice = "[Unsupported animated sticker: forwarded as fallback]"
		}
		textContent = &domain.TextContent{Text: fallbackNotice}
	}

	if textContent.ReplyTo != nil && textContent.ReplyTo.Platform != domain.WhatsApp {
		resolved := c.resolveReplyTo(ctx, domain.WhatsApp, *textContent)
		textContent = &resolved
	}

	sendID := c.waPort.AllocateSendID()
	c.recordBridgeSend(sendID)

	opKey := fmt.Sprintf("tg:%d:%s", e.InboxID, sendID)
	if strings.Contains(string(e.Metadata), "caption") || strings.Contains(e.Key, "caption") {
		opKey = fmt.Sprintf("%s:caption", opKey)
	}

	delivery := domain.Delivery{
		OperationKey:    opKey,
		Platform:        domain.WhatsApp,
		Kind:            "message.create",
		ChatID:          chat.ID,
		TopicGeneration: chat.Generation,
		ExpectedVersion: e.Version,
		Text:            textContent,
		WhatsAppSendID:  sendID,
		EchoActor:       chat.WhatsAppJID,
	}

	var timers []store.TimerSpec
	if dur, ok := c.getChatTimer(ctx, chat.WhatsAppJID); ok && dur > 0 {
		timers = append(timers, store.TimerSpec{
			Kind:     store.TimerExpire,
			Version:  e.Version,
			Deadline: now.Add(dur),
		})
	}

	c.pendingCount.Add(1)
	if len(timers) > 0 {
		return c.store.PlanEventWithTimers(ctx, e, token, []domain.Delivery{delivery}, timers, now)
	}
	return c.store.PlanEvent(ctx, e, token, []domain.Delivery{delivery}, now)
}

func (c *Coordinator) handleWhatsAppEvent(ctx context.Context, e domain.Event, token string, now time.Time) error {
	chat, err := c.store.Chat(ctx, e.Chat)
	if err != nil || chat.Topic == nil || chat.State != store.ChatActive {
		if _, err := c.EnsureTopic(ctx, e.Chat, ""); err != nil {
			return c.completeInbox(ctx, e, token, now)
		}
		chat, err = c.store.Chat(ctx, e.Chat)
		if err != nil || chat.Topic == nil || chat.State != store.ChatActive {
			return c.completeInbox(ctx, e, token, now)
		}
	}

	isFromMe := isWhatsAppFromMe(e, c.cfg.WhatsApp.Phone)
	targetID := ""
	if e.Target != nil {
		targetID = e.Target.ID
	}

	if isFromMe && c.isBridgeDispatched(ctx, e, targetID) {
		return c.completeInbox(ctx, e, token, now)
	}

	retryDelay := c.cfg.Runtime.RetryBase
	if retryDelay <= 0 {
		retryDelay = 100 * time.Millisecond
	}

	switch e.Kind {
	case domain.ChatTitle:
		if e.Text != nil && e.Text.Text != "" && chat.Topic != nil {
			_ = c.tgPort.RenameTopic(ctx, *chat.Topic, e.Text.Text)
		}
		return c.store.CompleteInbox(ctx, e.InboxID, token, now)

	case domain.ChatTimer:
		if e.Text != nil && e.Text.Text != "" {
			if dur, err := time.ParseDuration(e.Text.Text); err == nil {
				c.setChatTimer(ctx, e.Chat, dur)
			}
		}
		return c.store.CompleteInbox(ctx, e.InboxID, token, now)

	case domain.MessageEdit:
		if e.Target == nil {
			return c.completeInbox(ctx, e, token, now)
		}
		parts, err := c.store.FindParts(ctx, *e.Target)
		if err != nil {
			return c.store.DeferInbox(ctx, e.InboxID, token, now.Add(retryDelay), now)
		}
		isCaption := strings.Contains(string(e.Metadata), "caption") || strings.Contains(e.Key, "caption") || strings.Contains(e.Version.Tie, "caption")
		var tgPart *domain.DeliveredPart
		for i := range parts {
			if parts[i].Ref.Platform == domain.Telegram && !parts[i].Deleted {
				if isCaption && (parts[i].Role == domain.RoleCaptionOverflow || parts[i].Capabilities&domain.CanEditCaption != 0) {
					tgPart = &parts[i]
					break
				} else if !isCaption && (parts[i].Role == domain.RolePrimary || parts[i].Index == 0) {
					tgPart = &parts[i]
					break
				}
			}
		}
		if tgPart == nil {
			for i := range parts {
				if parts[i].Ref.Platform == domain.Telegram && !parts[i].Deleted {
					tgPart = &parts[i]
					break
				}
			}
		}
		if tgPart == nil {
			return c.completeInbox(ctx, e, token, now)
		}
		formatted := c.formatWhatsAppText(ctx, e, isFromMe)
		if formatted == nil {
			return c.completeInbox(ctx, e, token, now)
		}
		if formatted.ReplyTo != nil && formatted.ReplyTo.Platform != domain.Telegram {
			resolved := c.resolveReplyTo(ctx, domain.Telegram, *formatted)
			formatted = &resolved
		}

		opKey := fmt.Sprintf("wa:edit:%d:%s", e.InboxID, e.Key)
		if isCaption {
			opKey = fmt.Sprintf("wa:edit:caption:%d:%s", e.InboxID, e.Key)
		}

		delivery := domain.Delivery{
			OperationKey:    opKey,
			Platform:        domain.Telegram,
			Kind:            "message.edit",
			ChatID:          chat.ID,
			TopicGeneration: chat.Generation,
			ExpectedVersion: e.Version,
			Target:          &tgPart.Ref,
			Text:            formatted,
			EchoActor:       chat.WhatsAppJID,
		}

		c.pendingCount.Add(1)
		return c.store.PlanEvent(ctx, e, token, []domain.Delivery{delivery}, now)

	case domain.MessageRevoke:
		if e.Target == nil {
			return c.completeInbox(ctx, e, token, now)
		}
		parts, err := c.store.FindParts(ctx, *e.Target)
		if err != nil {
			return c.store.DeferInbox(ctx, e.InboxID, token, now.Add(retryDelay), now)
		}
		var tgPart *domain.DeliveredPart
		for i := range parts {
			if parts[i].Ref.Platform == domain.Telegram && !parts[i].Deleted {
				tgPart = &parts[i]
				break
			}
		}
		if tgPart == nil {
			return c.completeInbox(ctx, e, token, now)
		}

		delivery := domain.Delivery{
			OperationKey:    fmt.Sprintf("wa:revoke:%d:%s", e.InboxID, e.Key),
			Platform:        domain.Telegram,
			Kind:            "message.revoke",
			ChatID:          chat.ID,
			TopicGeneration: chat.Generation,
			ExpectedVersion: e.Version,
			Target:          &tgPart.Ref,
			EchoActor:       chat.WhatsAppJID,
		}

		c.pendingCount.Add(1)
		return c.store.PlanEvent(ctx, e, token, []domain.Delivery{delivery}, now)

	case domain.ReactionSet:
		if e.Target == nil {
			return c.completeInbox(ctx, e, token, now)
		}
		parts, err := c.store.FindParts(ctx, *e.Target)
		if err != nil {
			return c.store.DeferInbox(ctx, e.InboxID, token, now.Add(retryDelay), now)
		}
		var tgPart *domain.DeliveredPart
		for i := range parts {
			if parts[i].Ref.Platform == domain.Telegram && !parts[i].Deleted {
				tgPart = &parts[i]
				break
			}
		}
		if tgPart == nil {
			return c.completeInbox(ctx, e, token, now)
		}

		delivery := domain.Delivery{
			OperationKey:    fmt.Sprintf("wa:reaction:%d:%s", e.InboxID, e.Key),
			Platform:        domain.Telegram,
			Kind:            "reaction.set",
			ChatID:          chat.ID,
			TopicGeneration: chat.Generation,
			ExpectedVersion: e.Version,
			Target:          &tgPart.Ref,
			Text:            e.Text,
			EchoActor:       chat.WhatsAppJID,
		}

		c.pendingCount.Add(1)
		return c.store.PlanEvent(ctx, e, token, []domain.Delivery{delivery}, now)

	default:
		formatted := c.formatWhatsAppText(ctx, e, isFromMe)
		if formatted == nil {
			reason := "unsupported media"
			if len(e.Metadata) > 0 {
				var m struct {
					Reason string `json:"reason"`
				}
				if json.Unmarshal(e.Metadata, &m) == nil && m.Reason != "" {
					reason = m.Reason
				}
			}
			fallbackText := fmt.Sprintf("[Unsupported media: %s]", reason)
			if reason == "animated-sticker" || strings.Contains(e.Key, "sticker") {
				fallbackText = "[Unsupported animated sticker: forwarded as fallback]"
			}
			formatted = &domain.TextContent{Text: fallbackText}
			if isFromMe {
				formatted = prependText(formatted, "You: ")
			} else if strings.HasSuffix(e.Chat, "@g.us") {
				author := e.Actor
				authorName := ""
				if author != "" {
					if m, err := c.waPort.GetContactMeta(ctx, author); err == nil && m.Name != "" {
						authorName = m.Name
					}
				}
				if authorName == "" {
					authorName = author
					if idx := strings.Index(authorName, "@"); idx != -1 {
						authorName = authorName[:idx]
					}
				}
				var authorURL string
				if author != "" {
					if authorTopic, err := c.EnsureTopic(ctx, author, authorName); err == nil {
						authorURL = authorTopic.URL()
					}
				}
				formatted = appendHyperlink(formatted, authorName, authorURL)
			}
		}
		if formatted.ReplyTo != nil && formatted.ReplyTo.Platform != domain.Telegram {
			resolved := c.resolveReplyTo(ctx, domain.Telegram, *formatted)
			formatted = &resolved
		}

		opKey := fmt.Sprintf("wa:%d:%s", e.InboxID, e.Key)
		if isFromMe {
			opKey = fmt.Sprintf("wa:owner:%d:%s", e.InboxID, e.Key)
		}
		if strings.Contains(string(e.Metadata), "caption") || strings.Contains(e.Key, "caption") {
			opKey = fmt.Sprintf("%s:caption", opKey)
		}

		delivery := domain.Delivery{
			OperationKey:    opKey,
			Platform:        domain.Telegram,
			Kind:            "message.create",
			ChatID:          chat.ID,
			TopicGeneration: chat.Generation,
			ExpectedVersion: e.Version,
			Text:            formatted,
			EchoActor:       chat.WhatsAppJID,
		}

		var timers []store.TimerSpec
		if dur, ok := c.getChatTimer(ctx, chat.WhatsAppJID); ok && dur > 0 {
			timers = append(timers, store.TimerSpec{
				Kind:     store.TimerExpire,
				Version:  e.Version,
				Deadline: now.Add(dur),
			})
		}

		c.pendingCount.Add(1)
		if len(timers) > 0 {
			return c.store.PlanEventWithTimers(ctx, e, token, []domain.Delivery{delivery}, timers, now)
		}
		return c.store.PlanEvent(ctx, e, token, []domain.Delivery{delivery}, now)
	}
}

func (c *Coordinator) isBridgeDispatched(ctx context.Context, e domain.Event, targetID string) bool {
	switch e.Kind {
	case domain.MessageEdit, domain.MessageRevoke, domain.ReactionSet:
		return c.isKnownBridgeSend(e.Key, "")
	default:
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
}

func (c *Coordinator) completeInbox(ctx context.Context, e domain.Event, token string, now time.Time) error {
	if err := c.store.CompleteInbox(ctx, e.InboxID, token, now); err == nil {
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

func (c *Coordinator) normalizeCommandText(text string) string {
	trimmed := strings.TrimSpace(text)
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return text
	}

	cmdVerb := fields[0]
	atBot := ""
	if verb, botSuffix, hasBot := strings.Cut(fields[0], "@"); hasBot {
		cmdVerb = verb
		atBot = "@" + botSuffix
	}

	if cmdVerb == "/pair" {
		if len(fields) == 1 {
			if c != nil && c.cfg.WhatsApp.PairingMode == "code" && c.cfg.WhatsApp.Phone != "" {
				return cmdVerb + atBot + " code " + c.cfg.WhatsApp.Phone
			}
			return cmdVerb + atBot + " qr"
		}
		if len(fields) == 2 && isDigits(fields[1]) {
			return cmdVerb + atBot + " code " + fields[1]
		}
	}
	if cmdVerb == "/bind" && len(fields) == 3 {
		if isDigits(fields[1]) && strings.Contains(fields[2], "@") {
			return fmt.Sprintf("%s%s topic %s %s", cmdVerb, atBot, fields[2], fields[1])
		}
		if strings.Contains(fields[1], "@") && isDigits(fields[2]) {
			return fmt.Sprintf("%s%s topic %s %s", cmdVerb, atBot, fields[1], fields[2])
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

func (c *Coordinator) formatWhatsAppText(ctx context.Context, e domain.Event, isFromMe bool) *domain.TextContent {
	if len(e.Metadata) > 0 && (e.Text == nil || e.Text.Text == "") {
		var meta struct {
			Type      string   `json:"type"`
			Latitude  float64  `json:"latitude"`
			Longitude float64  `json:"longitude"`
			Title     string   `json:"title"`
			Address   string   `json:"address"`
			Name      string   `json:"name"`
			Phone     string   `json:"phone"`
			Question  string   `json:"question"`
			Options   []string `json:"options"`
		}
		if json.Unmarshal(e.Metadata, &meta) == nil {
			switch meta.Type {
			case "location":
				desc := fmt.Sprintf("📍 Location: %.6f, %.6f", meta.Latitude, meta.Longitude)
				if meta.Title != "" {
					desc += fmt.Sprintf(" (%s)", meta.Title)
				}
				e.Text = &domain.TextContent{Text: desc}
			case "contact":
				desc := fmt.Sprintf("👤 Contact: %s (%s)", meta.Name, meta.Phone)
				e.Text = &domain.TextContent{Text: desc}
			case "poll":
				desc := fmt.Sprintf("📊 Poll: %s", meta.Question)
				for _, opt := range meta.Options {
					desc += fmt.Sprintf("\n• %s", opt)
				}
				e.Text = &domain.TextContent{Text: desc}
			}
		}
	}

	if e.Text == nil {
		return nil
	}
	if isFromMe {
		return prependText(e.Text, "You: ")
	}
	if strings.HasSuffix(e.Chat, "@g.us") {
		author := e.Actor
		authorName := ""
		if author != "" {
			if m, err := c.waPort.GetContactMeta(ctx, author); err == nil && m.Name != "" {
				authorName = m.Name
			}
		}
		if authorName == "" {
			authorName = author
			if idx := strings.Index(authorName, "@"); idx != -1 {
				authorName = authorName[:idx]
			}
		}
		var authorURL string
		if author != "" {
			if authorTopic, err := c.EnsureTopic(ctx, author, authorName); err == nil {
				authorURL = authorTopic.URL()
			}
		}
		return appendHyperlink(e.Text, authorName, authorURL)
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
