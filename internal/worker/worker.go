package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/darky/whatsgram/internal/provider"
	"github.com/darky/whatsgram/internal/store"
)

type Worker struct {
	Store    *store.Store
	WhatsApp provider.WhatsApp
	Telegram provider.Telegram
	GroupID  int64
}

type whatsappEvent struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Contacts []struct {
					WaID string `json:"wa_id"`
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
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

type telegramEvent struct {
	Message *struct {
		MessageThreadID int64 `json:"message_thread_id"`
		Text            string `json:"text"`
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
			for _, message := range change.Value.Messages {
				name := message.From
				if len(change.Value.Contacts) > 0 && change.Value.Contacts[0].Profile.Name != "" {
					name = change.Value.Contacts[0].Profile.Name
				}
				chatID, err := w.Store.UpsertChat(ctx, message.From, name)
				if err != nil {
					return err
				}
				if err := w.Store.AddMessage(ctx, chatID, "inbound", message.ID, message.Text.Body); err != nil {
					return err
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
				text := fmt.Sprintf("%s:\n%s", name, message.Text.Body)
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
	if event.Message == nil || strings.TrimSpace(event.Message.Text) == "" {
		return nil
	}
	chatID, err := w.Store.ResolveTopic(ctx, w.GroupID, event.Message.MessageThreadID)
	if err != nil {
		return err
	}
	if strings.HasPrefix(event.Message.Text, "/") {
		return w.handleCommand(ctx, chatID, event.Message.Text)
	}
	recipient, err := w.Store.ChatTarget(ctx, chatID)
	if err != nil {
		return err
	}
	return w.Store.CreateOutbox(ctx, "whatsapp", &chatID, map[string]string{
		"to":   recipient,
		"body": event.Message.Text,
	})
}

func (w Worker) handleCommand(ctx context.Context, chatID int64, command string) error {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}
	switch parts[0] {
	case "/mute":
		return w.Store.SetMuted(ctx, chatID, true)
	case "/unmute":
		return w.Store.SetMuted(ctx, chatID, false)
	default:
		return nil
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
		To       string `json:"to"`
		Body     string `json:"body"`
		ThreadID int64  `json:"thread_id"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		_ = w.Store.FailOutbox(ctx, job.ID, job.Attempts+1, err)
		return
	}
	if job.Provider == "whatsapp" {
		err = w.WhatsApp.SendText(ctx, payload.To, payload.Body)
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
