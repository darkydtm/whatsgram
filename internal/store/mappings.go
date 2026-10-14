package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

type MessageState struct {
	ID        int64
	ChatID    int64
	Version   domain.SourceVersion
	CreatedAt time.Time
	Tombstone bool
	Kept      bool
}

func (s *Store) Message(ctx context.Context, ref domain.MessageRef) (MessageState, error) {
	if ref.Validate() != nil {
		return MessageState{}, ErrInvalid
	}
	var state MessageState
	var version []byte
	var createdMs int64
	err := s.db.QueryRowContext(ctx, `SELECT m.id,m.chat_id,m.created_at_ms,m.state_version,m.tombstone,m.kept FROM bridge_part p JOIN bridge_message m ON m.id=p.message_id WHERE p.platform=? AND p.remote_chat=? AND p.remote_author=? AND p.remote_id=?`, ref.Platform, ref.Chat, ref.Author, ref.ID).Scan(&state.ID, &state.ChatID, &createdMs, &version, &state.Tombstone, &state.Kept)
	if err == sql.ErrNoRows {
		return MessageState{}, ErrNoWork
	}
	if err != nil {
		return MessageState{}, safe(err)
	}
	if err := json.Unmarshal(version, &state.Version); err != nil {
		return MessageState{}, ErrStorage
	}
	if createdMs > 0 {
		state.CreatedAt = time.UnixMilli(createdMs)
	}
	return state, nil
}
func (s *Store) FindParts(ctx context.Context, ref domain.MessageRef) ([]domain.DeliveredPart, error) {
	if ref.Validate() != nil {
		return nil, ErrInvalid
	}
	var messageID int64
	err := s.db.QueryRowContext(ctx, "SELECT message_id FROM bridge_part WHERE platform=? AND remote_chat=? AND remote_author=? AND remote_id=?", ref.Platform, ref.Chat, ref.Author, ref.ID).Scan(&messageID)
	if err == sql.ErrNoRows {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, safe(err)
	}
	return readParts(ctx, s.db, messageID)
}

func (s *Store) FindPartsByRemote(ctx context.Context, platform domain.Platform, chat, id string) ([]domain.DeliveredPart, error) {
	if !platform.Valid() || chat == "" || id == "" {
		return nil, ErrInvalid
	}
	var messageID int64
	err := s.db.QueryRowContext(ctx, "SELECT message_id FROM bridge_part WHERE platform=? AND remote_chat=? AND remote_id=? LIMIT 1", platform, chat, id).Scan(&messageID)
	if err == sql.ErrNoRows {
		return nil, ErrNoWork
	}
	if err != nil {
		return nil, safe(err)
	}
	return readParts(ctx, s.db, messageID)
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readParts(ctx context.Context, q queryer, messageID int64) ([]domain.DeliveredPart, error) {
	rows, err := q.QueryContext(ctx, "SELECT platform,remote_chat,remote_author,remote_id,part_index,role,is_bridge_owned,capabilities,COALESCE(topic_generation,0),deleted,retired FROM bridge_part WHERE message_id=? ORDER BY platform,part_index", messageID)
	if err != nil {
		return nil, safe(err)
	}
	defer rows.Close()
	parts := []domain.DeliveredPart{}
	for rows.Next() {
		var p domain.DeliveredPart
		if err := rows.Scan(&p.Ref.Platform, &p.Ref.Chat, &p.Ref.Author, &p.Ref.ID, &p.Index, &p.Role, &p.BridgeOwned, &p.Capabilities, &p.TopicGeneration, &p.Deleted, &p.Retired); err != nil {
			return nil, safe(err)
		}
		parts = append(parts, p)
	}
	return parts, safe(rows.Err())
}

func (s *Store) Part(ctx context.Context, ref domain.MessageRef) (domain.DeliveredPart, error) {
	if ref.Validate() != nil {
		return domain.DeliveredPart{}, ErrInvalid
	}
	var p domain.DeliveredPart
	p.Ref = ref
	err := s.db.QueryRowContext(ctx, "SELECT part_index,role,is_bridge_owned,capabilities,COALESCE(topic_generation,0),deleted,retired FROM bridge_part WHERE platform=? AND remote_chat=? AND remote_author=? AND remote_id=?", ref.Platform, ref.Chat, ref.Author, ref.ID).Scan(&p.Index, &p.Role, &p.BridgeOwned, &p.Capabilities, &p.TopicGeneration, &p.Deleted, &p.Retired)
	if err == sql.ErrNoRows {
		return domain.DeliveredPart{}, ErrNoWork
	}
	return p, safe(err)
}
func (s *Store) SourceState(ctx context.Context, ref domain.MessageRef) (domain.SourceVersion, bool, error) {
	if ref.Validate() != nil {
		return domain.SourceVersion{}, false, ErrInvalid
	}
	var payload []byte
	var version domain.SourceVersion
	var deleted bool
	err := s.db.QueryRowContext(ctx, "SELECT ss.version,ss.deleted FROM bridge_source_state ss JOIN bridge_part p ON p.id=ss.part_id WHERE p.platform=? AND p.remote_chat=? AND p.remote_author=? AND p.remote_id=?", ref.Platform, ref.Chat, ref.Author, ref.ID).Scan(&payload, &deleted)
	if err == sql.ErrNoRows {
		return version, false, ErrNoWork
	}
	if err != nil {
		return version, false, safe(err)
	}
	if err := json.Unmarshal(payload, &version); err != nil {
		return version, false, ErrStorage
	}
	return version, deleted, nil
}
func (s *Store) RetirePart(ctx context.Context, ref domain.MessageRef, expected domain.SourceVersion) error {
	if ref.Validate() != nil || expected.Validate() != nil {
		return ErrInvalid
	}
	version, _ := json.Marshal(expected)
	result, err := s.db.ExecContext(ctx, `UPDATE bridge_part SET retired=1 WHERE platform=? AND remote_chat=? AND remote_author=? AND remote_id=? AND EXISTS (SELECT 1 FROM bridge_source_state ss WHERE ss.part_id=bridge_part.id AND ss.version=?)`, ref.Platform, ref.Chat, ref.Author, ref.ID, version)
	if err != nil {
		return safe(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return safe(err)
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) TombstoneMessage(ctx context.Context, messageID int64) error {
	if messageID <= 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE bridge_outbox SET state='superseded',lease_token=NULL,lease_until_ms=NULL WHERE message_id=? AND send_started_at_ms IS NULL AND operation IN ('message.create','message.edit','reaction.set') AND state IN ('pending','leased')", messageID); err != nil {
		return safe(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE bridge_source_state SET deleted=1 WHERE part_id IN (SELECT id FROM bridge_part WHERE message_id=?)", messageID); err != nil {
		return safe(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE bridge_part SET deleted=1 WHERE message_id=?", messageID); err != nil {
		return safe(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE bridge_message SET tombstone=1 WHERE id=?", messageID); err != nil {
		return safe(err)
	}
	return safe(tx.Commit())
}
