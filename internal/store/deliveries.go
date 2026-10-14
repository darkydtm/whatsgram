package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

type DeliveryState struct {
	Delivery   domain.Delivery
	State      string
	ErrorClass domain.ErrorClass
}

func (s *Store) Delivery(ctx context.Context, id int64) (DeliveryState, error) {
	var result DeliveryState
	var payload []byte
	var token sql.NullString
	var until, started sql.NullInt64
	var class sql.NullString
	err := s.db.QueryRowContext(ctx, "SELECT payload,state,lease_token,lease_until_ms,send_started_at_ms,error_class FROM bridge_outbox WHERE id=?", id).Scan(&payload, &result.State, &token, &until, &started, &class)
	if err == sql.ErrNoRows {
		return DeliveryState{}, ErrNoWork
	}
	if err != nil {
		return DeliveryState{}, safe(err)
	}
	if err := json.Unmarshal(payload, &result.Delivery); err != nil {
		return DeliveryState{}, ErrStorage
	}
	result.Delivery.ID = id
	result.Delivery.LeaseToken = token.String
	if until.Valid {
		result.Delivery.LeaseUntil = time.UnixMilli(until.Int64)
	}
	if started.Valid {
		at := time.UnixMilli(started.Int64)
		result.Delivery.SendStartedAt = &at
	}
	result.ErrorClass = domain.ErrorClass(class.String)
	return result, nil
}
func (s *Store) ClaimDelivery(ctx context.Context, now time.Time, ttl time.Duration) (domain.Delivery, error) {
	token, until, err := lease(now, ttl)
	if err != nil {
		return domain.Delivery{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Delivery{}, safe(err)
	}
	defer tx.Rollback()
	var id int64
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT o.id,o.payload FROM bridge_outbox o JOIN bridge_chat c ON c.id=o.chat_id WHERE o.state='pending' AND o.available_at_ms<=? AND o.topic_generation=c.topic_generation AND ((o.operation='topic.create' AND c.state='discovered') OR (o.operation!='topic.create' AND c.state='active')) AND (o.depends_on_id IS NULL OR EXISTS (SELECT 1 FROM bridge_outbox dep WHERE dep.id=o.depends_on_id AND dep.state='succeeded')) AND NOT EXISTS (SELECT 1 FROM bridge_outbox other WHERE other.id!=o.id AND other.state IN ('pending','leased','sending','uncertain','blocked') AND ((other.message_id=o.message_id AND (other.id<o.id OR other.state IN ('leased','sending','uncertain'))) OR (other.chat_id=o.chat_id AND (other.message_id IS NULL OR o.message_id IS NULL) AND (other.id<o.id OR other.state IN ('leased','sending','uncertain'))))) ORDER BY o.id LIMIT 1`, now.UnixMilli()).Scan(&id, &payload)
	if err == sql.ErrNoRows {
		return domain.Delivery{}, ErrNoWork
	}
	if err != nil {
		return domain.Delivery{}, safe(err)
	}
	var d domain.Delivery
	if err := json.Unmarshal(payload, &d); err != nil {
		return domain.Delivery{}, ErrStorage
	}
	if err := changed(tx.ExecContext(ctx, "UPDATE bridge_outbox SET state='leased',lease_token=?,lease_until_ms=? WHERE id=? AND state='pending'", token, until, id)); err != nil {
		return domain.Delivery{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Delivery{}, safe(err)
	}
	d.ID = id
	d.LeaseToken = token
	d.LeaseUntil = time.UnixMilli(until)
	return d, nil
}

type Echo struct {
	Platform domain.Platform
	Chat     string
	Actor    string
	EventID  string
	Kind     string
	Target   *domain.MessageRef
	Digest   string
}

func (s *Store) MatchEcho(ctx context.Context, echo Echo) (bool, error) {
	if !echo.Platform.Valid() || echo.Chat == "" || echo.Actor == "" || echo.EventID == "" || echo.Kind == "" || echo.Digest == "" || (echo.Target != nil && echo.Target.Validate() != nil) {
		return false, ErrInvalid
	}
	target := ""
	if echo.Target != nil {
		target = refKey(*echo.Target)
	}
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_echo WHERE platform=? AND chat_key=? AND actor_key=? AND remote_event_id=? AND operation_kind=? AND target_key=? AND payload_digest=?", echo.Platform, echo.Chat, echo.Actor, echo.EventID, echo.Kind, target, echo.Digest).Scan(&n)
	return n == 1, safe(err)
}
func (s *Store) BeginSend(ctx context.Context, id int64, token string, now time.Time) error {
	if now.IsZero() {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	var payload []byte
	var chat string
	err = tx.QueryRowContext(ctx, `SELECT o.payload,c.wa_jid FROM bridge_outbox o JOIN bridge_chat c ON c.id=o.chat_id WHERE o.id=? AND o.state='leased' AND o.lease_token=? AND o.lease_until_ms>? AND o.topic_generation=c.topic_generation AND ((o.operation='topic.create' AND c.state='discovered') OR (o.operation!='topic.create' AND c.state='active'))`, id, token, now.UnixMilli()).Scan(&payload, &chat)
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
	if d.Platform == domain.WhatsApp {
		target := ""
		if d.Target != nil {
			target = refKey(*d.Target)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO bridge_echo(platform,chat_key,actor_key,remote_event_id,operation_kind,target_key,payload_digest,outbox_id) VALUES (?,?,?,?,?,?,?,?)", d.Platform, chat, d.EchoActor, d.WhatsAppSendID, d.Kind, target, d.ExpectedVersion.Digest, id); err != nil {
			return safe(err)
		}
	}
	if err := changed(tx.ExecContext(ctx, "UPDATE bridge_outbox SET state='sending',send_started_at_ms=? WHERE id=? AND state='leased' AND lease_token=? AND lease_until_ms>?", now.UnixMilli(), id, token, now.UnixMilli())); err != nil {
		return err
	}
	return safe(tx.Commit())
}
