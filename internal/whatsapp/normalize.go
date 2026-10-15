package whatsapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type rawEnvelope struct {
	ID           string `json:"id"`
	Chat         string `json:"chat"`
	Sender       string `json:"sender"`
	SenderAlt    string `json:"sender_alt,omitempty"`
	RecipientAlt string `json:"recipient_alt,omitempty"`
	FromMe       bool   `json:"from_me"`
	Timestamp    int64  `json:"timestamp"`
	Payload      []byte `json:"payload"`
}

type eventMetadata struct {
	Source       string `json:"source"`
	Partial      bool   `json:"partial,omitempty"`
	RawChat      string `json:"raw_chat,omitempty"`
	RawAuthor    string `json:"raw_author,omitempty"`
	SenderAlt    string `json:"sender_alt,omitempty"`
	RecipientAlt string `json:"recipient_alt,omitempty"`
	Reason       string `json:"reason,omitempty"`
	Title        string `json:"title,omitempty"`
	Timer        uint32 `json:"timer_seconds,omitempty"`
	TargetID     string `json:"unresolved_target_id,omitempty"`
}

func (c *Client) canonical(ctx context.Context, jid types.JID) (types.JID, error) {
	jid = jid.ToNonAD()
	if jid.Server == types.HiddenUserServer && c.device.LIDs != nil {
		pn, err := c.device.LIDs.GetPNForLID(privateContext(ctx), jid)
		if err != nil {
			return types.EmptyJID, adapterError(domain.Transient, "storage")
		}
		if !pn.IsEmpty() {
			return pn.ToNonAD(), nil
		}
	}
	return jid, nil
}

func (c *Client) NormalizeEvent(ctx context.Context, event any) ([]domain.Event, error) {
	switch e := event.(type) {
	case *events.Message:
		return c.normalizeMessage(ctx, e)
	case *events.HistorySync:
		return c.normalizeHistorySync(ctx, e)
	default:
		return nil, nil
	}
}

func (c *Client) normalizeHistorySync(ctx context.Context, e *events.HistorySync) ([]domain.Event, error) {
	if e == nil {
		return nil, nil
	}
	data := e.Data
	if data == nil && e.Notification != nil && c.network != nil {
		var err error
		data, err = c.network.DownloadHistorySync(ctx, e.Notification, false)
		if err != nil || data == nil {
			return nil, err
		}
	}
	if data == nil {
		return nil, nil
	}

	var allEvents []domain.Event
	for _, conv := range data.GetConversations() {
		if conv == nil {
			continue
		}
		chatJID, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		messages := conv.GetMessages()
		if len(messages) == 0 {
			continue
		}
		// Sort messages chronologically: oldest first, newer last
		sort.SliceStable(messages, func(i, j int) bool {
			mi := messages[i].GetMessage()
			mj := messages[j].GetMessage()
			if mi == nil || mj == nil {
				return mi != nil
			}
			ti := mi.GetMessageTimestamp()
			tj := mj.GetMessageTimestamp()
			if ti != tj {
				return ti < tj
			}
			return messages[i].GetMsgOrderID() < messages[j].GetMsgOrderID()
		})

		for _, hMsg := range messages {
			if hMsg == nil || hMsg.GetMessage() == nil || c.network == nil {
				continue
			}
			parsedMsg, err := c.network.ParseWebMessage(chatJID, hMsg.GetMessage())
			if err != nil || parsedMsg == nil {
				continue
			}
			evs, err := c.normalizeMessage(ctx, parsedMsg)
			if err != nil {
				continue
			}
			allEvents = append(allEvents, evs...)
		}
	}
	return allEvents, nil
}

func unwrapMessage(raw *waE2E.Message) (*waE2E.Message, bool) {
	message := raw
	for range 16 {
		switch {
		case message.GetDeviceSentMessage().GetMessage() != nil:
			message = message.GetDeviceSentMessage().GetMessage()
		case message.GetEphemeralMessage().GetMessage() != nil:
			message = message.GetEphemeralMessage().GetMessage()
		case message.GetEditedMessage().GetMessage() != nil:
			message = message.GetEditedMessage().GetMessage()
		case message.GetViewOnceMessage().GetMessage() != nil || message.GetViewOnceMessageV2().GetMessage() != nil || message.GetViewOnceMessageV2Extension().GetMessage() != nil:
			return message, true
		default:
			return message, false
		}
	}
	return nil, false
}

func textContent(message *waE2E.Message) *domain.TextContent {
	if message.Conversation != nil {
		return &domain.TextContent{Text: message.GetConversation()}
	}
	if message.ExtendedTextMessage != nil {
		return &domain.TextContent{Text: message.GetExtendedTextMessage().GetText()}
	}
	return nil
}

func (c *Client) normalizeMessage(ctx context.Context, message *events.Message) ([]domain.Event, error) {
	if message == nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	info := message.Info
	id := info.ID
	if message.SourceWebMsg != nil {
		id = message.SourceWebMsg.GetKey().GetID()
	}
	if !validID(id) || info.Chat.IsEmpty() || info.Sender.IsEmpty() {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	raw := message.RawMessage
	if raw == nil {
		raw = message.Message
	}
	if raw == nil || proto.Size(raw) > 700000 {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	payload, err := proto.MarshalOptions{Deterministic: true}.Marshal(raw)
	if err != nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	chat, err := c.canonical(ctx, info.Chat)
	if err != nil {
		return nil, err
	}
	author, err := c.canonical(ctx, info.Sender)
	if err != nil {
		return nil, err
	}
	privateRaw, err := json.Marshal(rawEnvelope{ID: id, Chat: info.Chat.String(), Sender: info.Sender.String(), SenderAlt: info.SenderAlt.String(), RecipientAlt: info.RecipientAlt.String(), FromMe: info.IsFromMe, Timestamp: info.Timestamp.UnixMilli(), Payload: payload})
	if err != nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	digest := sha256.Sum256(payload)
	ref := domain.MessageRef{Platform: domain.WhatsApp, Chat: chat.String(), Author: author.String(), ID: id, Device: info.Sender.String()}
	e := domain.Event{Key: chat.String() + "/" + author.String() + "/" + id, Source: domain.WhatsApp, Kind: domain.MessageCreate, Chat: chat.String(), Actor: author.String(), Target: &ref, OccurredAt: info.Timestamp, Raw: privateRaw, Version: domain.SourceVersion{Kind: "wa.message", Order: max(0, info.Timestamp.UnixMilli()), Tie: id, Digest: hex.EncodeToString(digest[:])}}
	metadata := eventMetadata{Source: "message", RawChat: info.Chat.String(), RawAuthor: info.Sender.String(), SenderAlt: info.SenderAlt.String(), RecipientAlt: info.RecipientAlt.String()}
	if message.SourceWebMsg != nil {
		metadata.Source, metadata.Partial = "history-message", true
	}
	body, viewOnce := unwrapMessage(raw)
	if viewOnce || message.IsViewOnce {
		e.Kind = "unsupported"
		metadata.Reason = "view-once"
		e.Raw, _ = json.Marshal(rawEnvelope{ID: id, Chat: info.Chat.String(), Sender: info.Sender.String(), Timestamp: info.Timestamp.UnixMilli()})
	} else if body == nil {
		e.Kind = "unsupported"
		metadata.Reason = "wrapper-depth"
	} else if p := body.GetProtocolMessage(); p != nil && p.GetType() == waE2E.ProtocolMessage_MESSAGE_EDIT {
		target, err := c.targetFromKey(ctx, chat, info.Sender, p.GetKey())
		if err != nil {
			return nil, err
		}
		e.Kind, e.Target = domain.MessageEdit, target
		e.Version.Kind, e.Version.Order = "wa.edit", max(0, p.GetTimestampMS())
		e.OccurredAt = time.UnixMilli(e.Version.Order)
		e.Text = textContent(p.GetEditedMessage())
		if target == nil || e.Text == nil {
			e.Kind, metadata.Reason = "unsupported", "edit-target-or-type"
			metadata.TargetID = p.GetKey().GetID()
		}
	} else if p := body.GetProtocolMessage(); p != nil && p.GetType() == waE2E.ProtocolMessage_REVOKE {
		target, err := c.targetFromKey(ctx, chat, info.Sender, p.GetKey())
		if err != nil {
			return nil, err
		}
		e.Kind, e.Target = domain.MessageRevoke, target
		e.Version.Kind, e.Version.Order = "wa.revoke", max(0, p.GetTimestampMS())
		if p.GetTimestampMS() > 0 {
			e.OccurredAt = time.UnixMilli(p.GetTimestampMS())
		}
		if target == nil {
			e.Kind, metadata.Reason = "unsupported", "revoke-target"
			metadata.TargetID = p.GetKey().GetID()
		}
	} else if reaction := body.GetReactionMessage(); reaction != nil {
		target, err := c.targetFromKey(ctx, chat, info.Sender, reaction.GetKey())
		if err != nil {
			return nil, err
		}
		e.Kind, e.Target = domain.ReactionSet, target
		e.Version.Kind, e.Version.Order = "wa.reaction", max(0, reaction.GetSenderTimestampMS())
		e.OccurredAt = time.UnixMilli(e.Version.Order)
		e.Text = &domain.TextContent{Text: reaction.GetText()}
		if target == nil {
			e.Kind, metadata.Reason = "unsupported", "reaction-target"
			metadata.TargetID = reaction.GetKey().GetID()
		}
	} else if reaction := body.GetEncReactionMessage(); reaction != nil {
		target, err := c.targetFromKey(ctx, chat, info.Sender, reaction.GetTargetMessageKey())
		if err != nil {
			return nil, err
		}
		e.Kind, e.Target, metadata.Reason = "unsupported", target, "encrypted-reaction"
		metadata.TargetID = reaction.GetTargetMessageKey().GetID()
	} else {
		e.Text = textContent(body)
		if e.Text == nil {
			e.Kind, metadata.Reason = "unsupported", "message-type"
		}
	}
	e.Metadata, _ = json.Marshal(metadata)
	if e.Validate() != nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	return []domain.Event{e}, nil
}

func (c *Client) targetFromKey(ctx context.Context, chat, actor types.JID, key *waCommon.MessageKey) (*domain.MessageRef, error) {
	if key == nil || !validID(key.GetID()) {
		return nil, nil
	}
	if key.GetRemoteJID() != "" {
		remote, err := types.ParseJID(key.GetRemoteJID())
		if err != nil {
			return nil, adapterError(domain.Permanent, "invalid_target")
		}
		remote, err = c.canonical(ctx, remote)
		if err != nil {
			return nil, err
		}
		if remote != chat {
			return nil, adapterError(domain.Permanent, "invalid_target")
		}
	}
	var sender types.JID
	var err error
	switch {
	case key.GetParticipant() != "":
		sender, err = types.ParseJID(key.GetParticipant())
	case key.GetFromMe():
		sender = c.device.GetJID()
	case chat.Server == types.DefaultUserServer || chat.Server == types.HiddenUserServer:
		sender = chat
	default:
		return nil, nil
	}
	if err != nil || sender.IsEmpty() {
		return nil, nil
	}
	canonical, err := c.canonical(ctx, sender)
	if err != nil {
		return nil, err
	}
	return &domain.MessageRef{Platform: domain.WhatsApp, Chat: chat.String(), Author: canonical.String(), ID: key.GetID(), Device: sender.String()}, nil
}
