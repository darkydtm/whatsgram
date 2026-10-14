package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func acknowledgeSource(ctx context.Context, tx *sql.Tx, messageID int64, ref domain.MessageRef, applied domain.SourceVersion, deleted bool) error {
	var partID int64
	var payload []byte
	if err := tx.QueryRowContext(ctx, `SELECT ss.part_id,ss.version FROM bridge_source_state ss JOIN bridge_part p ON p.id=ss.part_id WHERE p.message_id=? AND p.platform=? AND p.remote_chat=? AND p.remote_author=? AND p.remote_id=?`, messageID, ref.Platform, ref.Chat, ref.Author, ref.ID).Scan(&partID, &payload); err != nil {
		return ErrConflict
	}
	var observed domain.SourceVersion
	if err := json.Unmarshal(payload, &observed); err != nil {
		return ErrStorage
	}
	order, err := applied.Compare(observed)
	if err != nil {
		return err
	}
	if order > 0 {
		payload, _ = json.Marshal(applied)
	}
	_, err = tx.ExecContext(ctx, "UPDATE bridge_source_state SET version=?,deleted=MAX(deleted,?) WHERE part_id=?", payload, deleted, partID)
	return safe(err)
}
