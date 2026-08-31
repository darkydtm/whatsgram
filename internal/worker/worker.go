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
	Entry []struct {
		Changes []struct {
			Value struct {
				Contacts []struct {
					WaID    string `json:"wa_id"`
					Profile struct {
						Name string `json:"name"`
					} `json:"profile"`
				} `json:"contacts"`
				Messages []struct {
					ID   string `json:"id"`
					From string `json:"from"`
					Text struct {
						Body string `json:"body"`
					} `json:"text"`
					Type  string `json:"type"`
					Image *struct {
						ID      string `json:"id"`
						Caption string `json:"caption"`
					} `json:"image"`
					Document *struct {
						ID      string `json:"id"`
						Caption string `json:"caption"`
					} `json:"document"`
					Video *struct {
						ID      string `json:"id"`
						Caption string `json:"caption"`
					} `json:"video"`
					Audio *struct {
						ID string `json:"id"`
					} `json:"audio"`
					Voice *struct {
						ID string `json:"id"`
					} `json:"voice"`
				} `json:"messages"`
				Statuses []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"statuses"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

type telegramEvent struct {
	Message *struct {
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
	for _, entry := range event.Entry {
		for _, change := range entry.Changes {
			for _, status := range change.Value.Statuses {
				if err := w.Store.SetMessageStatus(ctx, status.ID, status.Status); err != nil {
					return err
				}
			}
			for _, message := range change.Value.Messages {
				name := message.From
				if len(change.Value.Contacts) > 0 && change.Value.Contacts[0].Profile.Name != "" {
					name = change.Value.Contacts[0].Profile.Name
				}
				chatID, err := w.Store.UpsertChat(ctx, message.From, name)
				if err != nil {
					return err
				}
				body := message.Text.Body
				mediaID, mediaCaption := "", ""
				if message.Image != nil {
					mediaID, mediaCaption = message.Image.ID, message.Image.Caption
				}
				if message.Document != nil {
					mediaID, mediaCaption = message.Document.ID, message.Document.Caption
				}
				if message.Video != nil {
					mediaID, mediaCaption = message.Video.ID, message.Video.Caption
				}
				if message.Audio != nil {
					mediaID = message.Audio.ID
				}
				if message.Voice != nil {
					mediaID = message.Voice.ID
				}
				if body == "" {
					body = mediaCaption
				}
				inserted, err := w.Store.AddMessage(ctx, chatID, "inbound", message.ID, body)
				if err != nil {
					return err
				}
				if !inserted {
					continue
				}
				if muted, err := w.Store.IsMuted(ctx, chatID); err != nil {
					return err
				} else if muted {
					continue
				}
				if mediaID != "" {
					if err := w.Store.CreateMedia(ctx, chatID, "whatsapp", mediaID, message.Type, mediaCaption); err != nil {
						return err
					}
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
				text := fmt.Sprintf("%s:\n%s", name, body)
				if err := w.Store.CreateOutbox(ctx, "telegram", &chatID, map[string]any{
					"thread_id": threadID,
					"body":      text,
				}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (w Worker) handleTelegram(ctx context.Context, payload []byte) error {
	var event telegramEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	if event.Message == nil {
		return nil
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
		text = "Commands: /chats /status /help"
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
		To        string `json:"to"`
		Body      string `json:"body"`
		ThreadID  int64  `json:"thread_id"`
		MediaType string `json:"media_type"`
		MediaID   string `json:"media_id"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		_ = w.Store.FailOutbox(ctx, job.ID, job.Attempts+1, err)
		return
	}
	if job.Provider == "whatsapp" {
		if payload.MediaType != "" {
			file, fileErr := w.Telegram.GetFile(ctx, payload.MediaID)
			if fileErr != nil {
				err = fileErr
			} else {
				var content io.ReadCloser
				content, err = w.Telegram.DownloadFile(ctx, file.Path)
				if err == nil {
					defer content.Close()
					var mediaID string
					mediaID, err = w.WhatsApp.UploadMedia(ctx, payload.MediaType, content)
					if err == nil {
						err = w.WhatsApp.SendMedia(ctx, payload.To, payload.MediaType, mediaID, payload.Body)
					}
				}
			}
		} else {
			err = w.WhatsApp.SendText(ctx, payload.To, payload.Body)
		}
	} else if job.Provider == "telegram" {
		_, err = w.Telegram.SendText(ctx, w.GroupID, payload.ThreadID, payload.Body)
	} else {
		err = errors.New("unsupported outbox provider")
	}
	if err != nil {
		_ = w.Store.FailOutbox(ctx, job.ID, job.Attempts+1, err)
		return
	}
	if err := w.Store.CompleteOutbox(ctx, job.ID); err != nil {
		log.Printf("complete outbox %d: %v", job.ID, err)
	}
}
