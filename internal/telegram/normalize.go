package telegram

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
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
	FileID        string          `json:"file_id,omitempty"`
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
		e.Kind = "telegram.invalid"
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
		e.TelegramThreadID = int64(message.MessageThreadID)
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
		e.Kind = "telegram.rejected"
		if message.Chat.ID == cfg.ChatID && message.From != nil && message.From.ID > 0 && !message.From.IsBot && message.SenderChat == nil && allowed(cfg, message.From.ID) {
			meta.Admission, meta.Reason = "allowed", ""
			e.Kind = "telegram.unsupported"
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
				} else if len(message.Photo) > 0 {
					e.Kind = domain.MessageCreate
					if field == "edited_message" {
						e.Kind = domain.MessageEdit
					}
					content := domain.TextContent{Text: message.Caption}
					for _, entity := range message.CaptionEntities {
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
					meta.UpdateType = "photo"
					meta.FileID = message.Photo[len(message.Photo)-1].FileID
				}
			}
		}
		var native map[string]json.RawMessage
		_ = json.Unmarshal(wire[field], &native)
		meta.ForwardOrigin, meta.Reply, meta.Entities = native["forward_origin"], native["reply_to_message"], native["entities"]
		if meta.Admission == "allowed" && commandLike(message) {
			e.Kind, e.Text = "telegram.command", nil
		}
	} else {
		e, meta = normalizeState(cfg, update, e, meta)
	}
	result := finish(e, meta)
	if result.Validate() != nil {
		e.Kind, e.Target, e.Text = "telegram.invalid", nil, nil
		meta.Admission, meta.Reason = "quarantined", "invalid_payload"
		return finish(e, meta), nil
	}
	return result, nil
}

func commandLike(message *models.Message) bool {
	if strings.HasPrefix(strings.TrimSpace(message.Text), "/") || strings.HasPrefix(strings.TrimSpace(message.Caption), "/") {
		return true
	}
	for _, entities := range [][]models.MessageEntity{message.Entities, message.CaptionEntities} {
		for _, entity := range entities {
			if entity.Type == models.MessageEntityTypeBotCommand {
				return true
			}
		}
	}
	return false
}

func normalizeState(cfg config.Telegram, update models.Update, e domain.Event, meta Metadata) (domain.Event, Metadata) {
	stamp := 0
	switch {
	case update.MessageReaction != nil:
		r := update.MessageReaction
		e.Kind, meta.UpdateType, meta.Reason = "telegram.reaction", "message_reaction", "mapping_required"
		e.Chat, meta.MessageID, stamp = strconv.FormatInt(r.Chat.ID, 10), r.MessageID, r.Date
		if r.User != nil && r.User.ID > 0 {
			e.Actor = strconv.FormatInt(r.User.ID, 10)
		}
		if r.Chat.ID != cfg.ChatID || r.User == nil || r.User.IsBot || r.ActorChat != nil || !allowed(cfg, r.User.ID) {
			e.Kind, meta.Reason = "telegram.rejected", "unauthorized"
		} else if r.User.ID != driver(cfg) {
			e.Kind, meta.Reason = "telegram.rejected", "account_driver_only"
		}
	case update.MessageReactionCount != nil:
		r := update.MessageReactionCount
		e.Kind, meta.UpdateType, meta.Reason = "telegram.reaction_count", "message_reaction_count", "aggregate_no_actor"
		e.Chat, meta.MessageID, stamp = strconv.FormatInt(r.Chat.ID, 10), r.MessageID, r.Date
		if r.Chat.ID != cfg.ChatID {
			e.Kind, meta.Reason = "telegram.rejected", "unauthorized"
		}
	case update.PollAnswer != nil:
		p := update.PollAnswer
		e.Kind, meta.UpdateType, meta.Reason, meta.PollID = "telegram.poll_answer", "poll_answer", "poll_scope_required", p.PollID
		if p.User != nil && p.User.ID > 0 {
			e.Actor = strconv.FormatInt(p.User.ID, 10)
		}
		if p.User == nil || p.User.IsBot || p.VoterChat != nil || !allowed(cfg, p.User.ID) {
			e.Kind, meta.Reason = "telegram.rejected", "unauthorized"
		} else if p.User.ID != driver(cfg) {
			e.Kind, meta.Reason = "telegram.rejected", "account_driver_only"
		}
	case update.Poll != nil:
		e.Kind, meta.UpdateType, meta.Reason, meta.PollID = "telegram.poll", "poll", "poll_scope_required", update.Poll.ID
	case update.MyChatMember != nil || update.ChatMember != nil:
		member := update.MyChatMember
		meta.UpdateType = "my_chat_member"
		if member == nil {
			member = update.ChatMember
			meta.UpdateType = "chat_member"
		}
		e.Kind, meta.Reason = "telegram.membership", "state_observation"
		e.Chat, e.Actor, stamp = strconv.FormatInt(member.Chat.ID, 10), strconv.FormatInt(member.From.ID, 10), member.Date
		if member.Chat.ID != cfg.ChatID {
			e.Kind, meta.Reason = "telegram.rejected", "unauthorized"
		}
	}
	if stamp > 0 {
		e.OccurredAt = time.Unix(int64(stamp), 0).UTC()
		e.Version.Order = int64(stamp)
	}
	return e, meta
}

func driver(cfg config.Telegram) int64 {
	var result int64
	for _, id := range cfg.AllowedUserIDs {
		if id > 0 && (result == 0 || id < result) {
			result = id
		}
	}
	return result
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
