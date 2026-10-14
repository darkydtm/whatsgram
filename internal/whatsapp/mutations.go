package whatsapp

import (
	"context"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

type MessageMetadata struct {
	CreatedAt time.Time
	Text      bool
}

func (c *Client) mutationTarget(ctx context.Context, ref domain.MessageRef, id string, own bool) (types.JID, types.JID, error) {
	if ref.Validate() != nil || ref.Platform != domain.WhatsApp || !validID(id) {
		return types.EmptyJID, types.EmptyJID, adapterError(domain.Permanent, "invalid_target")
	}
	chat, err := parseChat(ref.Chat)
	if err != nil {
		return chat, types.EmptyJID, err
	}
	author, err := parseChat(ref.Author)
	if err != nil || author.Server == types.GroupServer {
		return chat, types.EmptyJID, adapterError(domain.Permanent, "invalid_target")
	}
	if own {
		canonical, err := c.canonical(ctx, author)
		if err != nil {
			return chat, author, err
		}
		account, err := c.canonical(ctx, c.device.GetJID())
		if err != nil {
			return chat, author, err
		}
		if canonical.IsEmpty() || canonical != account {
			return chat, author, adapterError(domain.Permission, "permission_denied")
		}
	}
	return chat, author, nil
}

func (c *Client) window(ctx context.Context, ref domain.MessageRef, duration time.Duration, text bool) error {
	if c.options.LookupMessage == nil {
		return adapterError(domain.Permanent, "invalid_target")
	}
	metadata, err := c.options.LookupMessage(ctx, ref)
	if err != nil {
		return adapterError(domain.Transient, "storage")
	}
	age := c.options.Now().Sub(metadata.CreatedAt)
	if metadata.CreatedAt.IsZero() || age < 0 || age >= duration || (text && !metadata.Text) {
		return adapterError(domain.Permanent, "invalid_target")
	}
	return nil
}

func (c *Client) operationTime(ctx context.Context, id string) (time.Time, error) {
	if c.options.OperationTime == nil {
		return time.Time{}, adapterError(domain.Permanent, "invalid_target")
	}
	t, err := c.options.OperationTime(ctx, id)
	if err != nil {
		return time.Time{}, adapterError(domain.Transient, "storage")
	}
	if t.IsZero() || t.UnixMilli() <= 0 || t.After(c.options.Now()) {
		return time.Time{}, adapterError(domain.Permanent, "invalid_target")
	}
	return t, nil
}

func (c *Client) EditOwnText(ctx context.Context, ref domain.MessageRef, content domain.TextContent, id string) error {
	chat, _, err := c.mutationTarget(ctx, ref, id, true)
	if err != nil {
		return err
	}
	if err := c.window(ctx, ref, 15*time.Minute, true); err != nil {
		return err
	}
	message, err := buildText(chat, content)
	if err != nil {
		return err
	}
	when, err := c.operationTime(ctx, id)
	if err != nil {
		return err
	}
	edit := c.client.BuildEdit(chat, ref.ID, message)
	edit.GetEditedMessage().GetMessage().GetProtocolMessage().TimestampMS = proto.Int64(when.UnixMilli())
	return c.sendMutation(ctx, chat, edit, id)
}

func (c *Client) Revoke(ctx context.Context, ref domain.MessageRef, id string) error {
	chat, author, err := c.mutationTarget(ctx, ref, id, true)
	if err != nil {
		return err
	}
	if err := c.window(ctx, ref, 48*time.Hour, false); err != nil {
		return err
	}
	return c.sendMutation(ctx, chat, c.client.BuildRevoke(chat, author, ref.ID), id)
}

func (c *Client) SetOwnReaction(ctx context.Context, ref domain.MessageRef, text, id string) error {
	chat, author, err := c.mutationTarget(ctx, ref, id, false)
	if err != nil {
		return err
	}
	if len(text) > 128 {
		return adapterError(domain.Permanent, "invalid_target")
	}
	when, err := c.operationTime(ctx, id)
	if err != nil {
		return err
	}
	reaction := c.client.BuildReaction(chat, author, ref.ID, text)
	reaction.GetReactionMessage().SenderTimestampMS = proto.Int64(when.UnixMilli())
	return c.sendMutation(ctx, chat, reaction, id)
}

func (c *Client) sendMutation(ctx context.Context, chat types.JID, message *waE2E.Message, id string) error {
	resp, err := c.send(ctx, chat, message, id)
	if err != nil {
		return err
	}
	if resp.ID != id {
		return adapterError(domain.Uncertain, "uncertain")
	}
	return nil
}
