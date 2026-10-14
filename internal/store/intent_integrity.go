package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func validateStoredIntent(ctx context.Context, tx *sql.Tx, id int64, d domain.Delivery) error {
	var inboxID, chatID, messageID, generation int64
	var operation string
	var platform domain.Platform
	var admitted []byte
	if err := tx.QueryRowContext(ctx, `SELECT o.inbox_id,o.chat_id,COALESCE(o.message_id,0),o.topic_generation,o.operation,o.platform,i.payload FROM bridge_outbox o JOIN bridge_inbox i ON i.id=o.inbox_id WHERE o.id=?`, id).Scan(&inboxID, &chatID, &messageID, &generation, &operation, &platform, &admitted); err != nil {
		return safe(err)
	}
	if d.ChatID != chatID || d.MessageID != messageID || d.TopicGeneration != generation || d.Kind != operation || d.Platform != platform || d.ExpectedVersion.Validate() != nil {
		return ErrConflict
	}
	var e domain.Event
	if err := json.Unmarshal(admitted, &e); err != nil {
		return ErrStorage
	}
	if messageID == 0 {
		if d.ExpectedVersion != e.Version {
			return ErrConflict
		}
		return nil
	}
	var effective []byte
	if err := tx.QueryRowContext(ctx, "SELECT state_version FROM bridge_message WHERE id=? AND chat_id=?", messageID, chatID).Scan(&effective); err != nil {
		return ErrConflict
	}
	var current domain.SourceVersion
	if err := json.Unmarshal(effective, &current); err != nil {
		return ErrStorage
	}
	if d.ExpectedVersion == e.Version && d.Kind == string(e.Kind) {
		if d.Kind == "message.create" {
			order, err := current.Compare(d.ExpectedVersion)
			if err != nil || order < 0 {
				return ErrConflict
			}
		} else if current != d.ExpectedVersion {
			return ErrConflict
		}
		return nil
	}
	if d.Kind != "message.revoke" || current != d.ExpectedVersion {
		return ErrConflict
	}
	version, _ := json.Marshal(d.ExpectedVersion)
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_timer WHERE message_id=? AND kind='expire' AND target_version=? AND state='done'", messageID, version).Scan(&count); err != nil {
		return safe(err)
	}
	if count == 0 {
		return ErrConflict
	}
	return nil
}
