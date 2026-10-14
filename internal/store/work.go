package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func fenceTime(now time.Time) time.Time {
	if current := time.Now(); current.After(now) {
		return current
	}
	return now
}
func lease(now time.Time, ttl time.Duration) (string, int64, error) {
	if now.IsZero() || ttl < time.Millisecond || ttl > time.Hour {
		return "", 0, ErrInvalid
	}
	data := make([]byte, 24)
	if _, err := rand.Read(data); err != nil {
		return "", 0, ErrStorage
	}
	return hex.EncodeToString(data), now.Add(ttl).UnixMilli(), nil
}
func changed(result sql.Result, err error) error {
	if err != nil {
		return safe(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return safe(err)
	}
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) ClaimInbox(ctx context.Context, now time.Time, ttl time.Duration) (domain.Event, string, error) {
	token, until, err := lease(now, ttl)
	if err != nil {
		return domain.Event{}, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Event{}, "", safe(err)
	}
	defer tx.Rollback()
	var id int64
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT i.id,i.payload FROM bridge_inbox i WHERE i.state='pending' AND i.available_at_ms<=? AND NOT EXISTS (SELECT 1 FROM bridge_inbox other WHERE other.state='leased' AND (other.target_key=i.target_key OR EXISTS (SELECT 1 FROM bridge_part p JOIN bridge_part other_part ON other_part.message_id=p.message_id WHERE json_array(p.platform,p.remote_chat,p.remote_author,p.remote_id)=i.target_key AND json_array(other_part.platform,other_part.remote_chat,other_part.remote_author,other_part.remote_id)=other.target_key))) ORDER BY i.id LIMIT 1`, now.UnixMilli()).Scan(&id, &payload)
	if err == sql.ErrNoRows {
		return domain.Event{}, "", ErrNoWork
	}
	if err != nil {
		return domain.Event{}, "", safe(err)
	}
	var e domain.Event
	if err := json.Unmarshal(payload, &e); err != nil {
		return domain.Event{}, "", ErrStorage
	}
	e.InboxID = id
	if err := changed(tx.ExecContext(ctx, "UPDATE bridge_inbox SET state='leased',lease_token=?,lease_until_ms=? WHERE id=? AND state='pending'", token, until, id)); err != nil {
		return domain.Event{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return domain.Event{}, "", safe(err)
	}
	return e, token, nil
}
func (s *Store) DeferInbox(ctx context.Context, id int64, token string, availableAt, now time.Time) error {
	if availableAt.Before(now) {
		return ErrInvalid
	}
	return changed(s.db.ExecContext(ctx, "UPDATE bridge_inbox SET state='pending',available_at_ms=?,lease_token=NULL,lease_until_ms=NULL WHERE id=? AND state='leased' AND lease_token=? AND lease_until_ms>?", availableAt.UnixMilli(), id, token, fenceTime(now).UnixMilli()))
}
func (s *Store) QuarantineInbox(ctx context.Context, id int64, token string, now time.Time) error {
	if now.IsZero() {
		return ErrInvalid
	}
	return changed(s.db.ExecContext(ctx, "UPDATE bridge_inbox SET state='quarantined',lease_token=NULL,lease_until_ms=NULL WHERE id=? AND state='leased' AND lease_token=? AND lease_until_ms>?", id, token, fenceTime(now).UnixMilli()))
}
func (s *Store) PlanEvent(ctx context.Context, e domain.Event, token string, deliveries []domain.Delivery, now time.Time) error {
	if e.InboxID <= 0 || now.IsZero() || e.Validate() != nil {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	now = fenceTime(now)
	var payload []byte
	err = tx.QueryRowContext(ctx, "SELECT payload FROM bridge_inbox WHERE id=? AND state='leased' AND lease_token=? AND lease_until_ms>?", e.InboxID, token, now.UnixMilli()).Scan(&payload)
	if err == sql.ErrNoRows {
		return ErrLeaseLost
	}
	if err != nil {
		return safe(err)
	}
	original := e
	original.InboxID = 0
	encoded, _ := json.Marshal(original)
	if string(encoded) != string(payload) {
		return ErrConflict
	}
	var chatID, messageID int64
	var suppress bool
	if e.Target != nil {
		messageID, chatID, suppress, err = s.applySource(ctx, tx, e, deliveries, now)
		if err != nil {
			return err
		}
	} else {
		if len(deliveries) == 0 {
			return ErrInvalid
		}
		chatID = deliveries[0].ChatID
	}
	if !suppress {
		for _, d := range deliveries {
			if d.ChatID != chatID || (d.MessageID != 0 && d.MessageID != messageID) {
				return ErrConflict
			}
			d.MessageID = messageID
			if err := s.insertDelivery(ctx, tx, e.InboxID, d, now); err != nil {
				return err
			}
		}
	}
	if err := changed(tx.ExecContext(ctx, "UPDATE bridge_inbox SET state='done',lease_token=NULL,lease_until_ms=NULL WHERE id=? AND state='leased' AND lease_token=? AND lease_until_ms>?", e.InboxID, token, fenceTime(now).UnixMilli())); err != nil {
		return err
	}
	return safe(tx.Commit())
}
func (s *Store) applySource(ctx context.Context, tx *sql.Tx, e domain.Event, deliveries []domain.Delivery, now time.Time) (messageID, chatID int64, suppress bool, err error) {
	var partID int64
	var tombstone bool
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT p.id,m.id,m.chat_id,m.tombstone,ss.version FROM bridge_part p JOIN bridge_message m ON m.id=p.message_id JOIN bridge_source_state ss ON ss.part_id=p.id WHERE p.platform=? AND p.remote_chat=? AND p.remote_author=? AND p.remote_id=?`, e.Target.Platform, e.Target.Chat, e.Target.Author, e.Target.ID).Scan(&partID, &messageID, &chatID, &tombstone, &previous)
	if err != nil && err != sql.ErrNoRows {
		return 0, 0, false, safe(err)
	}
	if err == sql.ErrNoRows {
		if e.Kind != domain.MessageCreate && e.Kind != domain.MessageRevoke {
			return 0, 0, false, ErrNoWork
		}
		if len(deliveries) > 0 {
			chatID = deliveries[0].ChatID
		} else {
			query := "SELECT id FROM bridge_chat WHERE wa_jid=?"
			if e.Source == domain.Telegram {
				query = "SELECT id FROM bridge_chat WHERE CAST(tg_chat_id AS TEXT)=?"
			}
			rows, err := tx.QueryContext(ctx, query, e.Chat)
			if err != nil {
				return 0, 0, false, safe(err)
			}
			count := 0
			for rows.Next() {
				if err := rows.Scan(&chatID); err != nil {
					rows.Close()
					return 0, 0, false, safe(err)
				}
				count++
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return 0, 0, false, safe(err)
			}
			if count != 1 {
				return 0, 0, false, ErrConflict
			}
		}
		var boundWA string
		var boundTG int64
		if err := tx.QueryRowContext(ctx, "SELECT wa_jid,tg_chat_id FROM bridge_chat WHERE id=?", chatID).Scan(&boundWA, &boundTG); err != nil {
			return 0, 0, false, safe(err)
		}
		if (e.Source == domain.WhatsApp && e.Chat != boundWA) || (e.Source == domain.Telegram && e.Chat != strconv.FormatInt(boundTG, 10)) {
			return 0, 0, false, ErrConflict
		}
		version, _ := json.Marshal(e.Version)
		text, _ := json.Marshal(e.Text)
		deleted := e.Kind == domain.MessageRevoke
		if deleted {
			text = []byte("null")
		}
		result, err := tx.ExecContext(ctx, "INSERT INTO bridge_message(chat_id,origin_platform,origin_author,logical_key,created_at_ms,tombstone,state_version,state_payload) VALUES (?,?,?,?,?,?,?,?)", chatID, e.Source, e.Target.Author, refKey(*e.Target), now.UnixMilli(), deleted, version, text)
		if err != nil {
			return 0, 0, false, safe(err)
		}
		messageID, err = result.LastInsertId()
		if err != nil {
			return 0, 0, false, safe(err)
		}
		var generation int64
		if err := tx.QueryRowContext(ctx, "SELECT topic_generation FROM bridge_chat WHERE id=?", chatID).Scan(&generation); err != nil {
			return 0, 0, false, safe(err)
		}
		result, err = tx.ExecContext(ctx, "INSERT INTO bridge_part(message_id,platform,remote_chat,remote_author,remote_id,part_index,role,is_bridge_owned,capabilities,topic_generation,deleted) VALUES (?,?,?,?,?,0,'primary',0,0,?,?)", messageID, e.Source, e.Target.Chat, e.Target.Author, e.Target.ID, generation, deleted)
		if err != nil {
			return 0, 0, false, safe(err)
		}
		partID, err = result.LastInsertId()
		if err != nil {
			return 0, 0, false, safe(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO bridge_source_state(part_id,version,deleted) VALUES (?,?,?)", partID, version, deleted); err != nil {
			return 0, 0, false, safe(err)
		}
		return messageID, chatID, false, nil
	}
	if tombstone && e.Kind != domain.MessageRevoke {
		return messageID, chatID, true, nil
	}
	if e.Kind != domain.MessageCreate && e.Kind != domain.MessageEdit && e.Kind != domain.MessageRevoke {
		return 0, 0, false, ErrInvalid
	}
	var version domain.SourceVersion
	if err := json.Unmarshal(previous, &version); err != nil {
		return 0, 0, false, ErrStorage
	}
	if e.Kind != domain.MessageRevoke {
		order, err := e.Version.Compare(version)
		if err != nil {
			return 0, 0, false, err
		}
		if order <= 0 {
			return messageID, chatID, true, nil
		}
		var effective []byte
		if err := tx.QueryRowContext(ctx, "SELECT state_version FROM bridge_message WHERE id=?", messageID).Scan(&effective); err != nil {
			return 0, 0, false, safe(err)
		}
		var effectiveVersion domain.SourceVersion
		if err := json.Unmarshal(effective, &effectiveVersion); err != nil {
			return 0, 0, false, ErrStorage
		}
		if _, err := e.Version.Compare(effectiveVersion); err != nil {
			return 0, 0, false, err
		}
	}
	versionData, _ := json.Marshal(e.Version)
	text, _ := json.Marshal(e.Text)
	deleted := e.Kind == domain.MessageRevoke
	if deleted {
		text = []byte("null")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE bridge_source_state SET version=?,deleted=MAX(deleted,?) WHERE part_id=?", versionData, deleted, partID); err != nil {
		return 0, 0, false, safe(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE bridge_message SET state_version=?,state_payload=?,tombstone=MAX(tombstone,?) WHERE id=?", versionData, text, deleted, messageID); err != nil {
		return 0, 0, false, safe(err)
	}
	if deleted {
		if _, err := tx.ExecContext(ctx, "UPDATE bridge_outbox SET state='superseded',lease_token=NULL,lease_until_ms=NULL WHERE message_id=? AND send_started_at_ms IS NULL AND operation IN ('message.create','message.edit','reaction.set') AND state IN ('pending','leased')", messageID); err != nil {
			return 0, 0, false, safe(err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE bridge_part SET deleted=1 WHERE message_id=?", messageID); err != nil {
			return 0, 0, false, safe(err)
		}
	}
	return messageID, chatID, false, nil
}
func (s *Store) insertDelivery(ctx context.Context, tx *sql.Tx, inboxID int64, d domain.Delivery, now time.Time) error {
	if d.OperationKey == "" || len(d.OperationKey) > 512 || !d.Platform.Valid() || d.ChatID <= 0 || d.TopicGeneration < 0 || d.ExpectedVersion.Validate() != nil || d.ID != 0 || d.LeaseToken != "" || d.SendStartedAt != nil || len(d.WhatsAppSendID) > 256 || (d.Text != nil && d.Text.Validate() != nil) || (d.Target != nil && d.Target.Validate() != nil) {
		return ErrInvalid
	}
	switch d.Kind {
	case "message.create", "message.edit", "message.revoke", "reaction.set", "topic.create", "topic.rename":
	default:
		return ErrInvalid
	}
	if d.Target != nil && d.Target.Platform != d.Platform {
		return ErrInvalid
	}
	if d.Kind == "message.create" && d.MessageID <= 0 {
		return ErrInvalid
	}
	if (d.Kind == "message.edit" || d.Kind == "message.revoke" || d.Kind == "reaction.set") && (d.MessageID <= 0 || d.Target == nil) {
		return ErrInvalid
	}
	if d.Platform == domain.WhatsApp && (d.WhatsAppSendID == "" || d.EchoActor == "" || len(d.EchoActor) > 256) {
		return ErrInvalid
	}
	var generation int64
	var state ChatState
	if err := tx.QueryRowContext(ctx, "SELECT topic_generation,state FROM bridge_chat WHERE id=?", d.ChatID).Scan(&generation, &state); err != nil {
		return safe(err)
	}
	if generation != d.TopicGeneration || (d.Kind != "topic.create" && state != ChatActive) || (d.Kind == "topic.create" && state != ChatDiscovered) {
		return ErrConflict
	}
	if d.DependsOnID != 0 {
		var message, chat int64
		var depGeneration int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(message_id,0),chat_id,topic_generation FROM bridge_outbox WHERE id=?", d.DependsOnID).Scan(&message, &chat, &depGeneration); err != nil {
			return ErrConflict
		}
		if message != d.MessageID || chat != d.ChatID || depGeneration != d.TopicGeneration {
			return ErrConflict
		}
	}
	payload, err := json.Marshal(d)
	if err != nil || len(payload) > domain.MaxEventBytes {
		return ErrInvalid
	}
	var existing []byte
	err = tx.QueryRowContext(ctx, "SELECT payload FROM bridge_outbox WHERE operation_key=?", d.OperationKey).Scan(&existing)
	if err == nil {
		if string(existing) != string(payload) {
			return ErrConflict
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return safe(err)
	}
	var count, bytes int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(length(payload)),0) FROM bridge_outbox WHERE state NOT IN ('succeeded','dead','superseded')").Scan(&count, &bytes); err != nil {
		return safe(err)
	}
	if count >= int64(s.options.MaxOutboxCount) || int64(len(payload)) > s.options.MaxOutboxBytes-bytes {
		return ErrSaturated
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO bridge_outbox(inbox_id,chat_id,message_id,operation_key,platform,operation,depends_on_id,topic_generation,payload,wa_send_id,state,available_at_ms) VALUES (?,?,?,?,?,?,?,?,?,?,'pending',?)", inboxID, d.ChatID, nullableID(d.MessageID), d.OperationKey, d.Platform, d.Kind, nullableID(d.DependsOnID), d.TopicGeneration, payload, nullableString(d.WhatsAppSendID), now.UnixMilli())
	return safe(err)
}
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Store) RecoverExpiredLeases(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	for _, query := range []string{
		"UPDATE bridge_inbox SET state='pending',lease_token=NULL,lease_until_ms=NULL WHERE state='leased' AND lease_until_ms<=?",
		"UPDATE bridge_outbox SET state=CASE WHEN send_started_at_ms IS NULL THEN 'pending' ELSE 'uncertain' END,lease_token=NULL,lease_until_ms=NULL WHERE state IN ('leased','sending') AND lease_until_ms<=?",
		"UPDATE bridge_timer SET state='pending',lease_token=NULL,lease_until_ms=NULL WHERE state='leased' AND lease_until_ms<=?",
	} {
		if _, err := tx.ExecContext(ctx, query, now.UnixMilli()); err != nil {
			return safe(err)
		}
	}
	return safe(tx.Commit())
}
