# Storage contract

## Lifecycle and compatibility

`internal/store` owns one `database/sql` pool and its whatsmeow
`sqlstore.Container`. Open a filesystem database with:

```go
s, err := store.Open(ctx, path, 5*time.Second)
```

Call `s.Close()` once workers stop. Close is idempotent. Do not independently
close `s.SessionStore()`: that container owns the same database. The session
container is for adapter composition, not a second copy of session keys.
No general SQL accessor is exported.

Pinned direct dependencies:

- `go.mau.fi/whatsmeow v0.0.0-20261014094007-f16f9ca30d6a`
- `modernc.org/sqlite v1.60.1`
- Go module `github.com/darkydtm/whatsgram`, minimum Go `1.26.0`

The driver and sqlstore dialect are both `sqlite`. Application package builds
and tests work with `CGO_ENABLED=0`; the Go race detector requires a compatible
`CGO_ENABLED=1` test environment. Synthetic local device/session save, reopen,
delete and cascading deletion are tested without network or pairing.

Paths become absolute filesystem paths and are encoded as file URLs. Filenames
containing spaces, `?`, `&` or `#` cannot inject pragmas. Each physical connection
receives foreign keys on, WAL, configurable busy timeout and synchronous FULL.
The pool permits four open/idle connections. Transactions use `BEGIN IMMEDIATE`
so a read-then-write claim cannot silently race another writer. Busy is bounded
by the configured timeout and returned as `ErrBusy`; there is no hidden retry
loop. Busy timeout must be between one millisecond and one minute.

New parent directories are created with mode `0700`; existing parents are not
chmodded. Database, WAL and SHM files are protected with mode `0600`. Keep the
containing directory private: file modes do not protect a shared writable parent
from rename or symlink attacks. The database must be a regular file; in-memory
or shared-cache special DSNs are not supported. Back up consistently with WAL,
using a SQLite-aware backup or after all writers close. Copying the main file
alone during activity is not a supported backup.

## Schema and migrations

Bridge migration version **1** is embedded from `migrations/001-initial.sql`.
`bridge_migration(version, checksum, applied_at_ms)` records SHA-256 of the exact
migration bytes. Applied versions must be contiguous and match embedded
checksums; gaps, future versions and altered checksums produce `ErrSchema`.
Each migration and its ledger insert commit as one transaction. A failed later
migration leaves earlier successful migrations committed, but never leaves the
failed migration partially applied. Never edit an already deployed migration.
`PRAGMA user_version` and upstream whatsmeow tables belong to sqlstore.

Tables:

| Table | Durable responsibility |
| --- | --- |
| `bridge_chat` | Canonical WA JID, TG chat/nullable topic, generation and explicit state |
| `bridge_message` | Logical source identity, effective version/text, keep flag and tombstone |
| `bridge_part` | Every contextual native ID, index, role, ownership, capability flags, deletion/retirement metadata |
| `bridge_source_state` | Source-specific version and tombstone per mapped part |
| `bridge_inbox` | Context-scoped dedupe key, normalized/raw event payload, digest and fenced admission work |
| `bridge_outbox` | Immutable operation intent, dependencies, generation, reserved WA ID, lease and send/result state |
| `bridge_echo` | Exact platform/chat/actor/event/operation/target/digest reservation |
| `bridge_timer` | Absolute expiry/mapping-wait deadline, version and fenced state |
| `bridge_cursor` | Telegram next offset and stream epoch |

Foreign keys, uniqueness and enum/boolean/index/capability checks reject invalid
rows. One active primary part exists per logical message/platform. Remote ID
uniqueness includes platform, chat and author. Part index uniqueness includes
logical message and platform. Message tombstones and retired parts persist.
No poll, identity-link, authorization or crypto-copy framework is introduced.

## Admission and checkpoint

```go
Accept(context.Context, domain.Event) (domain.Admission, error)
AcceptTelegramBatch(context.Context, []domain.Event, int64, string) error
Cursor(context.Context, string) (store.Cursor, error)
```

Pass `InboxID=0` on admission. The returned ID identifies committed work; only
claims populate `Event.InboxID`. Event keys are context-scoped by source, chat,
target author/native ID, actor, caller key and source epoch. Reuse in another
chat/author context is distinct. A repeated key in the same context must carry
identical normalized/raw JSON bytes, or admission returns `ErrConflict` without
altering the earlier event. Adapter normalization must be deterministic, including
`OccurredAt`, raw envelope and metadata; local receipt time is not event identity.

Unknown kinds are durably quarantined, not discarded or acknowledged from an
in-memory queue. Malformed/unbounded input is rejected. A serialized event is
limited to `domain.MaxEventBytes` (1 MiB), including base64 encoding of byte
slices. Reference components have explicit smaller bounds; text entity spans
use UTF-16 boundaries and cannot split surrogate pairs.

`AcceptTelegramBatch` atomically commits every new or duplicate admission and
`bridge_cursor(stream='telegram')`. Any conflict, malformed event or saturation
rolls back both the events and cursor. Events must be Telegram and match the
batch epoch. Offsets cannot regress and an existing epoch cannot be silently
replaced. Long-idle epoch transition policy is not implemented. `TelegramPort.Run`
is an adapter lifecycle contract, not a durable checkpoint guarantee; poller or
webhook implementations must use this commit path before acknowledging input.

`OpenWithOptions(ctx,path,busyTimeout,Options)` sets admission bounds. Defaults
are 10,000 unresolved inbox rows/64 MiB and 10,000 unresolved outbox rows/64 MiB.
Count and byte limits must all be positive. Duplicates still succeed at capacity.
Quarantine counts toward inbox capacity; uncertain/blocked deliveries count
against outbox capacity. Processed payloads remain retained and need a later
explicit cleanup policy.

## Chat bindings

`DiscoverChat` admits canonical device-free group, PN or LID JIDs only; it does
not merge PN/LID identities by display text. Repeated discovery updates title
and partial-history information but never rebinds a topic or target metagroup.
`Chat` and `ChatByTopic` expose safe lookup. Discovery leaves a nullable topic.
`BindTopic` accepts the same binding/generation idempotently. Initial binding is
generation 1; replacement is exactly generation + 1 and requires explicit
missing/uncertain/blocked state. `SetChatState` is generation-fenced.
States are discovered, active, closed, missing, uncertain and blocked. A topic is
never recreated by title. Claim and begin-send recheck state and generation.

Topic create/rename work uses a message-less outbox intent. Its send result must
use `CompleteTopicDelivery`, not `CompleteDelivery`. Creation commits the new
binding and succeeded result in one transaction and advances the generation.
Rename requires the existing exact topic/generation. No remote topic method is
implemented in this package.

## Work, mapping and recovery

```go
ClaimInbox(context.Context, time.Time, time.Duration) (domain.Event, string, error)
PlanEvent(context.Context, domain.Event, string, []domain.Delivery, time.Time) error
PlanEventWithTimers(context.Context, domain.Event, string, []domain.Delivery, []store.TimerSpec, time.Time) error
ClaimDelivery(context.Context, time.Time, time.Duration) (domain.Delivery, error)
BeginSend(context.Context, int64, string, time.Time) error
CompleteDelivery(context.Context, int64, string, domain.Completion, time.Time) error
FailDelivery(context.Context, int64, string, domain.RecoveryDecision, time.Time) error
RecoverExpiredLeases(context.Context, time.Time) error
```

All mutations require current random fencing tokens and an unexpired deadline.
Deadlines are checked against the greater of supplied `now` and actual local
wall time after acquiring the write transaction and before its final state
transition. Workers cannot extend a lease by passing a stale claim timestamp.
Lease TTL is one millisecond through one hour; no renewal API exists yet. Set
network deadlines shorter than the lease. `ErrNoWork` is the sole no-work
sentinel; `ErrLeaseLost` is never silently treated as success.

Claims exclude leased aliases of the same logical message, not just identical
source IDs. Outbox claims serialize a logical message and chat-level operations.
An unresolved earlier operation/dependency cannot be overtaken; unrelated
messages can proceed independently. Expired leases require explicit recovery.

`PlanEvent` verifies the immutable admitted event, lease, source ownership for
create/edit, stored source/effective version, tombstone, chat/generation and
intent bounds in one transaction. It updates source/message/part state, inserts
outbox intents and completes inbox work with no external calls.
`PlanEventWithTimers` adds absolute timer scheduling in the same transaction;
a timer with `MessageID=0` binds to that plan's logical message. A stale or
duplicate state revision suppresses its outbox/timer plan; late edits cannot
resurrect tombstones. Revoke dominates revisions and supersedes unsent creates,
edits and reactions. Newer edits supersede unsent older edits.

Only message create/edit/revoke and explicit chat-operation planning are
implemented. Other known event kinds remain durable but require coordinator
policy, `DeferInbox` or `QuarantineInbox`; they are not silently completed by an
unsupported default path. An edit without mapping returns `ErrNoWork` and leaves
the lease intact. Bounded waiting/fallback policy belongs to the coordinator.

`SourceVersion.Compare` compares `Order` only within the same Kind and StreamEpoch.
At equal order, a different opaque Tie or Digest is a conflict, not lexical LWW.
Different source kinds/epochs are incomparable. Adapter-specific causal conflict
resolution is not implemented; cross-source edits can require quarantine rather
than a fabricated global timestamp order. Revocations do not require comparable
source versions. `OccurredAt` is not used as an edit clock.

Every WA intent reserves its caller-provided outgoing ID transactionally with a
unique platform/chat/send-ID constraint. `BeginSend` commits send-started state
and an exact WA echo reservation before transport. It verifies mapped mutation
targets, stored ownership/capability and current deletion/retirement state.
`MatchEcho` requires the exact operation tuple and digest, never prefix or FromMe.
Remote per-operation rights/windows still need adapter validation.

`CompleteDelivery` commits all returned parts, source-state acknowledgment,
exact echoes, result and succeeded state atomically. Conflicting parts roll back
the whole result. `AppliedVersion` must exactly equal the immutable intent.
A result received after a newer tombstone retains the native IDs as deleted
metadata for compensation without restoring effective text. This is local
state, not proof of remote deletion. Results after a replaced topic generation
are rejected and need explicit uncertain/reconciliation handling.

`FindParts(ref)` returns every related part including original, retired and
deleted metadata. `Part(ref)` resolves one exact part. `Message(ref)` exposes
logical ID/chat/version/tombstone/keep. `SourceState(ref)` exposes per-part
version/deletion. `RetirePart(ref,expectedVersion)` never removes its ID.
Incoming source parts conservatively start non-bridge-owned with no mutation
capabilities; no authority is inferred from a platform or bare ID.

`FailDelivery` accepts pending, uncertain, blocked or dead with typed error
classes. Pending retry is allowed only before `BeginSend`, with transient or
rate-limited class and a future availability time. Once send has begun, no
automatic safe retry is assumed even for a transient transport error. Recovery
makes leased unsent work pending and begun sends uncertain. Uncertain outcomes
remain nonclaimable and block dependent work until a later explicit operator or
reconciliation contract is implemented. There is no exactly-once remote SLA.
`RecoveryDecision.Code` is not stored or logged.

## Timers

```go
ScheduleTimer(context.Context, store.TimerSpec) (int64, error)
ClaimTimer(context.Context, time.Time, time.Duration) (store.Timer, error)
CompleteTimer(context.Context, int64, string, []domain.Delivery, time.Time) error
SetKept(context.Context, domain.MessageRef, domain.SourceVersion, bool) error
```

Kinds are expire and mapping_wait only. A timer key is logical message, kind
and exact version. Its absolute deadline cannot be reset by duplicate scheduling.
Claim returns all mapped IDs, suppresses kept/stale/tombstoned expiry jobs and
fences concurrent workers. Completion rechecks version/keep/tombstone before
planning deletion. For expiry, the supplied deletion intents must cover every
currently deletable, owned, nonretired part exactly once; the store derives that
set from mappings, not a caller count. Completion atomically inserts those
outbox intents, marks the effective message tombstoned and completes the timer.
Mapping-wait completion is structural and does not delete the message.

A changed version cancels an old timer. The future coordinator must carry the
original absolute source deadline into a replacement-version timer using
`PlanEventWithTimers`, not reset expiry from edit or restart time. No chat timer
policy, remote keep synchronization, Telegram 48-hour limit enforcement or
privacy retention sweeper exists here. Expired effective text is cleared, but
raw inbox/outbox/result payloads remain retained. These primitives do not claim
remote or local-content disappearance guarantees.

## Errors and diagnostics

`ErrSchema`, `ErrStorage`, `ErrBusy`, `ErrNoWork`, `ErrLeaseLost`, `ErrSaturated`,
`ErrConflict` and `ErrInvalid` are stable sentinels. Context cancellation/deadline
errors propagate without SQL/path/body details. Store errors never include SQL
arguments, database paths or message content. SQL constraint errors map to
conflict; busy/locked map to bounded busy; I/O/full/corruption map to storage
failure, not successful admission.

Domain Event, TextContent, Delivery, SendTextRequest, PairingNotice and
AdapterError redact ordinary fmt, direct slog and public JSON output. Their
MarshalJSON methods also protect slices, maps and wrappers passed to the normal
slog.JSONHandler. Storage uses explicit private persistence codecs that retain
full event, text and intent fidelity; public JSON is diagnostic, not a wire or
persistence format.

PairingNotice.Secret is an opaque closure-backed PairingSecret, constructed by
NewPairingSecret(string). Only a trusted notification consumer may call Reveal()
to obtain exact bytes. Its zero value reveals an empty string. Direct, pointer
and nested malformed fmt diagnostics cannot reflect the captured pairing value.
The type is non-comparable and does not provide memory zeroization.

Message text and other raw exported fields remain ordinary data. Malformed fmt
verbs can bypass Formatter and expose these fields; arbitrary diagnostic dumps
of input objects are prohibited. Use the JSON/slog boundary or ordinary fmt,
never individual raw fields, reflection or private persistence bytes. This is
not universal malformed-fmt protection for conversation data. AdapterError prints
only a validated class and allowlisted safe code; its Cause is available for
classification but must not be logged directly. Never log session devices or
sqlstore structures. Synthetic tests are not live cryptographic/session data.

## Verification boundaries

Local tests use real temporary SQLite databases, four held physical connections,
synthetic sqlstore persistence, migration rollback/checksum/future rejection,
transactional queue bounds and batch checkpoints, simultaneous workers and
independent pools, expiry/token fences, uncertain restart recovery, contextual
all-part mapping, tombstones and absolute timer restart checks. Normal package
build/test/vet and cgo-enabled race tests are exercised locally.

Not established here: whole bridge build/runtime, parent integration CI,
independent review acceptance, live Telegram/WhatsApp behavior, process kill or
power-loss durability, sustained production load, disk-full fault injection,
remote reconciliation, authorization policy, media/poll/pin behavior or secure
retention cleanup. Adapters and coordinator are intentionally not implemented.
