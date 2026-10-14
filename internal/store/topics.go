package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func (s *Store) CompleteTopicDelivery(ctx context.Context, id int64, token string, topic domain.TopicRef, now time.Time) error {
	if now.IsZero() || topic.ThreadID <= 0 || topic.ChatID == 0 || topic.Generation <= 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	now = fenceTime(now)
	var payload []byte
	var chatID, generation int64
	var state ChatState
	err = tx.QueryRowContext(ctx, `SELECT o.payload,c.tg_chat_id,c.topic_generation,c.state FROM bridge_outbox o JOIN bridge_chat c ON c.id=o.chat_id WHERE o.id=? AND o.state='sending' AND o.lease_token=? AND o.lease_until_ms>?`, id, token, now.UnixMilli()).Scan(&payload, &chatID, &generation, &state)
	if err == sql.ErrNoRows {
		return ErrLeaseLost
	}
	if err != nil {
		return safe(err)
	}
	var d domain.Delivery
	if err := json.Unmarshal(payload, &d); err != nil {
		return ErrStorage
	}
	if d.Platform != domain.Telegram || d.MessageID != 0 || (d.Kind != "topic.create" && d.Kind != "topic.rename") || d.TopicGeneration != generation || chatID != topic.ChatID {
		return ErrConflict
	}
	if d.Kind == "topic.create" {
		if state != ChatDiscovered || topic.Generation != generation+1 {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "UPDATE bridge_chat SET topic_id=?,topic_generation=?,state='active' WHERE id=?", topic.ThreadID, topic.Generation, d.ChatID); err != nil {
			return safe(err)
		}
	} else {
		var threadID int64
		if err := tx.QueryRowContext(ctx, "SELECT topic_id FROM bridge_chat WHERE id=?", d.ChatID).Scan(&threadID); err != nil {
			return safe(err)
		}
		if topic.Generation != generation || topic.ThreadID != threadID || state != ChatActive {
			return ErrConflict
		}
	}
	result, _ := json.Marshal(topic)
	if err := changed(tx.ExecContext(ctx, "UPDATE bridge_outbox SET state='succeeded',result=?,lease_token=NULL,lease_until_ms=NULL WHERE id=? AND state='sending' AND lease_token=? AND lease_until_ms>?", result, id, token, fenceTime(now).UnixMilli())); err != nil {
		return err
	}
	return safe(tx.Commit())
}
