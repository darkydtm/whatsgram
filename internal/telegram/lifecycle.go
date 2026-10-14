package telegram

import (
	"context"
	"strconv"
	"unicode/utf8"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type PartStore interface {
	Part(context.Context, domain.MessageRef) (domain.DeliveredPart, error)
}

func (c *Client) BindParts(parts PartStore) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.parts = parts
}

func (c *Client) mapped(ctx context.Context, ref domain.MessageRef, capability uint64) (int64, int, error) {
	if _, err := c.identity(); err != nil {
		return 0, 0, err
	}
	chat, message, err := c.target(ref)
	if err != nil {
		return 0, 0, err
	}
	c.mu.RLock()
	registry := c.parts
	botID := c.botID
	c.mu.RUnlock()
	if registry == nil {
		return 0, 0, adapterError(domain.Permission, "permission_denied")
	}
	part, err := registry.Part(ctx, ref)
	if err != nil {
		return 0, 0, adapterError(domain.Permanent, "invalid_target")
	}
	if part.Validate() != nil || part.Ref != ref || part.Deleted || part.Retired || part.Capabilities&capability == 0 || (capability == domain.CanEditText && (!part.BridgeOwned || ref.Author != strconv.FormatInt(botID, 10))) {
		return 0, 0, adapterError(domain.Permission, "permission_denied")
	}
	if err := c.Initialize(ctx); err != nil {
		return 0, 0, err
	}
	return chat, message, nil
}

func (c *Client) EditText(ctx context.Context, ref domain.MessageRef, content domain.TextContent) error {
	if content.Validate() != nil || utf8.RuneCountInString(content.Text) == 0 || utf8.RuneCountInString(content.Text) > 4096 {
		return adapterError(domain.Permanent, "invalid_target")
	}
	chat, message, err := c.mapped(ctx, ref, domain.CanEditText)
	if err != nil {
		return err
	}
	_, err = c.api.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: chat, MessageID: message, Text: content.Text})
	return classify(ctx, err, true)
}

func (c *Client) Delete(ctx context.Context, ref domain.MessageRef) error {
	chat, message, err := c.mapped(ctx, ref, domain.CanDelete)
	if err != nil {
		return err
	}
	ok, err := c.api.DeleteMessage(ctx, &bot.DeleteMessageParams{ChatID: chat, MessageID: message})
	if err == nil && !ok {
		return adapterError(domain.Uncertain, "uncertain")
	}
	return classify(ctx, err, true)
}

func (c *Client) SetOwnReaction(ctx context.Context, ref domain.MessageRef, emoji string) error {
	if len(emoji) > 64 {
		return adapterError(domain.Permanent, "invalid_target")
	}
	chat, message, err := c.mapped(ctx, ref, domain.CanReact)
	if err != nil {
		return err
	}
	reaction := make([]models.ReactionType, 0, 1)
	if emoji != "" {
		reaction = append(reaction, models.ReactionType{Type: models.ReactionTypeTypeEmoji, ReactionTypeEmoji: &models.ReactionTypeEmoji{Emoji: emoji}})
	}
	ok, err := c.api.SetMessageReaction(ctx, &bot.SetMessageReactionParams{ChatID: chat, MessageID: message, Reaction: reaction, IsBig: bot.False()})
	if err == nil && !ok {
		return adapterError(domain.Uncertain, "uncertain")
	}
	return classify(ctx, err, true)
}

func validName(name string) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) > 0 && utf8.RuneCountInString(name) <= 128
}

func (c *Client) CreateTopic(ctx context.Context, chatID int64, name string) (domain.TopicRef, error) {
	if chatID != c.cfg.ChatID || !validName(name) {
		return domain.TopicRef{}, adapterError(domain.Permanent, "invalid_target")
	}
	if _, err := c.identity(); err != nil {
		return domain.TopicRef{}, err
	}
	if err := c.Initialize(ctx); err != nil {
		return domain.TopicRef{}, err
	}
	topic, err := c.api.CreateForumTopic(ctx, &bot.CreateForumTopicParams{ChatID: chatID, Name: name})
	if err != nil {
		return domain.TopicRef{}, classify(ctx, err, true)
	}
	if topic == nil || topic.MessageThreadID <= 0 {
		return domain.TopicRef{}, adapterError(domain.Uncertain, "uncertain")
	}
	return domain.TopicRef{ChatID: chatID, ThreadID: int64(topic.MessageThreadID)}, nil
}

func (c *Client) RenameTopic(ctx context.Context, topic domain.TopicRef, name string) error {
	if topic.ChatID != c.cfg.ChatID || topic.ThreadID <= 0 || topic.Generation < 0 || !validName(name) {
		return adapterError(domain.Permanent, "invalid_target")
	}
	if _, err := c.identity(); err != nil {
		return err
	}
	if err := c.Initialize(ctx); err != nil {
		return err
	}
	ok, err := c.api.EditForumTopic(ctx, &bot.EditForumTopicParams{ChatID: topic.ChatID, MessageThreadID: int(topic.ThreadID), Name: name})
	if err == nil && !ok {
		return adapterError(domain.Uncertain, "uncertain")
	}
	return classify(ctx, err, true)
}
