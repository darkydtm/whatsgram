package coordinator

import (
	"context"
	"fmt"
	"time"

	"github.com/darkydtm/whatsgram/internal/admin"
	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/store"
)

type AdminExecutor struct {
	coord *Coordinator
}

func newAdminExecutor(coord *Coordinator) *AdminExecutor {
	return &AdminExecutor{coord: coord}
}

func (a *AdminExecutor) Execute(ctx context.Context, cmd admin.ParsedCommand, in admin.Input, e domain.Event) error {
	switch cmd.Name {
	case "help":
		return a.handleHelp(ctx, in)
	case "status":
		return a.handleStatus(ctx, in)
	case "pair":
		return a.handlePair(ctx, cmd, in)
	case "sync":
		return a.handleSync(ctx, in)
	case "bind":
		return a.handleBind(ctx, cmd, in)
	default:
		return a.coord.reply(ctx, in, fmt.Sprintf("Command /%s is not supported", cmd.Name))
	}
}

func (a *AdminExecutor) handleHelp(ctx context.Context, in admin.Input) error {
	helpText := "Available commands:\n" +
		"/help - Show this help message\n" +
		"/status - Show bridge status and health metrics\n" +
		"/pair - Initiate WhatsApp QR pairing\n" +
		"/pair <code> - Initiate phone pairing with numeric code\n" +
		"/sync - Discover WhatsApp chats and create Telegram topics\n" +
		"/bind <topic_id> <jid> - Explicitly bind a Telegram topic to a WhatsApp JID"
	return a.coord.reply(ctx, in, helpText)
}

func (a *AdminExecutor) handleStatus(ctx context.Context, in admin.Input) error {
	activeChats := a.coord.activeChatCount.Load()
	pendingDeliveries := a.coord.pendingCount.Load()
	version := a.coord.options.Version

	msg := fmt.Sprintf("WhatsGram Bridge Status:\n"+
		"Version: %s\n"+
		"Active chats: %d\n"+
		"Pending deliveries: %d\n"+
		"Health: operational",
		version, activeChats, pendingDeliveries)
	return a.coord.reply(ctx, in, msg)
}

func (a *AdminExecutor) handlePair(ctx context.Context, cmd admin.ParsedCommand, in admin.Input) error {
	if cmd.Mode == "code" {
		phone := cmd.Argument.Reveal()
		if phone == "" {
			phone = a.coord.cfg.WhatsApp.Phone
		}
		notice, err := a.coord.pairingPort.BeginPhoneCode(ctx, phone)
		if err != nil {
			return a.coord.reply(ctx, in, "Failed to initiate phone code pairing.")
		}
		secret := notice.Secret.Reveal()
		msg := fmt.Sprintf("WhatsApp Pairing Code: %s\nExpires at: %s", secret, notice.ExpiresAt.UTC().Format(time.RFC3339))
		return a.coord.reply(ctx, in, msg)
	}

	qrChan, err := a.coord.pairingPort.BeginQR(ctx)
	if err != nil {
		return a.coord.reply(ctx, in, "Failed to initiate QR pairing.")
	}

	select {
	case notice, ok := <-qrChan:
		if !ok {
			return a.coord.reply(ctx, in, "QR pairing channel closed.")
		}
		secret := notice.Secret.Reveal()
		msg := fmt.Sprintf("WhatsApp QR Pairing:\nSecret: %s\nExpires at: %s", secret, notice.ExpiresAt.UTC().Format(time.RFC3339))
		return a.coord.reply(ctx, in, msg)
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(15 * time.Second):
		return a.coord.reply(ctx, in, "Timed out waiting for QR code notice.")
	}
}

func (a *AdminExecutor) handleSync(ctx context.Context, in admin.Input) error {
	candidates, err := a.coord.waPort.DiscoverChats(ctx)
	if err != nil {
		return a.coord.reply(ctx, in, "Failed to discover WhatsApp chats.")
	}

	chatID := in.ChatID
	if chatID == 0 {
		chatID = a.coord.cfg.Telegram.ChatID
	}

	synced := 0
	existing := 0
	failed := 0

	for _, cand := range candidates {
		existingChat, err := a.coord.store.Chat(ctx, cand.WhatsAppJID)
		if err == nil && existingChat.Topic != nil && existingChat.State == store.ChatActive {
			existing++
			continue
		}

		_ = a.coord.store.DiscoverChat(ctx, cand, chatID)

		title := cand.Title
		if title == "" {
			title = cand.WhatsAppJID
		}

		topicRef, err := a.coord.tgPort.CreateTopic(ctx, chatID, title)
		if err != nil {
			failed++
			continue
		}

		gen := int64(1)
		if ch, err := a.coord.store.Chat(ctx, cand.WhatsAppJID); err == nil {
			gen = ch.Generation + 1
		}
		topicRef.Generation = gen

		if err := a.coord.store.BindTopic(ctx, cand.WhatsAppJID, topicRef); err != nil {
			failed++
			continue
		}

		synced++
		a.coord.activeChatCount.Add(1)
	}

	msg := fmt.Sprintf("Sync completed: %d synced, %d existing, %d failed", synced, existing, failed)
	return a.coord.reply(ctx, in, msg)
}

func (a *AdminExecutor) handleBind(ctx context.Context, cmd admin.ParsedCommand, in admin.Input) error {
	jid := cmd.Argument.Reveal()
	topicID := cmd.ThreadID
	chatID := in.ChatID
	if chatID == 0 {
		chatID = a.coord.cfg.Telegram.ChatID
	}

	if jid == "" || topicID <= 0 {
		return a.coord.reply(ctx, in, "Invalid /bind parameters.")
	}

	chat, err := a.coord.store.Chat(ctx, jid)
	if err != nil {
		cand := domain.ChatCandidate{
			WhatsAppJID: jid,
			Title:       jid,
			Source:      "manual-bind",
		}
		if err := a.coord.store.DiscoverChat(ctx, cand, chatID); err != nil {
			return a.coord.reply(ctx, in, "Failed to register WhatsApp chat candidate.")
		}
		chat, err = a.coord.store.Chat(ctx, jid)
		if err != nil {
			return a.coord.reply(ctx, in, "Failed to retrieve WhatsApp chat record.")
		}
	}

	topicRef := domain.TopicRef{
		ChatID:     chatID,
		ThreadID:   topicID,
		Generation: chat.Generation + 1,
	}

	if err := a.coord.store.BindTopic(ctx, jid, topicRef); err != nil {
		return a.coord.reply(ctx, in, fmt.Sprintf("Failed to bind topic: %v", err))
	}

	a.coord.activeChatCount.Add(1)
	return a.coord.reply(ctx, in, fmt.Sprintf("Successfully bound topic %d to %s", topicID, jid))
}

func (c *Coordinator) reply(ctx context.Context, in admin.Input, text string) error {
	chatID := in.ChatID
	if chatID == 0 {
		chatID = c.cfg.Telegram.ChatID
	}
	req := domain.SendTextRequest{
		OperationKey: fmt.Sprintf("reply:%d:%d", in.UserID, time.Now().UnixNano()),
		TelegramTopic: domain.TopicRef{
			ChatID:   chatID,
			ThreadID: in.ThreadID,
		},
		Content: domain.TextContent{
			Text: text,
		},
	}
	_, err := c.tgPort.SendText(ctx, req)
	return err
}
