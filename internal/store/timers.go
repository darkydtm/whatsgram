package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

type TimerKind string

const (
	TimerExpire      TimerKind = "expire"
	TimerMappingWait TimerKind = "mapping_wait"
)

type TimerSpec struct {
	MessageID int64
	Kind      TimerKind
	Version   domain.SourceVersion
	Deadline  time.Time
}
type Timer struct {
	ID         int64
	MessageID  int64
	Kind       TimerKind
	Version    domain.SourceVersion
	Deadline   time.Time
	LeaseToken string
	LeaseUntil time.Time
	Parts      []domain.DeliveredPart
}

func (s *Store) ScheduleTimer(ctx context.Context, spec TimerSpec) (int64, error) {
	if spec.MessageID <= 0 || spec.Version.Validate() != nil || spec.Deadline.IsZero() || (spec.Kind != TimerExpire && spec.Kind != TimerMappingWait) {
		return 0, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, safe(err)
	}
	defer tx.Rollback()
	version, _ := json.Marshal(spec.Version)
	var id, deadline int64
	err = tx.QueryRowContext(ctx, "SELECT id,deadline_ms FROM bridge_timer WHERE message_id=? AND kind=? AND target_version=?", spec.MessageID, spec.Kind, version).Scan(&id, &deadline)
	if err == nil {
		if deadline != spec.Deadline.UnixMilli() {
			return 0, ErrConflict
		}
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, safe(err)
	}
	var current []byte
	var kept, tombstone bool
	if err := tx.QueryRowContext(ctx, "SELECT state_version,kept,tombstone FROM bridge_message WHERE id=?", spec.MessageID).Scan(&current, &kept, &tombstone); err != nil {
		return 0, safe(err)
	}
	if string(current) != string(version) || tombstone || (spec.Kind == TimerExpire && kept) {
		return 0, ErrConflict
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO bridge_timer(message_id,kind,target_version,deadline_ms,state) VALUES (?,?,?,?,'pending')", spec.MessageID, spec.Kind, version, spec.Deadline.UnixMilli())
	if err != nil {
		return 0, safe(err)
	}
	id, err = result.LastInsertId()
	if err != nil {
		return 0, safe(err)
	}
	if err := tx.Commit(); err != nil {
		return 0, safe(err)
	}
	return id, nil
}
func (s *Store) ClaimTimer(ctx context.Context, now time.Time, ttl time.Duration) (Timer, error) {
	token, until, err := lease(now, ttl)
	if err != nil {
		return Timer{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Timer{}, safe(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE bridge_timer SET state='cancelled' WHERE state='pending' AND EXISTS (SELECT 1 FROM bridge_message m WHERE m.id=bridge_timer.message_id AND (m.tombstone=1 OR m.state_version!=bridge_timer.target_version OR (bridge_timer.kind='expire' AND m.kept=1)))`); err != nil {
		return Timer{}, safe(err)
	}
	var timer Timer
	var version []byte
	var deadline int64
	err = tx.QueryRowContext(ctx, `SELECT t.id,t.message_id,t.kind,t.target_version,t.deadline_ms FROM bridge_timer t WHERE t.state='pending' AND t.deadline_ms<=? AND NOT EXISTS (SELECT 1 FROM bridge_timer other WHERE other.message_id=t.message_id AND other.state='leased') ORDER BY t.deadline_ms,t.id LIMIT 1`, now.UnixMilli()).Scan(&timer.ID, &timer.MessageID, &timer.Kind, &version, &deadline)
	if err == sql.ErrNoRows {
		if err := tx.Commit(); err != nil {
			return Timer{}, safe(err)
		}
		return Timer{}, ErrNoWork
	}
	if err != nil {
		return Timer{}, safe(err)
	}
	if err := json.Unmarshal(version, &timer.Version); err != nil {
		return Timer{}, ErrStorage
	}
	timer.Deadline = time.UnixMilli(deadline)
	timer.LeaseToken = token
	timer.LeaseUntil = time.UnixMilli(until)
	timer.Parts, err = readParts(ctx, tx, timer.MessageID)
	if err != nil {
		return Timer{}, err
	}
	if err := changed(tx.ExecContext(ctx, "UPDATE bridge_timer SET state='leased',lease_token=?,lease_until_ms=? WHERE id=? AND state='pending'", token, until, timer.ID)); err != nil {
		return Timer{}, err
	}
	if err := tx.Commit(); err != nil {
		return Timer{}, safe(err)
	}
	return timer, nil
}
func (s *Store) CompleteTimer(ctx context.Context, id int64, token string, deliveries []domain.Delivery, now time.Time) error {
	if now.IsZero() {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	var messageID, chatID, inboxID int64
	var kind TimerKind
	var version, effective []byte
	var kept, tombstone bool
	err = tx.QueryRowContext(ctx, `SELECT t.message_id,t.kind,t.target_version,m.state_version,m.kept,m.tombstone,m.chat_id FROM bridge_timer t JOIN bridge_message m ON m.id=t.message_id WHERE t.id=? AND t.state='leased' AND t.lease_token=? AND t.lease_until_ms>?`, id, token, now.UnixMilli()).Scan(&messageID, &kind, &version, &effective, &kept, &tombstone, &chatID)
	if err == sql.ErrNoRows {
		return ErrLeaseLost
	}
	if err != nil {
		return safe(err)
	}
	if tombstone || string(version) != string(effective) || (kind == TimerExpire && kept) {
		return ErrConflict
	}
	if len(deliveries) > 0 {
		if err := tx.QueryRowContext(ctx, "SELECT inbox_id FROM bridge_outbox WHERE message_id=? ORDER BY id LIMIT 1", messageID).Scan(&inboxID); err != nil {
			return safe(err)
		}
	}
	var targetVersion domain.SourceVersion
	if err := json.Unmarshal(version, &targetVersion); err != nil {
		return ErrStorage
	}
	for _, d := range deliveries {
		if kind != TimerExpire || d.MessageID != messageID || d.ChatID != chatID || d.Kind != "message.revoke" || d.Target == nil || d.ExpectedVersion != targetVersion {
			return ErrInvalid
		}
		var owned, deleted, retired bool
		if err := tx.QueryRowContext(ctx, "SELECT is_bridge_owned,deleted,retired FROM bridge_part WHERE message_id=? AND platform=? AND remote_chat=? AND remote_author=? AND remote_id=?", messageID, d.Target.Platform, d.Target.Chat, d.Target.Author, d.Target.ID).Scan(&owned, &deleted, &retired); err != nil {
			return ErrConflict
		}
		if !owned || deleted || retired {
			return ErrConflict
		}
		if err := s.insertDelivery(ctx, tx, inboxID, d, now); err != nil {
			return err
		}
	}
	if kind == TimerExpire {
		if _, err := tx.ExecContext(ctx, "UPDATE bridge_message SET tombstone=1,state_payload=? WHERE id=?", []byte("null"), messageID); err != nil {
			return safe(err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE bridge_part SET deleted=1 WHERE message_id=?", messageID); err != nil {
			return safe(err)
		}
	}
	if err := changed(tx.ExecContext(ctx, "UPDATE bridge_timer SET state='done',lease_token=NULL,lease_until_ms=NULL WHERE id=? AND state='leased' AND lease_token=? AND lease_until_ms>?", id, token, now.UnixMilli())); err != nil {
		return err
	}
	return safe(tx.Commit())
}
func (s *Store) SetKept(ctx context.Context, ref domain.MessageRef, expected domain.SourceVersion, kept bool) error {
	if ref.Validate() != nil || expected.Validate() != nil {
		return ErrInvalid
	}
	version, _ := json.Marshal(expected)
	result, err := s.db.ExecContext(ctx, `UPDATE bridge_message SET kept=? WHERE id=(SELECT message_id FROM bridge_part WHERE platform=? AND remote_chat=? AND remote_author=? AND remote_id=?) AND state_version=? AND tombstone=0`, kept, ref.Platform, ref.Chat, ref.Author, ref.ID, version)
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
