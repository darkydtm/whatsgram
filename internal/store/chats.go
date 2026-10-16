package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"go.mau.fi/whatsmeow/types"
)

type ChatState string

const (
	ChatDiscovered ChatState = "discovered"
	ChatActive     ChatState = "active"
	ChatClosed     ChatState = "closed"
	ChatMissing    ChatState = "missing"
	ChatUncertain  ChatState = "uncertain"
	ChatBlocked    ChatState = "blocked"
)

type Chat struct {
	ID             int64
	WhatsAppJID    string
	TelegramChatID int64
	Topic          *domain.TopicRef
	Generation     int64
	State          ChatState
	Title          string
	Source         string
	HistoryPartial bool
}

func (s *Store) DiscoverChat(ctx context.Context, candidate domain.ChatCandidate, telegramChatID int64) error {
	jid, err := types.ParseJID(candidate.WhatsAppJID)
	if err != nil || jid.User == "" || jid.Device != 0 || jid.RawAgent != 0 || jid.String() != candidate.WhatsAppJID || (jid.Server != types.GroupServer && jid.Server != types.DefaultUserServer && jid.Server != types.HiddenUserServer) || telegramChatID == 0 || len(candidate.WhatsAppJID) > 256 || strings.ContainsRune(candidate.WhatsAppJID, 0) || len(candidate.Title) > 512 || candidate.Source == "" || len(candidate.Source) > 64 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	var existing int64
	err = tx.QueryRowContext(ctx, "SELECT tg_chat_id FROM bridge_chat WHERE wa_jid=?", candidate.WhatsAppJID).Scan(&existing)
	if err == nil && existing != telegramChatID {
		return ErrConflict
	}
	if err != nil && err != sql.ErrNoRows {
		return safe(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO bridge_chat(wa_jid,tg_chat_id,state,title,discovered_by,history_partial) VALUES (?,?,'discovered',?,?,?) ON CONFLICT(wa_jid) DO UPDATE SET title=excluded.title,history_partial=MAX(bridge_chat.history_partial,excluded.history_partial)`, candidate.WhatsAppJID, telegramChatID, candidate.Title, candidate.Source, candidate.HistoryPartial)
	if err != nil {
		return safe(err)
	}
	return safe(tx.Commit())
}
func (s *Store) Chat(ctx context.Context, waJID string) (Chat, error) {
	var c Chat
	var topic sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT id,wa_jid,tg_chat_id,topic_id,topic_generation,state,title,discovered_by,history_partial FROM bridge_chat WHERE wa_jid=?", waJID).Scan(&c.ID, &c.WhatsAppJID, &c.TelegramChatID, &topic, &c.Generation, &c.State, &c.Title, &c.Source, &c.HistoryPartial)
	if err == sql.ErrNoRows {
		return Chat{}, ErrNoWork
	}
	if err != nil {
		return Chat{}, safe(err)
	}
	if topic.Valid {
		c.Topic = &domain.TopicRef{ChatID: c.TelegramChatID, ThreadID: topic.Int64, Generation: c.Generation}
	}
	return c, nil
}
func (s *Store) ChatByTopic(ctx context.Context, telegramChatID, threadID int64) (Chat, error) {
	if telegramChatID == 0 || threadID <= 0 {
		return Chat{}, ErrInvalid
	}
	var jid string
	err := s.db.QueryRowContext(ctx, "SELECT wa_jid FROM bridge_chat WHERE tg_chat_id=? AND topic_id=?", telegramChatID, threadID).Scan(&jid)
	if err == sql.ErrNoRows {
		return Chat{}, ErrNoWork
	}
	if err != nil {
		return Chat{}, safe(err)
	}
	return s.Chat(ctx, jid)
}
func (s *Store) BindTopic(ctx context.Context, waJID string, topic domain.TopicRef) error {
	if topic.ChatID == 0 || topic.ThreadID <= 0 || topic.Generation <= 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return safe(err)
	}
	defer tx.Rollback()
	var chatID, generation int64
	var previous sql.NullInt64
	var state ChatState
	err = tx.QueryRowContext(ctx, "SELECT tg_chat_id,topic_id,topic_generation,state FROM bridge_chat WHERE wa_jid=?", waJID).Scan(&chatID, &previous, &generation, &state)
	if err == sql.ErrNoRows {
		return ErrNoWork
	}
	if err != nil {
		return safe(err)
	}
	if chatID != topic.ChatID {
		return ErrConflict
	}
	if generation == topic.Generation && previous.Valid && previous.Int64 == topic.ThreadID {
		return nil
	}
	if topic.Generation != generation+1 || (previous.Valid && state != ChatMissing && state != ChatUncertain && state != ChatBlocked) {
		return ErrConflict
	}
	_, err = tx.ExecContext(ctx, "UPDATE bridge_chat SET topic_id=?,topic_generation=?,state='active' WHERE wa_jid=?", topic.ThreadID, topic.Generation, waJID)
	if err != nil {
		return safe(err)
	}
	return safe(tx.Commit())
}
func (s *Store) SetChatState(ctx context.Context, chatID, generation int64, state ChatState) error {
	switch state {
	case ChatActive, ChatClosed, ChatMissing, ChatUncertain, ChatBlocked:
	default:
		return ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, "UPDATE bridge_chat SET state=? WHERE id=? AND topic_generation=? AND (?!='active' OR topic_id IS NOT NULL)", state, chatID, generation, state)
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

func (s *Store) SetChatTitle(ctx context.Context, waJID string, title string) error {
	if len(title) == 0 || len(title) > 512 {
		return ErrInvalid
	}
	result, err := s.db.ExecContext(ctx, "UPDATE bridge_chat SET title=? WHERE wa_jid=?", title, waJID)
	if err != nil {
		return safe(err)
	}
	return changed(result, nil)
}

func (s *Store) SetChatTimer(ctx context.Context, waJID string, d time.Duration) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO bridge_cursor(stream,next_offset,epoch) VALUES (?,?,'timer') ON CONFLICT(stream) DO UPDATE SET next_offset=?", "timer:"+waJID, d.Milliseconds(), d.Milliseconds())
	return safe(err)
}

func (s *Store) GetChatTimer(ctx context.Context, waJID string) (time.Duration, error) {
	var ms int64
	err := s.db.QueryRowContext(ctx, "SELECT next_offset FROM bridge_cursor WHERE stream=? AND epoch='timer'", "timer:"+waJID).Scan(&ms)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, safe(err)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

func (s *Store) SetMetaMessageID(ctx context.Context, waJID string, msgID int, isPhoto bool) error {
	epoch := "text"
	if isPhoto {
		epoch = "photo"
	}
	_, err := s.db.ExecContext(ctx, "INSERT INTO bridge_cursor(stream,next_offset,epoch) VALUES (?,?,?) ON CONFLICT(stream) DO UPDATE SET next_offset=?, epoch=?", "meta:"+waJID, int64(msgID), epoch, int64(msgID), epoch)
	return safe(err)
}

func (s *Store) GetMetaMessageID(ctx context.Context, waJID string) (int, bool, error) {
	var msgID int64
	var epoch string
	err := s.db.QueryRowContext(ctx, "SELECT next_offset, epoch FROM bridge_cursor WHERE stream=?", "meta:"+waJID).Scan(&msgID, &epoch)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, safe(err)
	}
	return int(msgID), epoch == "photo", nil
}
