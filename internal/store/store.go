package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed schema.sql
var schema embed.FS

type Store struct {
	DB *sql.DB
}

type Outbox struct {
	ID       int64
	Provider string
	ChatID   sql.NullInt64
	Payload  []byte
	Attempts int
}

type Inbox struct {
	ID       int64
	Provider string
	Payload  []byte
}

type Chat struct {
	ID             int64  `json:"id"`
	ProviderChatID string `json:"provider_chat_id"`
	DisplayName    string `json:"display_name"`
	Muted          bool   `json:"muted"`
	ThreadID       int64  `json:"telegram_thread_id"`
}

type Message struct {
	ID                int64     `json:"id"`
	ChatID            int64     `json:"chat_id"`
	Direction         string    `json:"direction"`
	ProviderMessageID string    `json:"provider_message_id"`
	Body              string    `json:"body"`
	CreatedAt         time.Time `json:"created_at"`
}

type Delivery struct {
	ID        int64     `json:"id"`
	Provider  string    `json:"provider"`
	ChatID    *int64    `json:"chat_id,omitempty"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

func (s *Store) Migrate(ctx context.Context) error {
	contents, err := schema.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, string(contents))
	return err
}

func (s *Store) Close() error {
	return s.DB.Close()
}

func (s *Store) PutInbox(ctx context.Context, provider, externalID string, payload []byte) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO inbox_events(provider, external_id, payload)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING`, provider, externalID, payload)
	return err
}

func (s *Store) ClaimInbox(ctx context.Context) (Inbox, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Inbox{}, err
	}
	defer tx.Rollback()

	var event Inbox
	err = tx.QueryRowContext(ctx, `
		SELECT id, provider, payload
		FROM inbox_events
		WHERE status = 'pending'
		ORDER BY id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`).Scan(&event.ID, &event.Provider, &event.Payload)
	if err != nil {
		return event, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE inbox_events SET status = 'processing' WHERE id = $1`, event.ID); err != nil {
		return event, err
	}
	return event, tx.Commit()
}

func (s *Store) CompleteInbox(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE inbox_events SET status = 'processed', processed_at = now() WHERE id = $1`, id)
	return err
}

func (s *Store) ResetInbox(ctx context.Context, id int64, cause error) error {
	_ = cause
	_, err := s.DB.ExecContext(ctx, `UPDATE inbox_events SET status = 'pending' WHERE id = $1`, id)
	return err
}

func (s *Store) TopicForChat(ctx context.Context, chatID int64) (int64, error) {
	var threadID int64
	err := s.DB.QueryRowContext(ctx, `SELECT telegram_thread_id FROM topic_links WHERE chat_id = $1`, chatID).Scan(&threadID)
	return threadID, err
}

func (s *Store) ClaimOutbox(ctx context.Context) (Outbox, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Outbox{}, err
	}
	defer tx.Rollback()

	var outbox Outbox
	err = tx.QueryRowContext(ctx, `
		SELECT id, provider, chat_id, payload, attempts
		FROM outbox_events
		WHERE status = 'pending' AND next_attempt_at <= now()
		ORDER BY id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`).Scan(
		&outbox.ID, &outbox.Provider, &outbox.ChatID,
		&outbox.Payload, &outbox.Attempts,
	)
	if err != nil {
		return outbox, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE outbox_events SET status = 'processing' WHERE id = $1`, outbox.ID); err != nil {
		return outbox, err
	}
	return outbox, tx.Commit()
}

func (s *Store) CompleteOutbox(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE outbox_events SET status = 'sent' WHERE id = $1`, id)
	return err
}

func (s *Store) FailOutbox(ctx context.Context, id int64, attempts int, cause error) error {
	status := "pending"
	if attempts >= 5 {
		status = "dead"
	}
	_, err := s.DB.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = $1, attempts = $2, last_error = $3,
		    next_attempt_at = now() + ($2 * interval '5 seconds')
		WHERE id = $4`, status, attempts, cause.Error(), id)
	return err
}

func (s *Store) CreateOutbox(ctx context.Context, provider string, chatID *int64, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO outbox_events(provider, chat_id, payload)
		VALUES ($1, $2, $3)`, provider, chatID, data)
	return err
}

func (s *Store) ResolveTopic(ctx context.Context, groupID, threadID int64) (int64, error) {
	var chatID int64
	err := s.DB.QueryRowContext(ctx, `
		SELECT chat_id FROM topic_links
		WHERE telegram_group_id = $1 AND telegram_thread_id = $2`, groupID, threadID).Scan(&chatID)
	return chatID, err
}

func (s *Store) UpsertChat(ctx context.Context, providerID, name string) (int64, error) {
	var chatID int64
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO chats(provider_chat_id, display_name)
		VALUES ($1, $2)
		ON CONFLICT(provider_chat_id) DO UPDATE SET display_name = EXCLUDED.display_name
		RETURNING id`, providerID, name).Scan(&chatID)
	return chatID, err
}

func (s *Store) LinkTopic(ctx context.Context, chatID, groupID, threadID int64) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO topic_links(chat_id, telegram_group_id, telegram_thread_id)
		VALUES ($1, $2, $3)
		ON CONFLICT(chat_id) DO NOTHING`, chatID, groupID, threadID)
	return err
}

func (s *Store) AddMessage(ctx context.Context, chatID int64, direction, providerID, body string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `
		INSERT INTO messages(chat_id, direction, provider_message_id, body)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT(provider_message_id) DO NOTHING`, chatID, direction, providerID, body)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) SetTelegramMessageID(ctx context.Context, providerID string, telegramID int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE messages SET telegram_message_id = $1 WHERE provider_message_id = $2`, telegramID, providerID)
	return err
}

func (s *Store) ChatTarget(ctx context.Context, chatID int64) (string, error) {
	var target string
	err := s.DB.QueryRowContext(ctx, `SELECT provider_chat_id FROM chats WHERE id = $1`, chatID).Scan(&target)
	return target, err
}

func (s *Store) ListChats(ctx context.Context) ([]Chat, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT c.id, c.provider_chat_id, c.display_name, c.muted,
		       COALESCE(t.telegram_thread_id, 0)
		FROM chats c LEFT JOIN topic_links t ON t.chat_id = c.id
		ORDER BY c.display_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chats []Chat
	for rows.Next() {
		var chat Chat
		if err := rows.Scan(&chat.ID, &chat.ProviderChatID, &chat.DisplayName, &chat.Muted, &chat.ThreadID); err != nil {
			return nil, err
		}
		chats = append(chats, chat)
	}
	return chats, rows.Err()
}

func (s *Store) ListMessages(ctx context.Context, chatID int64) ([]Message, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, chat_id, direction, COALESCE(provider_message_id, ''), body, created_at
		FROM messages WHERE chat_id = $1 ORDER BY id`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.ID, &message.ChatID, &message.Direction, &message.ProviderMessageID, &message.Body, &message.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) SetMuted(ctx context.Context, chatID int64, muted bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE chats SET muted = $1 WHERE id = $2`, muted, chatID)
	return err
}

func (s *Store) IsMuted(ctx context.Context, chatID int64) (bool, error) {
	var muted bool
	err := s.DB.QueryRowContext(ctx, `SELECT muted FROM chats WHERE id = $1`, chatID).Scan(&muted)
	return muted, err
}

func (s *Store) SetMessageStatus(ctx context.Context, providerID, status string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE messages SET status = $1 WHERE provider_message_id = $2`, status, providerID)
	return err
}

func (s *Store) CreateMedia(ctx context.Context, chatID int64, provider, fileID, mimeType, caption string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO media_objects(chat_id, provider, provider_file_id, mime_type, caption)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT(provider, provider_file_id) DO NOTHING`, chatID, provider, fileID, mimeType, caption)
	return err
}

func (s *Store) GetDelivery(ctx context.Context, id int64) (Delivery, error) {
	var delivery Delivery
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, provider, chat_id, status, attempts, COALESCE(last_error, ''), created_at
		FROM outbox_events WHERE id = $1`, id).Scan(
		&delivery.ID, &delivery.Provider, &delivery.ChatID, &delivery.Status,
		&delivery.Attempts, &delivery.LastError, &delivery.CreatedAt,
	)
	return delivery, err
}

func (s *Store) RetryDelivery(ctx context.Context, id int64) error {
	result, err := s.DB.ExecContext(ctx, `
		UPDATE outbox_events
		SET status = 'pending', next_attempt_at = now(), last_error = NULL
		WHERE id = $1 AND status IN ('dead', 'failed')`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func RetryDelay(attempts int) time.Duration {
	if attempts > 5 {
		attempts = 5
	}
	return time.Duration(1<<attempts) * time.Second
}

func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("empty external id")
	}
	return nil
}
