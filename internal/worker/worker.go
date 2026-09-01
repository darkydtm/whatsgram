package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/darky/whatsgram/internal/provider"
	"github.com/darky/whatsgram/internal/store"
)

type Worker struct {
	Store          *store.Store
	WhatsApp       provider.WhatsApp
	Telegram       provider.Telegram
	GroupID        int64
	SystemThreadID int64
}

type whatsappEvent struct {
	From     string              `json:"from"`
	Name     string              `json:"name"`
	ChatName string              `json:"chat_name"`
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Body     string              `json:"body"`
	Status   string              `json:"status"`
	MediaID  string              `json:"media_id"`
	Mimetype string              `json:"mimetype"`
	Caption  string              `json:"caption"`
	Media    *provider.MediaInfo `json:"media"`
	Edited   bool                `json:"edited"`
	Deleted  bool                `json:"deleted"`
	TargetID string              `json:"target_id"`
	Action   string              `json:"action"`
	Reaction string              `json:"reaction"`
}

type telegramFile struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type telegramSticker struct {
	telegramFile
	IsAnimated bool `json:"is_animated"`
	IsVideo    bool `json:"is_video"`
}

type telegramMessage struct {
	MessageID       int64            `json:"message_id"`
	MessageThreadID int64            `json:"message_thread_id"`
	Text            string           `json:"text"`
	Caption         string           `json:"caption"`
	Photo           []telegramFile   `json:"photo"`
	Document        *telegramFile    `json:"document"`
	Video           *telegramFile    `json:"video"`
	Animation       *telegramFile    `json:"animation"`
	VideoNote       *telegramFile    `json:"video_note"`
	Audio           *telegramFile    `json:"audio"`
	Voice           *telegramFile    `json:"voice"`
	Sticker         *telegramSticker `json:"sticker"`
}

type telegramEvent struct {
	Message       *telegramMessage `json:"message"`
	EditedMessage *struct {
		MessageThreadID int64  `json:"message_thread_id"`
		MessageID       int64  `json:"message_id"`
		Text            string `json:"text"`
	} `json:"edited_message"`
}

func (w Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		w.processInbox(ctx)
		w.processOutbox(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w Worker) processInbox(ctx context.Context) {
	event, err := w.Store.ClaimInbox(ctx)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return
	}

	if event.Provider == "whatsapp" {
		err = w.handleWhatsApp(ctx, event.Payload)
	} else if event.Provider == "telegram" {
		err = w.handleTelegram(ctx, event.Payload)
	} else {
		err = fmt.Errorf("unsupported inbox provider %q", event.Provider)
	}
	if err != nil {
		log.Printf("process inbox %d: %v", event.ID, err)
		_ = w.Store.ResetInbox(ctx, event.ID, err)
		return
	}
	_ = w.Store.CompleteInbox(ctx, event.ID)
}

func (w Worker) handleWhatsApp(ctx context.Context, payload []byte) error {
	var event whatsappEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	if event.ID == "" {
		return nil
	}
	if event.Status != "" {
		return w.Store.SetMessageStatus(ctx, event.ID, event.Status)
	}
	if event.Edited || event.Deleted {
		if event.TargetID == "" {
			event.TargetID = event.ID
		}
		message, err := w.Store.MessageByProviderID(ctx, event.TargetID)
		if err != nil {
			return err
		}
		if message.TelegramMessageID == 0 {
			return nil
		}
		return w.Store.CreateOutbox(ctx, "telegram", &message.ChatID, map[string]any{
			"action": map[string]any{"type": map[bool]string{true: "delete", false: "edit"}[event.Deleted], "message_id": message.TelegramMessageID, "body": event.Body},
		})
	}
	name := event.Name
	if event.ChatName != "" {
		name = event.ChatName
	}
	if name == "" {
		name = event.From
	}
	if name == "" {
		name = "WhatsApp"
	}
	chatID, err := w.Store.UpsertChat(ctx, event.From, name)
	if err != nil {
		return err
	}
	if event.Action == "edit" || event.Action == "delete" {
		message, lookupErr := w.Store.MessageByProviderID(ctx, event.TargetID)
		if lookupErr != nil {
			return lookupErr
		}
		if message.TelegramMessageID == 0 {
			return nil
		}
		body := event.Body
		if event.Action == "delete" {
			body = "[deleted]"
		}
		return w.Store.CreateOutbox(ctx, "telegram", &message.ChatID, map[string]any{
			"action": map[string]any{"type": event.Action, "message_id": message.TelegramMessageID, "body": body},
		})
	}
	if event.Action == "reaction" {
		threadID, threadErr := w.Store.TopicForChat(ctx, chatID)
		if threadErr != nil {
			return threadErr
		}
		return w.Store.CreateOutbox(ctx, "telegram", &chatID, map[string]any{
			"thread_id": threadID,
			"body":      fmt.Sprintf("%s reacted %q", name, event.Reaction),
		})
	}
	if event.MediaID != "" {
		mimetype := event.Mimetype
		if event.Media != nil {
			mimetype = event.Media.Mimetype
		}
		if err := w.Store.CreateMedia(ctx, chatID, "whatsapp", event.MediaID, mimetype, event.Caption); err != nil {
			return err
		}
	}
	if strings.TrimSpace(event.Body) == "" && strings.TrimSpace(event.Caption) != "" {
		event.Body = event.Caption
	}
	if strings.TrimSpace(event.Body) == "" && event.Media == nil {
		event.Body = "[WHATSAPP MESSAGE]"
	}
	inserted, err := w.Store.AddMessage(ctx, chatID, "inbound", event.ID, event.Body)
	if err != nil || !inserted {
		return err
	}
	if muted, err := w.Store.IsMuted(ctx, chatID); err != nil {
		return err
	} else if muted {
		return nil
	}
	threadID, err := w.Store.TopicForChat(ctx, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		threadID, err = w.Telegram.CreateTopic(ctx, w.GroupID, safeTopicName(name))
		if err == nil {
			err = w.Store.LinkTopic(ctx, chatID, w.GroupID, threadID)
		}
	}
	if err != nil {
		return err
	}
	if event.Media != nil {
		caption := strings.TrimSpace(event.Body)
		if caption == "" {
			caption = name
		} else {
			caption = fmt.Sprintf("%s:\n%s", name, caption)
		}
		return w.Store.CreateOutbox(ctx, "telegram", &chatID, map[string]any{
			"thread_id":           threadID,
			"body":                caption,
			"provider_message_id": event.ID,
			"media":               event.Media,
		})
	}
	body := fmt.Sprintf("%s:\n%s", name, event.Body)
	return w.Store.CreateOutbox(ctx, "telegram", &chatID, map[string]any{
		"thread_id":           threadID,
		"body":                body,
		"provider_message_id": event.ID,
	})
}

func (w Worker) handleTelegram(ctx context.Context, payload []byte) error {
	var event telegramEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	if event.Message == nil {
		if event.EditedMessage == nil {
			return nil
		}
		message, err := w.Store.MessageByTelegramID(ctx, event.EditedMessage.MessageID)
		if err != nil {
			return err
		}
		recipient, err := w.Store.ChatTarget(ctx, message.ChatID)
		if err != nil {
			return err
		}
		if err := w.WhatsApp.EditText(ctx, recipient, message.ProviderMessageID, event.EditedMessage.Text); err != nil {
			return err
		}
		return w.Store.UpdateMessageBody(ctx, message.ProviderMessageID, event.EditedMessage.Text)
	}
	chatID, err := w.Store.ResolveTopic(ctx, w.GroupID, event.Message.MessageThreadID)
	if strings.TrimSpace(event.Message.Text) != "" && strings.HasPrefix(event.Message.Text, "/") {
		if errors.Is(err, sql.ErrNoRows) && w.SystemThreadID == event.Message.MessageThreadID {
			return w.handleSystemCommand(ctx, event.Message.Text)
		}
		if err != nil {
			return err
		}
		return w.handleCommand(ctx, chatID, event.Message.Text)
	}
	if err != nil {
		return err
	}
	recipient, err := w.Store.ChatTarget(ctx, chatID)
	if err != nil {
		return err
	}
	mediaKind, file := telegramMedia(event.Message)
	if mediaKind != "" {
		return w.Store.CreateOutbox(ctx, "whatsapp", &chatID, map[string]any{
			"to": recipient, "body": event.Message.Caption, "media_kind": mediaKind,
			"media_id": file.FileID, "mimetype": file.MimeType, "filename": file.FileName,
		})
	}
	if strings.TrimSpace(event.Message.Text) == "" {
		return nil
	}
	return w.Store.CreateOutbox(ctx, "whatsapp", &chatID, map[string]string{"to": recipient, "body": event.Message.Text})
}

func (w Worker) handleCommand(ctx context.Context, chatID int64, command string) error {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}
	switch parts[0] {
	case "/help":
		return w.reply(ctx, chatID, "Commands: /help /status /details /mute /unmute /retry")
	case "/status":
		return w.reply(ctx, chatID, "Bridge is online")
	case "/chats":
		chats, err := w.Store.ListChats(ctx)
		if err != nil {
			return err
		}
		if len(chats) == 0 {
			return w.reply(ctx, chatID, "No chats")
		}
		var lines []string
		for _, chat := range chats {
			lines = append(lines, fmt.Sprintf("%d - %s", chat.ID, chat.DisplayName))
		}
		return w.reply(ctx, chatID, strings.Join(lines, "\n"))
	case "/details":
		target, err := w.Store.ChatTarget(ctx, chatID)
		if err != nil {
			return err
		}
		return w.reply(ctx, chatID, "WhatsApp chat: "+target)
	case "/mute":
		if err := w.Store.SetMuted(ctx, chatID, true); err != nil {
			return err
		}
		return w.reply(ctx, chatID, "Chat muted")
	case "/unmute":
		if err := w.Store.SetMuted(ctx, chatID, false); err != nil {
			return err
		}
		return w.reply(ctx, chatID, "Chat unmuted")
	case "/retry":
		if len(parts) != 2 {
			return w.reply(ctx, chatID, "Usage: /retry <delivery_id>")
		}
		deliveryID, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || deliveryID <= 0 {
			return w.reply(ctx, chatID, "Invalid delivery id")
		}
		if err := w.Store.RetryDelivery(ctx, deliveryID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return w.reply(ctx, chatID, "Delivery is not retryable")
			}
			return err
		}
		return w.reply(ctx, chatID, "Delivery queued for retry")
	case "/send":
		if len(parts) == 1 {
			return w.reply(ctx, chatID, "Usage: /send <text>")
		}
		target, err := w.Store.ChatTarget(ctx, chatID)
		if err != nil {
			return err
		}
		return w.Store.CreateOutbox(ctx, "whatsapp", &chatID, map[string]string{"to": target, "body": strings.TrimSpace(strings.TrimPrefix(command, parts[0]))})
	default:
		return w.reply(ctx, chatID, "Unknown command. Use /help")
	}
}

func (w Worker) handleSystemCommand(ctx context.Context, command string) error {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}

	var text string
	switch parts[0] {
	case "/help":
		text = "Commands: /chats /status /sync /help"
	case "/status":
		text = "Bridge is online"
	case "/chats":
		chats, err := w.Store.ListChats(ctx)
		if err != nil {
			return err
		}
		if len(chats) == 0 {
			text = "No chats"
			break
		}
		var lines []string
		for _, chat := range chats {
			lines = append(lines, fmt.Sprintf("%d - %s", chat.ID, chat.DisplayName))
		}
		text = strings.Join(lines, "\n")
	case "/sync":
		chats, err := w.Store.ListChats(ctx)
		if err != nil {
			return err
		}
		requested := 0
		for _, chat := range chats {
			last, lastErr := w.Store.LastMessage(ctx, chat.ID)
			if lastErr != nil {
				continue
			}
			if err := w.WhatsApp.RequestHistory(ctx, chat.ProviderChatID, last.ProviderMessageID, 50); err != nil {
				log.Printf("history sync %s: %v", chat.ProviderChatID, err)
				continue
			}
			requested++
		}
		text = fmt.Sprintf("History sync requested for %d chats", requested)
	default:
		text = "Unknown command. Use /help"
	}
	return w.Store.CreateOutbox(ctx, "telegram", nil, map[string]any{
		"thread_id": w.SystemThreadID,
		"body":      text,
	})
}

func (w Worker) reply(ctx context.Context, chatID int64, text string) error {
	threadID, err := w.Store.TopicForChat(ctx, chatID)
	if err != nil {
		return err
	}
	return w.Store.CreateOutbox(ctx, "telegram", &chatID, map[string]any{"thread_id": threadID, "body": text})
}

func telegramMedia(message *telegramMessage) (kind string, file telegramFile) {
	switch {
	case len(message.Photo) > 0:
		return "image", message.Photo[len(message.Photo)-1]
	case message.Sticker != nil:
		if message.Sticker.IsAnimated || message.Sticker.IsVideo {
			return "document", message.Sticker.telegramFile
		}
		return "sticker", message.Sticker.telegramFile
	case message.Animation != nil:
		return "animation", *message.Animation
	case message.VideoNote != nil:
		return "video_note", *message.VideoNote
	case message.Video != nil:
		return "video", *message.Video
	case message.Voice != nil:
		return "voice", *message.Voice
	case message.Audio != nil:
		return "audio", *message.Audio
	case message.Document != nil:
		return "document", *message.Document
	default:
		return "", telegramFile{}
	}
}

func safeTopicName(name string) string {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		name = "WhatsApp chat"
	}
	if len(name) > 128 {
		name = name[:128]
	}
	return name
}

func (w Worker) processOutbox(ctx context.Context) {
	job, err := w.Store.ClaimOutbox(ctx)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return
	}
	var payload struct {
		To                string              `json:"to"`
		Body              string              `json:"body"`
		ThreadID          int64               `json:"thread_id"`
		ProviderMessageID string              `json:"provider_message_id"`
		MediaKind         string              `json:"media_kind"`
		MediaID           string              `json:"media_id"`
		Mimetype          string              `json:"mimetype"`
		Filename          string              `json:"filename"`
		Media             *provider.MediaInfo `json:"media"`
		Action            *struct {
			Type      string `json:"type"`
			MessageID int64  `json:"message_id"`
			Body      string `json:"body"`
		} `json:"action"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		_ = w.Store.FailOutbox(ctx, job.ID, job.Attempts+1, err)
		return
	}
	if job.Provider == "whatsapp" {
		log.Printf("sending WhatsApp outbox %d to=%s media=%s", job.ID, payload.To, payload.MediaKind)
		var providerID string
		if payload.MediaKind != "" {
			var media provider.Media
			media, err = w.downloadTelegramMedia(ctx, payload.MediaID, payload.MediaKind, payload.Mimetype, payload.Filename, payload.Body)
			if err == nil {
				providerID, err = w.WhatsApp.SendMedia(ctx, payload.To, media)
			}
		} else {
			providerID, err = w.WhatsApp.SendText(ctx, payload.To, payload.Body)
		}
		if err == nil && job.ChatID.Valid {
			err = w.Store.AddOutboundMessage(ctx, job.ChatID.Int64, providerID, payload.Body)
		}
		if err != nil {
			log.Printf("send WhatsApp message to %s: %v", payload.To, err)
		}
	} else if job.Provider == "telegram" {
		if payload.Action != nil {
			switch payload.Action.Type {
			case "edit":
				err = w.Telegram.EditText(ctx, w.GroupID, payload.Action.MessageID, payload.Action.Body)
			case "delete":
				err = w.Telegram.DeleteMessage(ctx, w.GroupID, payload.Action.MessageID)
			default:
				err = errors.New("unsupported Telegram action")
			}
			if err != nil {
				log.Printf("telegram action %s failed: %v", payload.Action.Type, err)
			}
		} else {
			var telegramID int64
			if payload.Media != nil {
				telegramID, err = w.sendTelegramMedia(ctx, payload.ThreadID, payload.Body, payload.Media)
			} else {
				telegramID, err = w.Telegram.SendText(ctx, w.GroupID, payload.ThreadID, payload.Body)
			}
			if err == nil && payload.ProviderMessageID != "" {
				err = w.Store.SetTelegramMessageID(ctx, payload.ProviderMessageID, telegramID)
			}
		}
	} else {
		err = errors.New("unsupported outbox provider")
	}
	if err != nil {
		log.Printf("outbox %d failed: %v", job.ID, err)
		_ = w.Store.FailOutbox(ctx, job.ID, job.Attempts+1, err)
		return
	}
	if err := w.Store.CompleteOutbox(ctx, job.ID); err != nil {
		log.Printf("complete outbox %d: %v", job.ID, err)
	}
}

// ponytail: buffer media in memory, stream through object storage if 50 MB files strain the worker
func (w Worker) downloadTelegramMedia(ctx context.Context, fileID, kind, mimetype, filename, caption string) (provider.Media, error) {
	file, err := w.Telegram.GetFile(ctx, fileID)
	if err != nil {
		return provider.Media{}, err
	}
	content, err := w.Telegram.DownloadFile(ctx, file.Path)
	if err != nil {
		return provider.Media{}, err
	}
	defer content.Close()
	data, err := io.ReadAll(io.LimitReader(content, provider.TelegramUploadLimit))
	if err != nil {
		return provider.Media{}, err
	}
	return provider.Media{Kind: kind, Mimetype: mimetype, Filename: filename, Caption: caption, Data: data}, nil
}

func (w Worker) sendTelegramMedia(ctx context.Context, threadID int64, caption string, info *provider.MediaInfo) (int64, error) {
	if info.Length > provider.TelegramUploadLimit {
		return w.Telegram.SendText(ctx, w.GroupID, threadID, fmt.Sprintf("%s\n[%s is too large to forward: %d bytes]", caption, info.Kind, info.Length))
	}
	data, err := w.WhatsApp.DownloadMedia(ctx, info.Encoded)
	if err != nil {
		return 0, err
	}
	if !provider.SupportsCaption(info.Kind) && caption != "" {
		if _, err := w.Telegram.SendText(ctx, w.GroupID, threadID, caption); err != nil {
			return 0, err
		}
	}
	return w.Telegram.SendMedia(ctx, w.GroupID, threadID, provider.Media{
		Kind: info.Kind, Mimetype: info.Mimetype, Filename: info.Filename, Caption: caption, Data: data,
	})
}
