package telegram

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/darkydtm/whatsgram/internal/config"
	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/go-telegram/bot/models"
)

const MaxUpdateBytes = 128 << 10

type Metadata struct {
	UpdateType    string          `json:"update_type"`
	Admission     string          `json:"admission"`
	Reason        string          `json:"reason,omitempty"`
	ThreadID      int64           `json:"thread_id,omitempty"`
	MessageID     int             `json:"message_id,omitempty"`
	PollID        string          `json:"poll_id,omitempty"`
	ForwardOrigin json.RawMessage `json:"forward_origin,omitempty"`
	Reply         json.RawMessage `json:"reply,omitempty"`
	Entities      json.RawMessage `json:"entities,omitempty"`
}

func Normalize(cfg config.Telegram, raw json.RawMessage, epoch string) (domain.Event, error) {
	var wire map[string]json.RawMessage
	if len(raw) == 0 || len(raw) > MaxUpdateBytes || epoch == "" || len(epoch) > 256 || json.Unmarshal(raw, &wire) != nil {
		return domain.Event{}, adapterError(domain.Permanent, "invalid_target")
	}
	var updateID int64
	if json.Unmarshal(wire["update_id"], &updateID) != nil || updateID < 0 {
		return domain.Event{}, adapterError(domain.Permanent, "invalid_target")
	}
	canonical, _ := json.Marshal(wire)
	sum := sha256.Sum256(canonical)
	e := domain.Event{Key: strconv.FormatInt(updateID, 10), Source: domain.Telegram, Kind: "telegram.unknown", Chat: "unresolved", Actor: "unresolved", OccurredAt: time.Unix(0, 0).UTC(), Raw: canonical, Version: domain.SourceVersion{Kind: "telegram.update", Tie: strconv.FormatInt(updateID, 10), Digest: hex.EncodeToString(sum[:]), StreamEpoch: epoch}}
	meta := Metadata{UpdateType: "unknown", Admission: "quarantined", Reason: "unsupported_update"}
	var update models.Update
	if json.Unmarshal(canonical, &update) != nil {
		meta.Reason = "invalid_payload"
		return finish(e, meta), nil
	}
	message := update.Message
	field := "message"
	if update.EditedMessage != nil {
		message, field = update.EditedMessage, "edited_message"
	}
	if message != nil {
		meta.UpdateType = field
		meta.ThreadID = int64(message.MessageThreadID)
		meta.MessageID = message.ID
		e.Chat = strconv.FormatInt(message.Chat.ID, 10)
		if message.From != nil && message.From.ID > 0 {
			e.Actor = strconv.FormatInt(message.From.ID, 10)
		}
		stamp := message.Date
		if field == "edited_message" {
			stamp = message.EditDate
		}
		if stamp >= 0 {
			e.Version.Order = int64(stamp)
			e.OccurredAt = time.Unix(int64(stamp), 0).UTC()
		}
		e.Version.Kind = "telegram.message"
		sum = sha256.Sum256(wire[field])
		e.Version.Digest = hex.EncodeToString(sum[:])
		meta.Reason = "unauthorized"
		if message.Chat.ID == cfg.ChatID && message.From != nil && message.From.ID > 0 && !message.From.IsBot && message.SenderChat == nil && allowed(cfg, message.From.ID) {
			meta.Admission, meta.Reason = "allowed", ""
			if message.ID > 0 && stamp > 0 {
				e.Target = &domain.MessageRef{Platform: domain.Telegram, Chat: e.Chat, Author: e.Actor, ID: strconv.Itoa(message.ID)}
				if message.Text != "" {
					e.Kind = domain.MessageCreate
					if field == "edited_message" {
						e.Kind = domain.MessageEdit
					}
					content := domain.TextContent{Text: message.Text}
					for _, entity := range message.Entities {
						mention := ""
						if entity.User != nil {
							mention = strconv.FormatInt(entity.User.ID, 10)
						}
						content.Entities = append(content.Entities, domain.TextEntity{Kind: string(entity.Type), StartUTF16: entity.Offset, LengthUTF16: entity.Length, URL: entity.URL, Mention: mention})
					}
					if reply := message.ReplyToMessage; reply != nil && reply.From != nil && reply.From.ID > 0 && reply.ID > 0 && reply.Chat.ID == message.Chat.ID && reply.SenderChat == nil {
						content.ReplyTo = &domain.MessageRef{Platform: domain.Telegram, Chat: e.Chat, Author: strconv.FormatInt(reply.From.ID, 10), ID: strconv.Itoa(reply.ID)}
					}
					e.Text = &content
				}
			}
		}
		var native map[string]json.RawMessage
		_ = json.Unmarshal(wire[field], &native)
		meta.ForwardOrigin, meta.Reply, meta.Entities = native["forward_origin"], native["reply_to_message"], native["entities"]
	}
	result := finish(e, meta)
	if result.Validate() != nil {
		e.Kind, e.Target, e.Text = "telegram.invalid", nil, nil
		meta.Admission, meta.Reason = "quarantined", "invalid_payload"
		return finish(e, meta), nil
	}
	return result, nil
}

func allowed(cfg config.Telegram, id int64) bool {
	for _, allowedID := range cfg.AllowedUserIDs {
		if allowedID == id {
			return true
		}
	}
	return false
}

func finish(e domain.Event, meta Metadata) domain.Event {
	e.Metadata, _ = json.Marshal(meta)
	return e
}
