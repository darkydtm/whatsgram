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

func (s *Store) AddMessage(ctx context.Context, chatID int64, direction, providerID, body string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO messages(chat_id, direction, provider_message_id, body)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT(provider_message_id) DO NOTHING`, chatID, direction, providerID, body)
	return err
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
