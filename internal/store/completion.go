package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func (s *Store) CompleteDelivery(ctx context.Context, id int64, token string, c domain.Completion, now time.Time) error {
	if now.IsZero() || c.AppliedVersion.Validate() != nil || len(c.Result.Parts) > 1024 || len(c.EchoKeys) > 1024 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	var payload []byte
	var generation int64
	var waChat string
	var tgChat int64
	err = tx.QueryRowContext(ctx, `SELECT o.payload,c.topic_generation,c.wa_jid,c.tg_chat_id FROM bridge_outbox o JOIN bridge_chat c ON c.id=o.chat_id WHERE o.id=? AND o.state='sending' AND o.lease_token=? AND o.lease_until_ms>?`, id, token, now.UnixMilli()).Scan(&payload, &generation, &waChat, &tgChat)
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
	if d.TopicGeneration != generation || d.ExpectedVersion != c.AppliedVersion {
		return ErrConflict
	}
	if d.MessageID == 0 {
		return ErrInvalid
	}
	var tombstone bool
	if err := tx.QueryRowContext(ctx, "SELECT tombstone FROM bridge_message WHERE id=?", d.MessageID).Scan(&tombstone); err != nil {
		return safe(err)
	}
	if d.Kind == "message.create" && len(c.Result.Parts) == 0 {
		return ErrInvalid
	}
	version, _ := json.Marshal(c.AppliedVersion)
	for _, p := range c.Result.Parts {
		if p.Validate() != nil || p.Ref.Platform != d.Platform || !p.BridgeOwned || (p.Ref.Platform == domain.WhatsApp && p.Ref.Chat != waChat) {
			return ErrInvalid
		}
		if p.Ref.Platform == domain.Telegram {
			partChat, err := strconv.ParseInt(p.Ref.Chat, 10, 64)
			if err != nil || partChat != tgChat || strconv.FormatInt(partChat, 10) != p.Ref.Chat {
				return ErrInvalid
			}
		}
		var existingID, existingMessage int64
		var existingIndex int
		var role string
		var owned bool
		var capabilities uint64
		err = tx.QueryRowContext(ctx, "SELECT id,message_id,part_index,role,is_bridge_owned,capabilities FROM bridge_part WHERE platform=? AND remote_chat=? AND remote_author=? AND remote_id=?", p.Ref.Platform, p.Ref.Chat, p.Ref.Author, p.Ref.ID).Scan(&existingID, &existingMessage, &existingIndex, &role, &owned, &capabilities)
		if err == nil {
			if existingMessage != d.MessageID || existingIndex != p.Index || role != p.Role || owned != p.BridgeOwned || capabilities != p.Capabilities {
				return ErrConflict
			}
			if _, err := tx.ExecContext(ctx, "UPDATE bridge_part SET deleted=MAX(deleted,?),retired=MAX(retired,?) WHERE id=?", p.Deleted || tombstone, p.Retired, existingID); err != nil {
				return safe(err)
			}
		} else if err == sql.ErrNoRows {
			result, err := tx.ExecContext(ctx, "INSERT INTO bridge_part(message_id,platform,remote_chat,remote_author,remote_id,part_index,role,is_bridge_owned,capabilities,topic_generation,deleted,retired) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)", d.MessageID, p.Ref.Platform, p.Ref.Chat, p.Ref.Author, p.Ref.ID, p.Index, p.Role, p.BridgeOwned, p.Capabilities, d.TopicGeneration, p.Deleted || tombstone, p.Retired)
			if err != nil {
				return safe(err)
			}
			existingID, err = result.LastInsertId()
			if err != nil {
				return safe(err)
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO bridge_source_state(part_id,version,deleted) VALUES (?,?,?)", existingID, version, p.Deleted || tombstone); err != nil {
				return safe(err)
			}
		} else {
			return safe(err)
		}
	}
	if d.Kind == "message.revoke" && d.Target != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE bridge_part SET deleted=1 WHERE message_id=? AND platform=? AND remote_chat=? AND remote_author=? AND remote_id=?", d.MessageID, d.Target.Platform, d.Target.Chat, d.Target.Author, d.Target.ID); err != nil {
			return safe(err)
		}
	}
	chat := waChat
	if d.Platform == domain.Telegram {
		chat = strconv.FormatInt(tgChat, 10)
	}
	target := ""
	if d.Target != nil {
		target = refKey(*d.Target)
	}
	for _, key := range c.EchoKeys {
		if key == "" || len(key) > 256 || d.EchoActor == "" || len(d.EchoActor) > 256 {
			return ErrInvalid
		}
		var oldID int64
		err := tx.QueryRowContext(ctx, "SELECT outbox_id FROM bridge_echo WHERE platform=? AND chat_key=? AND actor_key=? AND remote_event_id=? AND operation_kind=?", d.Platform, chat, d.EchoActor, key, d.Kind).Scan(&oldID)
		if err == nil {
			if oldID != id {
				return ErrConflict
			}
			continue
		}
		if err != sql.ErrNoRows {
			return safe(err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO bridge_echo(platform,chat_key,actor_key,remote_event_id,operation_kind,target_key,payload_digest,outbox_id) VALUES (?,?,?,?,?,?,?,?)", d.Platform, chat, d.EchoActor, key, d.Kind, target, c.AppliedVersion.Digest, id); err != nil {
			return safe(err)
		}
	}
	result, err := json.Marshal(c)
	if err != nil || len(result) > domain.MaxEventBytes {
		return ErrInvalid
	}
	if err := changed(tx.ExecContext(ctx, "UPDATE bridge_outbox SET state='succeeded',result=?,lease_token=NULL,lease_until_ms=NULL WHERE id=? AND state='sending' AND lease_token=? AND lease_until_ms>?", result, id, token, now.UnixMilli())); err != nil {
		return err
	}
	return safe(tx.Commit())
}
func (s *Store) FailDelivery(ctx context.Context, id int64, token string, decision domain.RecoveryDecision, now time.Time) error {
	if now.IsZero() {
		return ErrInvalid
	}
	switch decision.State {
	case "pending", "uncertain", "blocked", "dead":
	default:
		return ErrInvalid
	}
	switch decision.ErrorClass {
	case domain.Transient, domain.RateLimited, domain.Permission, domain.Permanent, domain.Conflict, domain.Uncertain:
	default:
		return ErrInvalid
	}
	if decision.State == "pending" && decision.AvailableAt.Before(now) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	var started sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT send_started_at_ms FROM bridge_outbox WHERE id=? AND state IN ('leased','sending') AND lease_token=? AND lease_until_ms>?", id, token, now.UnixMilli()).Scan(&started)
	if err == sql.ErrNoRows {
		return ErrLeaseLost
	}
	if err != nil {
		return safe(err)
	}
	if started.Valid && decision.State == "pending" {
		return ErrInvalid
	}
	if decision.State == "pending" && decision.ErrorClass != domain.Transient && decision.ErrorClass != domain.RateLimited {
		return ErrInvalid
	}
	if decision.State == "uncertain" && decision.ErrorClass != domain.Uncertain {
		return ErrInvalid
	}
	if err := changed(tx.ExecContext(ctx, "UPDATE bridge_outbox SET state=?,available_at_ms=?,error_class=?,lease_token=NULL,lease_until_ms=NULL WHERE id=? AND state IN ('leased','sending') AND lease_token=? AND lease_until_ms>?", decision.State, decision.AvailableAt.UnixMilli(), decision.ErrorClass, id, token, now.UnixMilli())); err != nil {
		return err
	}
	return safe(tx.Commit())
}
