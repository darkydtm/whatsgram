# Coordinator Architecture and Operation

## Scope
The `internal/coordinator` package implements the bridge coordinator engine. It orchestrates:
1. `domain.TelegramPort` - Telegram event intake, outbound messaging, and topic management
2. `domain.WhatsAppPort` - WhatsApp event intake, outbound messaging, and chat discovery
3. `domain.PairingPort` - WhatsApp QR and phone code pairing lifecycle
4. `admin.Policy` - Command parsing, authorization, and message routing policy
5. `store.Store` - Durable inbox, outbox, mappings, echo tracking, and leasing
6. `config.Config` - Runtime parameters, endpoints, and timeouts

## Inbound Event Processing and Planning
The coordinator processes inbound events via `StepInbox(ctx)` and `RunInboxLoop(ctx)`:

- **Claiming Inbox**:
  - Events are claimed via `store.ClaimInbox(ctx, now, leaseDuration)`.
  - Lease duration is configured via `config.Runtime.OperationTimeout` (default 30 seconds).

- **Telegram Events**:
  - The event payload is inspected for leading commands or bot command entities.
  - If a command is recognized, it is authorized and parsed via `admin.Policy.Decide` and executed by `AdminExecutor`.
  - The inbox item is completed without forwarding to WhatsApp.
  - Normal messages are evaluated against admin policy. If admitted:
    - The bound WhatsApp chat is resolved via `store.ChatByTopic(ctx, chatID, threadID)`.
    - If the chat is unbound or not active, the inbox item is completed without forward.
    - If active, delivery is planned to WhatsApp:
      - `delivery.Platform = domain.WhatsApp`
      - `delivery.WhatsAppSendID = waPort.AllocateSendID()`
      - `delivery.EchoActor = chat.WhatsAppJID`
      - Content: original text content
      - Dispatched via `store.PlanEvent(ctx, event, token, []domain.Delivery{delivery}, now)`

- **WhatsApp Events**:
  - The bound Telegram topic is resolved via `store.Chat(ctx, waJID)`.
  - If unbound or inactive, the inbox item is completed without forwarding.
  - **Echo Protection**:
    - If the sender is the connected WhatsApp account (`FromMe`):
      - If the message ID matches a known bridge-dispatched send ID (tracked in memory, `store.Part`, or `store.MatchEcho`), forward is suppressed and the inbox item is completed.
      - If sent by the owner from another device (phone or web client), text is formatted with `"You: <text>"` and forwarded to Telegram.
    - If from other WhatsApp participants:
      - Group chats (`@g.us`) format text with `"[<Author>]: <text>"`.
      - Direct chats preserve original text.
      - Delivery is planned to Telegram topic:
        - `delivery.Platform = domain.Telegram`
        - `delivery.TopicGeneration = chat.Generation`
        - `delivery.EchoActor = chat.WhatsAppJID`
        - Content: formatted text content
        - Dispatched via `store.PlanEvent(ctx, event, token, []domain.Delivery{delivery}, now)`

## Outbound Delivery Processing
Outbound delivery is executed via `StepDelivery(ctx)` and `RunDeliveryLoop(ctx)`:

- **Claiming Delivery**:
  - Deliveries are claimed from `store.ClaimDelivery(ctx, now, leaseDuration)`.

- **Two-Phase Dispatch**:
  - Phase 1: `store.BeginSend(ctx, delivery.ID, leaseToken, now)` commits send-started state and echo reservations.
  - Phase 2: Transmit to destination port:
    - Telegram: `tgPort.SendText(ctx, req)`
    - WhatsApp: `waPort.SendText(ctx, req)`

- **Completion Handling**:
  - On successful transmission, `store.CompleteDelivery(ctx, delivery.ID, leaseToken, completion, now)` atomically commits delivered parts, echo keys, and marks outbox state as `succeeded`.

- **Error Classification and Recovery**:
  - If an error occurs before send started:
    - Transient or rate-limited errors are deferred via `store.FailDelivery` with state `pending` and backoff.
    - Permanent or permission errors are failed via `store.FailDelivery` with state `dead` or `blocked`.
  - If an error occurs after send started:
    - Once send has begun, automatic retry is unsafe; transient or uncertain outcomes are classified into state `uncertain` with error class `Uncertain`.
    - Permanent errors are marked `dead`.

## Admin Command Execution
The `AdminExecutor` handles management commands within the Telegram meta-group:

- `/help`: Replies with list of available commands and usage instructions.
- `/status`: Replies with bridge status, active chat count, and pending delivery counts.
- `/pair`: Calls `pairingPort.BeginQR(ctx)`, awaits QR secret, and replies with pairing notice.
- `/pair <code>`: Calls `pairingPort.BeginPhoneCode(ctx, phone)` and replies with code pairing notice.
- `/sync`: Calls `waPort.DiscoverChats(ctx)`. For each candidate not already bound, creates a Telegram topic via `tgPort.CreateTopic`, binds topic via `store.BindTopic`, and replies with summary.
- `/bind <topic_id> <jid>`: Explicitly binds a Telegram topic to a WhatsApp chat JID.

## Safety, Concurrency, and Secrecy
- **Shutdown**: All worker loops (`RunInboxLoop`, `RunDeliveryLoop`, `runLeaseRecoveryLoop`) respect `context.Context` cancellation and terminate gracefully.
- **Race Safety**: State is guarded by atomic operations and synchronizing primitives; tests pass under `go test -race`.
- **Secrecy**: Tokens, pairing secrets, QR secrets, and message bodies are excluded from standard log calls and redacted by types implementing `fmt.Formatter` and `slog.LogValuer`.
