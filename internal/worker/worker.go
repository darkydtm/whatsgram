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
	From     string `json:"from"`
	Name     string `json:"name"`
	ChatName string `json:"chat_name"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Body     string `json:"body"`
	Status   string `json:"status"`
	MediaID  string `json:"media_id"`
	Mimetype string `json:"mimetype"`
	Caption  string `json:"caption"`
	Edited   bool   `json:"edited"`
	Deleted  bool   `json:"deleted"`
	TargetID string `json:"target_id"`
	Action   string `json:"action"`
	Reaction string `json:"reaction"`
}

type telegramEvent struct {
	Message *struct {
		MessageID       int64  `json:"message_id"`
		MessageThreadID int64  `json:"message_thread_id"`
		Text            string `json:"text"`
		Caption         string `json:"caption"`
		Photo           []struct {
			FileID string `json:"file_id"`
		} `json:"photo"`
		Document *struct {
			FileID string `json:"file_id"`
		} `json:"document"`
		Video *struct {
			FileID string `json:"file_id"`
		} `json:"video"`
		Audio *struct {
			FileID string `json:"file_id"`
		} `json:"audio"`
		Voice *struct {
			FileID string `json:"file_id"`
		} `json:"voice"`
	} `json:"message"`
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
		if err := w.Store.CreateMedia(ctx, chatID, "whatsapp", event.MediaID, event.Mimetype, event.Caption); err != nil {
			return err
		}
	}
	if strings.TrimSpace(event.Body) == "" && strings.TrimSpace(event.Caption) != "" {
		event.Body = event.Caption
	}
	if strings.TrimSpace(event.Body) == "" {
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
	mediaType, fileID := telegramMedia(event.Message)
	if mediaType != "" {
		return w.Store.CreateOutbox(ctx, "whatsapp", &chatID, map[string]string{
			"to": recipient, "body": event.Message.Caption, "media_type": mediaType, "media_id": fileID,
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

func telegramMedia(message *struct {
	MessageID       int64  `json:"message_id"`
	MessageThreadID int64  `json:"message_thread_id"`
	Text            string `json:"text"`
	Caption         string `json:"caption"`
	Photo           []struct {
		FileID string `json:"file_id"`
	} `json:"photo"`
	Document *struct {
		FileID string `json:"file_id"`
	} `json:"document"`
	Video *struct {
		FileID string `json:"file_id"`
	} `json:"video"`
	Audio *struct {
		FileID string `json:"file_id"`
	} `json:"audio"`
	Voice *struct {
		FileID string `json:"file_id"`
	} `json:"voice"`
}) (string, string) {
	if len(message.Photo) > 0 {
		return "image", message.Photo[len(message.Photo)-1].FileID
	}
	if message.Document != nil {
		return "document", message.Document.FileID
	}
	if message.Video != nil {
		return "video", message.Video.FileID
	}
	if message.Audio != nil {
		return "audio", message.Audio.FileID
	}
	if message.Voice != nil {
		return "audio", message.Voice.FileID
	}
	return "", ""
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
		To                string `json:"to"`
		Body              string `json:"body"`
		ThreadID          int64  `json:"thread_id"`
		ProviderMessageID string `json:"provider_message_id"`
		MediaType         string `json:"media_type"`
		MediaID           string `json:"media_id"`
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
		log.Printf("sending WhatsApp outbox %d to=%s media=%s", job.ID, payload.To, payload.MediaType)
		if payload.MediaType != "" {
			file, fileErr := w.Telegram.GetFile(ctx, payload.MediaID)
			if fileErr != nil {
				err = fileErr
			} else {
				var content io.ReadCloser
				content, err = w.Telegram.DownloadFile(ctx, file.Path)
				if err == nil {
					defer content.Close()
					var providerID string
					providerID, err = w.WhatsApp.SendMedia(ctx, payload.To, payload.MediaType, content, payload.Body)
					if err == nil && job.ChatID.Valid {
						err = w.Store.AddOutboundMessage(ctx, job.ChatID.Int64, providerID, payload.Body)
					}
				}
			}
		} else {
			var providerID string
			providerID, err = w.WhatsApp.SendText(ctx, payload.To, payload.Body)
			if err == nil && job.ChatID.Valid {
				err = w.Store.AddOutboundMessage(ctx, job.ChatID.Int64, providerID, payload.Body)
			}
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
			telegramID, err = w.Telegram.SendText(ctx, w.GroupID, payload.ThreadID, payload.Body)
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
