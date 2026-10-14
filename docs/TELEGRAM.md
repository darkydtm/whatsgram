# Telegram Adapter

## Scope and Architecture
The `internal/telegram` package provides a typed, resilient bridge adapter on top of `github.com/go-telegram/bot v1.27.0`. It implements `domain.TelegramPort`.

### Key Responsibilities
- Offline construction: `New(...)` does not dial Telegram or query `getMe`.
- Durable batch admission: `RunPolling` manages long-polling `getUpdates` with explicit `allowed_updates`. Updates are converted into normalized `domain.Event`s and durably committed with the next update offset to `AcceptTelegramBatch` before advancing the remote cursor.
- Rate limits and backoff: Respects HTTP 429 `retry_after` parameters and applies bounded jittered backoff on transient errors.
- Token secrecy: `config.Secret` is accessed only at the concrete HTTP transport boundary via `Reveal()`. Error formatting and logging never expose tokens or raw payload secrets.
- Chat and topic management: Provides explicit methods for `CreateTopic` and `RenameTopic` within the configured supergroup.
- Error classification: Categorizes errors into typed `domain.AdapterError` (`Transient`, `Permanent`, `RateLimited`, `Permission`, `Conflict`, `Uncertain`).
