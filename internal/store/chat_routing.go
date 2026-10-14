package store

import (
	"context"
	"database/sql"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func routeChatEvent(ctx context.Context, tx *sql.Tx, e domain.Event, deliveries []domain.Delivery) (int64, error) {
	if e.Target != nil || len(deliveries) == 0 {
		return 0, ErrInvalid
	}
	operation := ""
	switch e.Kind {
	case domain.ChatTitle:
		operation = "topic.rename"
	case domain.ChatDiscovered:
		if e.Source != domain.WhatsApp {
			return 0, ErrInvalid
		}
		operation = "topic.create"
	default:
		return 0, ErrInvalid
	}
	var chatID int64
	var err error
	if e.Source == domain.WhatsApp {
		err = tx.QueryRowContext(ctx, "SELECT id FROM bridge_chat WHERE wa_jid=?", e.Chat).Scan(&chatID)
	} else {
		if e.TelegramThreadID <= 0 {
			return 0, ErrInvalid
		}
		err = tx.QueryRowContext(ctx, "SELECT id FROM bridge_chat WHERE CAST(tg_chat_id AS TEXT)=? AND topic_id=?", e.Chat, e.TelegramThreadID).Scan(&chatID)
	}
	if err == sql.ErrNoRows {
		return 0, ErrConflict
	}
	if err != nil {
		return 0, safe(err)
	}
	for _, d := range deliveries {
		if d.ChatID != chatID {
			return 0, ErrConflict
		}
		if d.Kind != operation || d.Platform != domain.Telegram || d.MessageID != 0 || d.Target != nil {
			return 0, ErrInvalid
		}
	}
	return chatID, nil
}
