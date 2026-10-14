# Configuration

The `internal/config` package provides data only. It does not connect to Telegram or WhatsApp, open a database, create directories, start FFmpeg, or read session contents.

```go
func Default() Config
func Load(path string, lookup func(string) (string, bool)) (Config, error)
func (c Config) Validate() error
```

`Config` contains concrete `Telegram`, `WhatsApp`, `Database`, `Media`, and `Runtime` values. `Default()` returns fresh safe resource defaults with no access credentials. It is deliberately invalid until a token, chat ID, and allowlist are supplied. `Validate()` checks a value without modifying it; `Load()` validates and then removes trailing endpoint slashes and deduplicates allowlist IDs in first-seen order. Any loading error returns a zero `Config`, never partially loaded secrets.

## Sources and precedence

Environment values override YAML values, which override defaults. Pass an empty path to skip file loading. A nonempty path must be readable; there is no silent fallback. A nil lookup uses `os.LookupEnv`. Inject a lookup returning `("", false)` to disable environment loading, or inject a map-based lookup for tests.

Copy `config.example.yaml`, replace its fake negative chat ID and positive user ID with the intended access settings, and supply `WHATSGRAM_TELEGRAM_TOKEN` through the process environment. The example deliberately cannot load without a token. Do not commit real tokens, IDs, phone numbers, session data, or private configuration files.

Present-but-empty environment values override YAML; they never inherit a YAML token. Empty token, chat ID, allowlist, endpoint, pairing/history policy, database path, FFmpeg path, bool, number, or duration fails. Empty `WHATSAPP_PHONE` is allowed only in QR mode. Empty `MEDIA_TEMP_DIR` selects the caller's runtime `os.TempDir()` behavior; the config loader does not resolve it.

YAML uses nested lowercase snake_case keys as shown in the example. Durations are strings such as `30s` or `2m`, not numeric nanoseconds. Allowlist YAML values are a list of integer IDs. Files are limited to 1 MiB (1,048,576 bytes), including comments. Unknown fields, duplicate mapping keys, explicit null values, empty/non-map documents, malformed YAML, and extra documents (including an empty second document) fail. An empty mapping `{}` is allowed with environment access settings. Syntax/type errors remain errors even if environment values would replace the affected field. Semantic validation runs on the final merged configuration.

## Environment fields

All names below have the `WHATSGRAM_` prefix. The suffix matches the nested YAML key, with underscores and uppercase letters.

| Suffix | Type | Default |
| --- | --- | --- |
| `TELEGRAM_TOKEN` | required secret string | empty |
| `TELEGRAM_CHAT_ID` | required negative int64 | 0 (invalid) |
| `TELEGRAM_ALLOWED_USER_IDS` | required comma-separated positive int64 IDs | empty (invalid) |
| `TELEGRAM_ENDPOINT` | URL | `https://api.telegram.org` |
| `TELEGRAM_LOCAL_API` | boolean | `false` |
| `WHATSAPP_PAIRING_MODE` | `qr` or `code` | `qr` |
| `WHATSAPP_PHONE` | international digits | empty |
| `WHATSAPP_HISTORY_POLICY` | `none` or `recent` | `none` |
| `DATABASE_PATH` | nonblank path | `data/whatsgram.db` |
| `DATABASE_BUSY_TIMEOUT` | duration | `5s` |
| `MEDIA_TEMP_DIR` | path | empty |
| `MEDIA_FFMPEG` | nonblank executable/path | `ffmpeg` |
| `MEDIA_MAX_BYTES` | int64 bytes | `52428800` |
| `MEDIA_MAX_DURATION` | duration | `10m` |
| `MEDIA_TIMEOUT` | duration | `2m` |
| `MEDIA_WORKERS` | int | `2` |
| `RUNTIME_QUEUE_CAPACITY` | int | `256` |
| `RUNTIME_OPERATION_TIMEOUT` | duration | `30s` |
| `RUNTIME_MAX_ATTEMPTS` | int | `5` |
| `RUNTIME_RETRY_BASE` | duration | `1s` |
| `RUNTIME_RETRY_MAX` | duration | `1m` |
| `RUNTIME_SHUTDOWN_TIMEOUT` | duration | `15s` |

Environment integers are decimal and range checked. Allowlist entries permit surrounding whitespace, but empty entries, trailing commas, zero, negatives, and overflow fail. Duplicates are removed. Booleans accept Go's `strconv.ParseBool` forms (`true`/`false`, `TRUE`/`FALSE`, `True`/`False`, `1`/`0`, `t`/`f`, `T`/`F`). Other scalar whitespace is not silently trimmed for parsing.

## Access, endpoints, and pairing

The chat ID must be nonzero and negative, representing the selected supergroup; the loader cannot verify the remote chat type or membership. Every allowed user ID must be positive. The allowlist is mandatory and must not be interpreted as allowing all users when empty.

Endpoints permit arbitrary HTTPS hosts and paths, or HTTP only with the literal host `localhost`, `127.0.0.1`, or `[::1]`. Userinfo, query strings (including a bare `?`), fragments (including a bare `#`), missing hosts, other schemes, and ports outside 1-65535 fail. Endpoint checks do not resolve DNS or probe network services. `local_api: true` is explicit mode data; it neither permits remote HTTP nor increases any media limit.

Code pairing requires a phone with 7-15 ASCII digits, a nonzero first digit, no `+`, spaces, or punctuation. QR mode permits an empty phone; any supplied phone must still satisfy the same format. This is format validation, not proof of a valid or reachable telephone number. History is opt-in `recent` or default `none`; configuration never initiates archive/history crawling.

## Resource bounds

All durations must be greater than zero and at most 24 hours. Media bytes must be 1 through 10 GiB (10,737,418,240 bytes). Workers must be 1-64, queue capacity 1-65,536, and max attempts 1-100. Retry base must not exceed retry max. Limits are defensive configuration ceilings, not a promise that Telegram or WhatsApp accepts that media size or duration. Local Telegram mode does not remove WhatsApp limits.

Database and FFmpeg paths cannot be blank. Paths are not created, expanded, or checked for existence by this package. Resource consumers must perform their own runtime checks, safe directory permissions, API-specific media limits, and shutdown/cancellation handling.

## Secret-safe diagnostics

Errors name fixed fields or parser categories, never input values, YAML source lines, filesystem paths, or wrapped decoder errors. The package performs no logging. `Config` and `Telegram` values and pointers redact all standard `fmt` verbs, `String()`, and `GoString()`. Their JSON output is the fixed object `{"redacted":true}` rather than a configuration export, so copied secrets in other fields cannot leak through those surfaces.

The concrete token remains available to authorized consumers as `c.Telegram.Token`. Do not format individual sensitive fields, use reflection-based dumps, or serialize the configuration with other encoders. Redaction is not a security boundary against code that directly reads those values. Keep injected lookups trusted; this package cannot suppress logging, panic messages, or side effects inside a caller's lookup.
