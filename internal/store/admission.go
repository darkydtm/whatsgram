package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

type Cursor struct {
	NextOffset int64
	Epoch      string
}

func (s *Store) Cursor(ctx context.Context, stream string) (Cursor, error) {
	var c Cursor
	err := s.db.QueryRowContext(ctx, "SELECT next_offset,epoch FROM bridge_cursor WHERE stream=?", stream).Scan(&c.NextOffset, &c.Epoch)
	if err == sql.ErrNoRows {
		return Cursor{}, ErrNoWork
	}
	return c, safe(err)
}
func refKey(ref domain.MessageRef) string {
	data, _ := json.Marshal([]string{string(ref.Platform), ref.Chat, ref.Author, ref.ID})
	return string(data)
}
func admissionKey(e domain.Event) string {
	author, id := "", ""
	if e.Target != nil {
		author = e.Target.Author
		id = e.Target.ID
	}
	data, _ := json.Marshal([]string{string(e.Source), e.Chat, author, id, e.Actor, e.Key, e.Version.StreamEpoch})
	return string(data)
}
func (s *Store) Accept(ctx context.Context, e domain.Event) (domain.Admission, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Admission{}, safe(err)
	}
	defer tx.Rollback()
	a, err := s.accept(ctx, tx, e)
	if err != nil {
		return domain.Admission{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Admission{}, safe(err)
	}
	return a, nil
}
func (s *Store) accept(ctx context.Context, tx *sql.Tx, e domain.Event) (domain.Admission, error) {
	if e.InboxID != 0 || e.Validate() != nil {
		return domain.Admission{}, ErrInvalid
	}
	payload, err := marshalEvent(e)
	if err != nil || len(payload) > domain.MaxEventBytes {
		return domain.Admission{}, ErrInvalid
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	key := admissionKey(e)
	var id int64
	var previous string
	err = tx.QueryRowContext(ctx, "SELECT id,digest FROM bridge_inbox WHERE platform=? AND dedupe_key=?", e.Source, key).Scan(&id, &previous)
	if err == nil {
		if previous != digest {
			return domain.Admission{}, ErrConflict
		}
		return domain.Admission{InboxID: id}, nil
	}
	if err != sql.ErrNoRows {
		return domain.Admission{}, safe(err)
	}
	var count, bytes int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(length(payload)),0) FROM bridge_inbox WHERE state IN ('pending','leased','quarantined')").Scan(&count, &bytes); err != nil {
		return domain.Admission{}, safe(err)
	}
	if count >= int64(s.options.MaxInboxCount) || int64(len(payload)) > s.options.MaxInboxBytes-bytes {
		return domain.Admission{}, ErrSaturated
	}
	target := string(e.Source) + ":" + e.Chat
	if e.Target != nil {
		target = refKey(*e.Target)
	}
	state := "pending"
	if !e.Kind.Known() {
		state = "quarantined"
	}
	result, err := tx.ExecContext(ctx, "INSERT INTO bridge_inbox(platform,dedupe_key,event_kind,target_key,payload,digest,state,available_at_ms) VALUES (?,?,?,?,?,?,?,?)", e.Source, key, e.Kind, target, payload, digest, state, time.Now().UnixMilli())
	if err != nil {
		return domain.Admission{}, safe(err)
	}
	id, err = result.LastInsertId()
	return domain.Admission{Inserted: true, InboxID: id}, safe(err)
}
func (s *Store) AcceptTelegramBatch(ctx context.Context, events []domain.Event, nextOffset int64, epoch string) error {
	if nextOffset < 0 || epoch == "" || len(epoch) > 256 || len(events) > s.options.MaxInboxCount {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	var current int64
	var previous string
	err = tx.QueryRowContext(ctx, "SELECT next_offset,epoch FROM bridge_cursor WHERE stream='telegram'").Scan(&current, &previous)
	if err == nil && (epoch != previous || nextOffset < current) {
		return ErrConflict
	}
	if err != nil && err != sql.ErrNoRows {
		return safe(err)
	}
	for _, e := range events {
		if e.Source != domain.Telegram || e.Version.StreamEpoch != epoch {
			return ErrInvalid
		}
		if _, err := s.accept(ctx, tx, e); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO bridge_cursor(stream,next_offset,epoch) VALUES ('telegram',?,?) ON CONFLICT(stream) DO UPDATE SET next_offset=excluded.next_offset,epoch=excluded.epoch", nextOffset, epoch)
	if err != nil {
		return safe(err)
	}
	return safe(tx.Commit())
}
