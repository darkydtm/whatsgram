CREATE TABLE IF NOT EXISTS inbox_events (
 id BIGSERIAL PRIMARY KEY, provider TEXT NOT NULL, external_id TEXT NOT NULL,
 payload JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), processed_at TIMESTAMPTZ,
 UNIQUE(provider, external_id)
);
CREATE TABLE IF NOT EXISTS chats (
 id BIGSERIAL PRIMARY KEY, provider_chat_id TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL,
 muted BOOLEAN NOT NULL DEFAULT false, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS topic_links (
 chat_id BIGINT PRIMARY KEY REFERENCES chats(id), telegram_group_id BIGINT NOT NULL,
 telegram_thread_id BIGINT NOT NULL, UNIQUE(telegram_group_id, telegram_thread_id)
);
CREATE TABLE IF NOT EXISTS messages (
 id BIGSERIAL PRIMARY KEY, chat_id BIGINT NOT NULL REFERENCES chats(id), direction TEXT NOT NULL,
 provider_message_id TEXT UNIQUE, telegram_message_id BIGINT, body TEXT NOT NULL DEFAULT '',
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS outbox_events (
 id BIGSERIAL PRIMARY KEY, provider TEXT NOT NULL, chat_id BIGINT REFERENCES chats(id),
 payload JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'pending', attempts INT NOT NULL DEFAULT 0,
 next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_error TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS outbox_pending_idx ON outbox_events(status, next_attempt_at);
