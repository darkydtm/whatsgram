# Whatsgram

Whatsgram is a production-grade, bidirectional bridge connecting WhatsApp to Telegram forum supergroups. It models WhatsApp chats as native Telegram forum topics, giving operators a unified, multi-topic interface to communicate across platforms with strong durability, zero impersonation, and transactional consistency.

---

## Architecture and Core Principles

- **Durable Two-Phase Delivery**: Inbound messages from WhatsApp and Telegram are durably admitted into SQLite before acknowledgment. Outbound messages are claimed with expiring leases and two-phase dispatch, preventing duplicate deliveries.
- **Strict Access Control**: Only explicitly allowlisted Telegram user IDs can execute administrative commands. The smallest numeric user ID is designated as the primary account driver for native reactions and poll voting.
- **Attribution, Never Impersonation**: Outbound Telegram messages are transmitted by the bot on behalf of operators, with clear sender headers. WhatsApp events from other linked devices are preserved and attributed.
- **Secret-Safe Diagnostics**: Tokens, pairing codes, QR secrets, and session payloads implement strict redaction interfaces (`slog.LogValuer` and `fmt.Formatter`), preventing accidental leakage in logs.
- **Pure Go SQLite Driver**: Uses `modernc.org/sqlite` and compiles with `CGO_ENABLED=0` for fully static, dependency-free cross-compilation.

---

## Step-by-Step Setup Guide

### 1. Telegram Bot Creation and Group Configuration

1. **Create the Bot**: Open Telegram, talk to [@BotFather](https://t.me/BotFather), run `/newbot`, and follow prompts to obtain your bot token (format: `123456789:ABCdefGhIJKlmNoPQRsTUVwxyZ`).
2. **Create a Forum Supergroup**:
   - Create a new Telegram group and convert it to a supergroup.
   - In group settings, enable **Topics** (Forums).
3. **Add Bot as Administrator**:
   - Add your newly created bot to the supergroup.
   - Promote the bot to **Administrator** with the following required permissions:
     - `can_manage_topics`: Required to create, rename, and bind forum topics.
     - `can_delete_messages`: Required to synchronize message deletions and revokes.
4. **Retrieve Identifiers**:
   - Get your supergroup Chat ID (a negative int64, e.g. `-1001234567890`).
   - Get your personal Telegram User ID (a positive int64, e.g. `42`) using [@userinfobot](https://t.me/userinfobot) or Bot API updates.

### 2. Configuration Setup

Copy the reference configuration file:

```bash
cp .env.example .env
```

Edit `.env` to specify your Telegram credentials and authorized users:

```env
# Telegram configuration
WHATSGRAM_TELEGRAM_TOKEN="" # Keep empty if injecting via process environment
WHATSGRAM_TELEGRAM_CHAT_ID=-1001234567890 # Replace with your forum supergroup ID
WHATSGRAM_TELEGRAM_ALLOWED_USER_IDS="42" # Replace with your numeric Telegram user ID
WHATSGRAM_TELEGRAM_ENDPOINT="https://api.telegram.org"
WHATSGRAM_TELEGRAM_LOCAL_API=false

# WhatsApp configuration
WHATSGRAM_WHATSAPP_PAIRING_MODE=qr
WHATSGRAM_WHATSAPP_PHONE=""
WHATSGRAM_WHATSAPP_HISTORY_POLICY=none

# Database configuration
WHATSGRAM_DATABASE_PATH=data/whatsgram.db
WHATSGRAM_DATABASE_BUSY_TIMEOUT=5s

# Media configuration
WHATSGRAM_MEDIA_TEMP_DIR=""
WHATSGRAM_MEDIA_FFMPEG=ffmpeg
WHATSGRAM_MEDIA_MAX_BYTES=52428800
WHATSGRAM_MEDIA_MAX_DURATION=10m
WHATSGRAM_MEDIA_TIMEOUT=2m
WHATSGRAM_MEDIA_WORKERS=2

# Runtime configuration
WHATSGRAM_RUNTIME_QUEUE_CAPACITY=256
WHATSGRAM_RUNTIME_OPERATION_TIMEOUT=30s
WHATSGRAM_RUNTIME_MAX_ATTEMPTS=5
WHATSGRAM_RUNTIME_RETRY_BASE=1s
WHATSGRAM_RUNTIME_RETRY_MAX=1m
WHATSGRAM_RUNTIME_SHUTDOWN_TIMEOUT=15s
```

Process environment variables override `.env` file values, which override built-in defaults.

---

## Running the Bridge

### Option A: Local Binary

Compile and run the bridge binary:

```bash
# Build static binary
CGO_ENABLED=0 go build -o whatsgram ./cmd/whatsgram

# Run with token from environment
export WHATSGRAM_TELEGRAM_TOKEN="123456789:ABCdefGhIJKlmNoPQRsTUVwxyZ"
./whatsgram -env .env
```

If `.env` is present in the working directory, running `./whatsgram` will automatically load it.

### Option B: Docker Container

Build and run using Docker:

```bash
# Build container image
docker build -t whatsgram:latest .

# Run container with persistent data volume
docker run -d \
  --name whatsgram \
  -e WHATSGRAM_TELEGRAM_TOKEN="123456789:ABCdefGhIJKlmNoPQRsTUVwxyZ" \
  -v $(pwd)/data:/data \
  whatsgram:latest
```

### Option C: Docker Compose

Run using Docker Compose (pulls prebuilt image):

```bash
docker compose pull
docker compose up -d
```

For local development builds from source:

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --build
```

---

## WhatsApp Device Pairing

When first started, Whatsgram logs that WhatsApp is not yet paired. Pairing is initiated directly from the Telegram meta-group (General topic).

### QR Code Pairing (Default)

1. In the Telegram General topic, send:
   ```text
   /pair
   ```
2. The bot replies with a pairing notice containing the QR secret string.
3. Open WhatsApp on your primary phone:
   - Go to **Linked Devices** -> **Link a Device**.
   - Point your phone camera at the QR code generated from the secret.
4. Once authenticated, Whatsgram automatically links the session and begins listening for messages.

### Phone Code Pairing

If pairing via phone code:

1. In the Telegram General topic, send:
   ```text
   /pair code 15551234567
   ```
   (Provide your international phone number without '+' or formatting).
2. The bot replies with an 8-character pairing code.
3. Open WhatsApp on your phone -> **Linked Devices** -> **Link with phone number instead** and enter the code.

### Chat Synchronization

After pairing succeeds, discover your WhatsApp groups and chats:

```text
/sync
```

Whatsgram scans your active WhatsApp chats, creates a dedicated Telegram topic for each chat, and binds the mappings in the database.

---

## Bot Permissions & Telegram Environment

### Required Administrator Permissions

The Telegram bot must be granted the following administrator privileges in the forum supergroup:

| Permission | Purpose |
| --- | --- |
| `can_manage_topics` | Create topics on `/sync`, close topics, and update topic names |
| `can_delete_messages` | Delete bridged messages when revoked on WhatsApp |
| `can_post_messages` | Transmit bridged messages to forum topics |

### Cloud vs Local Bot API Server

Whatsgram supports both the standard Telegram Cloud Bot API and self-hosted `telegram-bot-api` instances:

| Feature | Cloud API (`WHATSGRAM_TELEGRAM_LOCAL_API=false`) | Local API (`WHATSGRAM_TELEGRAM_LOCAL_API=true`) |
| --- | --- | --- |
| **Endpoint** | `https://api.telegram.org` | `http://localhost:8081` (or private IP) |
| **Max Download** | 20 MiB | 2000 MiB (2 GiB) |
| **Max Upload** | 50 MiB | 2000 MiB (2 GiB) |
| **Local I/O** | Network streaming | Fast direct filesystem transfer |

To use a local Bot API server, set `WHATSGRAM_TELEGRAM_LOCAL_API=true` and specify the HTTP URL in `WHATSGRAM_TELEGRAM_ENDPOINT`. HTTP endpoints are permitted only for `localhost`, `127.0.0.1`, and `[::1]` to preserve connection security.

---

## Command Reference & Access Control

All commands require the sender to be an authorized operator listed in `WHATSGRAM_TELEGRAM_ALLOWED_USER_IDS`. Messages from unauthorized users or outside the configured supergroup are ignored.

### Access Control Model

- **Operators**: Every user ID in `WHATSGRAM_TELEGRAM_ALLOWED_USER_IDS` can invoke management commands (`/help`, `/status`, `/sync`, `/pair`, etc.).
- **Account Driver**: The smallest numeric user ID in `WHATSGRAM_TELEGRAM_ALLOWED_USER_IDS` is the designated account driver. Only the driver's emoji reactions and poll votes are propagated to WhatsApp.
- **Original Author Mutation**: Edits and deletions require that the requesting Telegram operator is the original author of the message, unless an administrative override is active.

### Command List

| Command | Context | Description |
| --- | --- | --- |
| `/help` | Any topic | Display list of available commands and usage instructions |
| `/status` | Any topic | Report bridge health, active chat mappings, and pending queue size |
| `/sync` | General | Discover WhatsApp chats and automatically create and bind forum topics |
| `/pair` | General | Initiate QR code pairing session |
| `/pair code <phone>` | General | Initiate 8-character phone pairing code |
| `/bind topic <jid> <topic_id>` | General | Manually bind an existing Telegram topic ID to a WhatsApp JID |
| `/bind message <op_id> <msg_id>` | Any topic | Resolve an uncertain delivery by binding an operation ID to a message ID |
| `/del` | Reply to message | Revoke/delete the replied message on WhatsApp |
| `/pin` | Reply to message | Pin the replied message |
| `/unpin` | Reply or argument | Unpin the specified or replied message |
| `/timer off\|24h\|7d\|90d` | Mapped topic | Configure disappearing messages timer for the active chat |
| `/rename <title>` | Mapped topic | Set a local alias for the Telegram forum topic |
| `/rename-wa <title>` | Mapped topic | Rename the remote WhatsApp group title |
| `/read` | Reply to message | Mark the replied message as read on WhatsApp |
| `/retry <op_id> confirm` | Any topic | Manually re-attempt an uncertain delivery operation |

---

## Directional Support Matrix

Whatsgram handles cross-platform differences through native operations where supported, with graceful emulation fallbacks:

| Feature | WhatsApp -> Telegram | Telegram -> WhatsApp | Notes |
| --- | --- | --- | --- |
| **Text, Unicode, Emoji** | Native | Native | Preserves complete Unicode range and emojis |
| **Formatting** | Emulated conversion | Emulated conversion | Markdown, bold, italic, strikethrough, monospace |
| **Replies & Quotes** | Native | Native | Maps cross-platform message IDs with fallback quoting |
| **Photos & Images** | Native | Native | Auto-converted to compatible formats |
| **Voice Notes (PTT)** | Native | Native | Transcoded via FFmpeg (OGG Opus <-> MP4/AAC) |
| **Audio Files** | Native | Native | Preserves metadata where available |
| **Videos & Video Notes** | Native | Native | Transcoded via FFmpeg as needed |
| **Documents / Files** | Native | Native | Size subject to Cloud (20/50MB) or Local API (2GB) limits |
| **Stickers** | Emulated / Converted | Emulated / Converted | WebP animated and static sticker conversion |
| **Reactions** | Native | Driver only | Propagates driver reactions; other operators summarized |
| **Message Edits** | Native | Native | Enforces 15-minute window for WhatsApp edits |
| **Message Deletions** | Native | Native | Enforces 2-day window for WhatsApp revokes |
| **Disappearing Timers** | Native | Native | Supports standard 24h, 7d, 90d expiration windows |
| **Location & Contacts** | Native | Native | Forwarded with structured attribution cards |

---

## Delivery Guarantees & Uncertainty Windows

### At-Least-Once Delivery to Queue
All incoming network updates from Telegram and WhatsApp are committed into durable SQLite tables (`bridge_inbox`) before acknowledgment to platform servers. If the process is terminated abruptly, pending events remain queued for processing upon restart.

### Two-Phase Dispatch & Uncertainty Window
Outbound messages follow a strict two-phase state machine:
1. **BeginSend**: A unique operation lease token and outbound ID are committed to `bridge_outbox`.
2. **Network Transmission**: The payload is sent to the target platform API.
3. **CompleteDelivery**: On positive acknowledgment, the delivery state becomes `succeeded` and delivered parts are registered.

**Handling Network Timeouts (`Uncertain` State)**:
If a network timeout or connection reset occurs after `BeginSend` has dispatched the request, the result is classified as `Uncertain`. Automatic retries are suspended to prevent accidental duplicate message delivery. Operators can inspect uncertain deliveries using `/status` and reconcile them using `/bind message` or `/retry <op_id> confirm`.

### Automatic Lease Recovery
If a worker crashes while processing a claimed inbox or outbox item, the lease expires after `WHATSGRAM_RUNTIME_OPERATION_TIMEOUT`. A background recovery loop periodically checks for expired leases and returns them to `pending` status.

---

## Backup and Maintenance Procedures

### SQLite Database Backup

Whatsgram runs SQLite in WAL (Write-Ahead Logging) mode with `synchronous=FULL`. A live database file cannot be safely backed up by simply copying `whatsgram.db` because active data resides in `whatsgram.db-wal`.

#### Online Backup (Recommended)

Use the SQLite online backup API via CLI while Whatsgram is running:

```bash
sqlite3 data/whatsgram.db ".backup data/backup-$(date +%Y%m%d%H%M%S).db"
```

#### Offline Backup

Stop the Whatsgram service:

```bash
# If running as systemd service
sudo systemctl stop whatsgram

# Or if running Docker container
docker stop whatsgram

# Archive database and WAL files
tar -czvf whatsgram-backup.tar.gz data/whatsgram.db*

# Restart service
docker start whatsgram
```

### Security and Storage Permissions

- Ensure the database directory is restricted to the Whatsgram process user:
  ```bash
  chmod 700 data
  chmod 600 data/whatsgram.db*
  ```
- Store session databases in private volumes; never expose `/data` on shared file systems.
