package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
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

func (w Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		w.process(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w Worker) process(ctx context.Context) {
	job, err := w.Store.ClaimOutbox(ctx)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return
	}
	var payload struct {
		To          string `json:"to"`
		Body        string `json:"body"`
		ThreadID    int64  `json:"thread_id"`
		TelegramMsg string `json:"telegram_message_id"`
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
