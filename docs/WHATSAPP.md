# WhatsApp Adapter

## Scope and Architecture
The `internal/whatsapp` package provides a typed, resilient bridge adapter on top of `go.mau.fi/whatsmeow`. It implements `domain.WhatsAppPort` and `domain.PairingPort`.

### Key Responsibilities
- Offline construction: `New(...)` does not establish network connections.
- Persistent caller IDs: `AllocateSendID()` generates a unique WhatsApp message ID; callers persist this ID before transmitting over the network.
- Synchronous durable admission: Inbound messages are processed via `AddEventHandlerWithSuccessStatus`. Messages are only acknowledged (ACKed) after durable sink commit.
- Owner-other-device events: Events from other linked devices (`EventFromMe`) are retained and attributed rather than blanket-dropped.
- Pairing: `BeginQR` and `BeginPhoneCode` provide dedicated pairing channels. Secrets are bounded and never leaked to logs or public chat rooms.
- Mutation limits: Message edits enforce a conservative 15-minute window; revokes enforce ownership and window constraints.
- Diagnostic logging: Logging uses structured `slog` with sanitized codes; raw stanzas, QR codes, pairing codes, and tokens are strictly excluded.
