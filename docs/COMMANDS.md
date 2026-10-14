# Admin admission and command grammar

`internal/admin` is a pure authorization and parsing package. It performs no API calls, pairing, synchronization, storage, mutations, retries or bindings. Every `Command` decision has code `parsed-command` and the fixed reason `Parsed intent requires executor validation; no action has run.` All command execution is not implemented in this package, including later-phase commands. A future coordinator must retain the admitted input's source/chat/actor/message/topic scope and validate it against durable mappings and platform rights before execution.

## Admission

Construct `NewPolicy(config.Telegram.ChatID, config.Telegram.AllowedUserIDs, botUsername)` after trusted runtime obtains the bot's actual username. Do not pass tokens. The constructor requires a negative meta-group ID, a nonempty list of positive numeric users, and a literal username of 5-32 ASCII characters starting with a letter and otherwise containing only letters, digits or underscores. Supply the username without `@`; whitespace is not repaired. Invalid policy errors are always `invalid admin policy`. The allowlist is copied and deduplicated; a zero policy rejects Telegram admission.

- Telegram admission requires the exact configured `ChatID`, `HasUser`, a positive allowlisted `UserID`, and no `SenderChat`. Anonymous/service identities and display names cannot authorize anything. Private inbound management is rejected by default.
- WhatsApp input is data regardless of text, slash prefixes, command entities or Telegram actor fields. `Decide` returns `Bridge` with code `source-data`, never `Command`. This is not permission to execute WhatsApp-origin management.
- Unknown source strings are ignored. Only exact `telegram` and `whatsapp` strings are recognized.
- Authorized Telegram data returns `Bridge`. A leading slash after surrounding Unicode whitespace trimming, or `BotCommand=true`, prevents ordinary forwarding. Unknown, malformed, entity-only ambiguous and other-bot commands never return `Bridge`.
- `BotCommand` means the adapter observed a bot-command entity and conservatively reserves the input from forwarding. The boolean carries no entity offset: if it is true but the text has no leading command, the package rejects rather than guessing. Without that flag, a slash elsewhere in ordinary text remains data.

## Command syntax

Commands use exact lowercase names. `/name` and `/name@configured_username` are accepted. Only the addressed ASCII username is case-insensitive; a syntactically valid foreign username is ignored with code `other-bot`. Non-ASCII, malformed, multiple-`@` or empty addressed usernames reject before case matching. No foreign username is inferred or repaired.

Command text must be valid UTF-8 and at most 4096 bytes, including surrounding whitespace. This is a conservative parser limit, not a claim about Telegram's character limit. Unicode whitespace separates literal arguments. There is no shell, quoting, escape or environment-variable expansion. Context `ThreadID` and `ReplyMessageID` must be in `0..2147483647`, with zero meaning absent. Explicit numeric identifiers use canonical ASCII decimal `[1-9][0-9]*` in `1..2147483647`, without a sign or leading zeros. Source `MessageID` is retained in `Input` for the future coordinator and is not a parsed argument.

| Grammar | Parsed fields and context |
| --- | --- |
| `/help` | `Name=help`; no arguments |
| `/status` | `Name=status`; no arguments |
| `/sync` | `Name=sync`; no arguments |
| `/pair qr` | `Name=pair`, `Mode=qr`; no private argument |
| `/pair code international-digits` | `Name=pair`, `Mode=code`, private `Argument` contains 7-15 ASCII digits with first digit nonzero; no `+`, spaces or device suffix |
| `/del` | Positive reply required; `TargetMessageID=ReplyMessageID`; no arguments |
| `/pin` | Positive reply required; `TargetMessageID=ReplyMessageID`; no arguments |
| `/unpin` | Positive reply required; `TargetMessageID=ReplyMessageID` |
| `/unpin message-id` | Explicit positive numeric target; no reply may also be present |
| `/timer off` | Positive topic required; `ThreadID` copied, `Timer=0` |
| `/timer 24h` | Positive topic required; `Timer=24*time.Hour` |
| `/timer 7d` | Positive topic required; `Timer=7*24*time.Hour` |
| `/timer 90d` | Positive topic required; `Timer=90*24*time.Hour` |
| `/rename text` | Positive topic required; private `Argument` is a local topic alias |
| `/rename-wa text` | Positive topic required; private `Argument` is an explicit WhatsApp group rename intent |
| `/read` | Positive reply required; `TargetMessageID=ReplyMessageID`; no arguments |
| `/retry operation-id confirm` | Private `OperationID`, `ConfirmDuplicateRisk=true`; exact literal `confirm` is mandatory |
| `/bind topic whatsapp-jid topic-id` | `Name=bind`, `Mode=topic`, private `Argument` contains the literal JID, `ThreadID` is the explicit positive topic ID |
| `/bind message operation-id native-message-id` | `Name=bind`, `Mode=message`, private `OperationID`, `TargetMessageID` is an explicit positive Telegram message ID |

The rename tail is trimmed at its edges but internal spacing is preserved. It must contain 1-128 Unicode code points, with no control or Unicode format characters. This intentionally rejects bidi controls, zero-width format characters and multiline names. Limits are conservative local syntax policy; WhatsApp server title limits/permissions still need runtime validation.

Operation IDs are 1-128 ASCII characters: first `[A-Za-z0-9]`, remaining `[A-Za-z0-9_-]`. They are identifiers, never paths or shell input. `/retry` acknowledges duplicate risk syntactically; it does not authorize an automatic retry or establish reconciliation.

Accepted JID literals are deliberately limited, without normalization:

- `international-digits@s.whatsapp.net`, where the digit rule matches `/pair code`.
- `digits@lid`, with 1-20 ASCII digits and first digit nonzero.
- `digits@g.us`, or `digits-digits@g.us`, with each component 1-20 ASCII digits and first digit nonzero.

Device-qualified JIDs, broadcast/newsletter domains, uppercase domains and arbitrary strings reject. Syntax does not verify account membership, JID existence, LID/phone relationships, group scope or topic availability. `/bind message` is explicitly Telegram uncertain-create recovery; it is not a WhatsApp native string message-ID binding. Later state validation must confirm the operation belongs to the configured meta-group, correct topic generation and correct source/target account.

## Account identity and original ownership

All allowlisted numeric users are operators for explicit commands. `DriverID()` is the smallest numeric allowlisted user, independent of list order and duplicates. `CanDriveAccount(userID)` permits only that ID for reactions and poll votes as the one linked WhatsApp account. A changed allowlist changes the driver on reconstruction/restart; diagnostics must show the numeric driver without conversation content or secrets.

`DecideAccountAction(input)` requires Telegram admission and returns visible `Reject` with code `not-account-driver` for another authorized operator. It is the gate for both reaction and poll-vote intents, not an API executor. The adapter must resolve the original chat/topic context for poll answers from trusted mappings; a poll-answer update may not carry a chat ID. Anonymous aggregate reactions cannot produce an actor-resolved intent. Do not fabricate an identity or select an actor by display name.

`CanMutateOriginal(actorID, originUserID, explicitOverride)` requires an authorized positive actor and positive original author ID. It permits the actor's own originals, or an explicit override. `DecideMutation(input, originUserID, explicitOverride)` combines that test with exact Telegram admission and prevents command text becoming mutation data. Both helpers are Telegram-origin ownership checks, not admission for WhatsApp lifecycle events. Missing original author IDs fail closed even with override. The executor must load the real original author, audit the explicit override, and still check platform ownership, time windows, rights, target/version/tombstone and scope. An override is not permission to edit another WhatsApp participant's message. No override command syntax is introduced here.

The numeric-only helpers do not verify chat or anonymous identity themselves. Use the decision wrappers for normalized reaction/vote/edit intents; command execution must independently revalidate author ownership after resolving reply mappings. `Bridge` means admitted data/action intent, not that native transmission or mutation has occurred.

## Privacy and future execution boundary

`ParsedCommand.Argument` and `OperationID` are `PrivateValue` objects whose only trusted plaintext accessor is `Reveal()`. Closure-backed capture avoids raw string fields surviving invalid `fmt` verb diagnostics. String, GoString, fmt and JSON representations redact the values. Tests include pointers, aggregate commands/decisions and invalid verbs. Never log values after `Reveal()`.

The redaction guarantee covers parser-produced private values and fixed decisions, not arbitrary caller-mutated exported fields or the raw `Input.Text`. Never dump `Input` or raw conversation text into logs. Parsed command/decision objects do not retain raw command bodies; rejected decisions carry no parsed arguments. Log fixed code/reason and separately approved numeric diagnostic identifiers only. Redacted JSON is not a persistence format for command payloads.

Pairing QR/code output must be sent privately to the allowed numeric operator, never into the forum/meta-group or logs. Accepting `/pair` in the meta-group is only an intent. Private pairing notification is outbound-only; private inbound management remains unauthorized until an explicit separate contract exists.

Timer and rename commands require positive topic context here, but the package has no store and cannot prove that a topic is mapped. The executor must resolve an active, correct-generation mapping before using any command. `pin`/`unpin` do not imply verified native WhatsApp pin support. Follow `SUPPORT_MATRIX.md` for platform-specific native/emulated/unavailable behavior, and never silently turn a parsed later-phase intent into a fake success.
