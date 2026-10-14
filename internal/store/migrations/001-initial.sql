CREATE TABLE bridge_chat (
	id INTEGER PRIMARY KEY,
	wa_jid TEXT NOT NULL UNIQUE,
	tg_chat_id INTEGER NOT NULL,
	topic_id INTEGER CHECK (topic_id > 0),
	topic_generation INTEGER NOT NULL DEFAULT 0 CHECK (topic_generation >= 0),
	state TEXT NOT NULL CHECK (state IN ('discovered','active','closed','missing','uncertain','blocked')),
	title TEXT NOT NULL,
	discovered_by TEXT NOT NULL,
	history_partial INTEGER NOT NULL CHECK (history_partial IN (0,1)),
	UNIQUE (tg_chat_id,topic_id)
);
CREATE TABLE bridge_message (
	id INTEGER PRIMARY KEY,
	chat_id INTEGER NOT NULL REFERENCES bridge_chat(id),
	origin_platform TEXT NOT NULL CHECK (origin_platform IN ('telegram','whatsapp')),
	origin_author TEXT NOT NULL,
	logical_key TEXT NOT NULL UNIQUE,
	created_at_ms INTEGER NOT NULL,
	tombstone INTEGER NOT NULL DEFAULT 0 CHECK (tombstone IN (0,1)),
	state_version BLOB NOT NULL,
	state_payload BLOB NOT NULL,
	kept INTEGER NOT NULL DEFAULT 0 CHECK (kept IN (0,1))
);
CREATE TABLE bridge_part (
	id INTEGER PRIMARY KEY,
	message_id INTEGER NOT NULL REFERENCES bridge_message(id),
	platform TEXT NOT NULL CHECK (platform IN ('telegram','whatsapp')),
	remote_chat TEXT NOT NULL,
	remote_author TEXT NOT NULL,
	remote_id TEXT NOT NULL,
	part_index INTEGER NOT NULL CHECK (part_index >= 0),
	role TEXT NOT NULL CHECK (role IN ('primary','media','text','caption_overflow','fallback','revision_notice')),
	is_bridge_owned INTEGER NOT NULL CHECK (is_bridge_owned IN (0,1)),
	capabilities INTEGER NOT NULL CHECK (capabilities >= 0 AND capabilities <= 127),
	topic_generation INTEGER NOT NULL CHECK (topic_generation >= 0),
	deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0,1)),
	retired INTEGER NOT NULL DEFAULT 0 CHECK (retired IN (0,1)),
	UNIQUE (platform,remote_chat,remote_author,remote_id),
	UNIQUE (message_id,platform,part_index)
);
CREATE UNIQUE INDEX bridge_part_primary ON bridge_part(message_id,platform) WHERE role='primary' AND retired=0;
CREATE TABLE bridge_source_state (
	part_id INTEGER PRIMARY KEY REFERENCES bridge_part(id),
	version BLOB NOT NULL,
	deleted INTEGER NOT NULL CHECK (deleted IN (0,1))
);
CREATE TABLE bridge_inbox (
	id INTEGER PRIMARY KEY,
	platform TEXT NOT NULL CHECK (platform IN ('telegram','whatsapp')),
	dedupe_key TEXT NOT NULL,
	event_kind TEXT NOT NULL,
	target_key TEXT NOT NULL,
	payload BLOB NOT NULL,
	digest TEXT NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('pending','leased','done','quarantined')),
	available_at_ms INTEGER NOT NULL,
	lease_token TEXT,
	lease_until_ms INTEGER,
	UNIQUE (platform,dedupe_key)
);
CREATE INDEX bridge_inbox_due ON bridge_inbox(state,available_at_ms,id);
CREATE TABLE bridge_outbox (
	id INTEGER PRIMARY KEY,
	inbox_id INTEGER NOT NULL REFERENCES bridge_inbox(id),
	chat_id INTEGER NOT NULL REFERENCES bridge_chat(id),
	message_id INTEGER REFERENCES bridge_message(id),
	operation_key TEXT NOT NULL UNIQUE,
	platform TEXT NOT NULL CHECK (platform IN ('telegram','whatsapp')),
	operation TEXT NOT NULL CHECK (operation IN ('message.create','message.edit','message.revoke','reaction.set','topic.create','topic.rename')),
	depends_on_id INTEGER REFERENCES bridge_outbox(id),
	topic_generation INTEGER NOT NULL CHECK (topic_generation >= 0),
	payload BLOB NOT NULL,
	wa_send_id TEXT,
	state TEXT NOT NULL CHECK (state IN ('pending','leased','sending','succeeded','uncertain','blocked','dead','superseded')),
	available_at_ms INTEGER NOT NULL,
	lease_token TEXT,
	lease_until_ms INTEGER,
	send_started_at_ms INTEGER,
	result BLOB,
	error_class TEXT,
	UNIQUE (platform,chat_id,wa_send_id)
);
CREATE INDEX bridge_outbox_due ON bridge_outbox(state,available_at_ms,id);
CREATE INDEX bridge_outbox_message ON bridge_outbox(message_id,state,id);
CREATE TABLE bridge_echo (
	platform TEXT NOT NULL CHECK (platform IN ('telegram','whatsapp')),
	chat_key TEXT NOT NULL,
	actor_key TEXT NOT NULL,
	remote_event_id TEXT NOT NULL,
	operation_kind TEXT NOT NULL,
	target_key TEXT NOT NULL,
	payload_digest TEXT NOT NULL,
	outbox_id INTEGER NOT NULL REFERENCES bridge_outbox(id),
	PRIMARY KEY (platform,chat_key,actor_key,remote_event_id,operation_kind)
);
CREATE TABLE bridge_timer (
	id INTEGER PRIMARY KEY,
	message_id INTEGER NOT NULL REFERENCES bridge_message(id),
	kind TEXT NOT NULL CHECK (kind IN ('expire','mapping_wait')),
	target_version BLOB NOT NULL,
	deadline_ms INTEGER NOT NULL,
	state TEXT NOT NULL CHECK (state IN ('pending','leased','done','cancelled')),
	lease_token TEXT,
	lease_until_ms INTEGER,
	UNIQUE (message_id,kind,target_version)
);
CREATE INDEX bridge_timer_due ON bridge_timer(state,deadline_ms,id);
CREATE TABLE bridge_cursor (
	stream TEXT PRIMARY KEY,
	next_offset INTEGER NOT NULL CHECK (next_offset >= 0),
	epoch TEXT NOT NULL
);
