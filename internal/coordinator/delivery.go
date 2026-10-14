package coordinator

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/store"
)

func (c *Coordinator) RunDeliveryLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := c.StepDelivery(ctx)
		if err != nil {
			if errors.Is(err, store.ErrNoWork) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(c.options.PollInterval):
					continue
				}
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.options.PollInterval):
				continue
			}
		}
	}
}

func (c *Coordinator) StepDelivery(ctx context.Context) error {
	now := time.Now()
	delivery, err := c.store.ClaimDelivery(ctx, now, c.options.LeaseDuration)
	if err != nil {
		return err
	}

	beginErr := c.store.BeginSend(ctx, delivery.ID, delivery.LeaseToken, now)
	if beginErr != nil {
		if failErr := c.handleDeliveryError(ctx, delivery, beginErr, false, now); failErr != nil {
			return failErr
		}
		return beginErr
	}

	var sendResult domain.SendResult
	var sendErr error

	switch delivery.Platform {
	case domain.Telegram:
		sendResult, sendErr = c.sendTelegram(ctx, delivery)
	case domain.WhatsApp:
		sendResult, sendErr = c.sendWhatsApp(ctx, delivery)
	default:
		sendErr = domain.AdapterError{
			Class: domain.Permanent,
			Code:  "invalid_target",
		}
	}

	if sendErr != nil {
		if failErr := c.handleDeliveryError(ctx, delivery, sendErr, true, now); failErr != nil {
			return failErr
		}
		return sendErr
	}

	completion := domain.Completion{
		Result:         sendResult,
		AppliedVersion: delivery.ExpectedVersion,
	}

	if delivery.Platform == domain.WhatsApp && delivery.WhatsAppSendID != "" {
		completion.EchoKeys = []string{delivery.WhatsAppSendID}
	}

	if len(completion.Result.Parts) == 0 && delivery.Kind == "message.create" {
		completion.Result.Parts = c.buildFallbackParts(delivery)
	}

	if err := c.store.CompleteDelivery(ctx, delivery.ID, delivery.LeaseToken, completion, now); err != nil {
		return err
	}

	c.pendingCount.Add(-1)
	return nil
}

func (c *Coordinator) sendTelegram(ctx context.Context, delivery domain.Delivery) (domain.SendResult, error) {
	topic := domain.TopicRef{
		ChatID:     c.cfg.Telegram.ChatID,
		Generation: delivery.TopicGeneration,
	}

	if delivery.EchoActor != "" {
		chat, err := c.store.Chat(ctx, delivery.EchoActor)
		if err == nil && chat.Topic != nil {
			topic = *chat.Topic
		}
	}

	content := domain.TextContent{}
	if delivery.Text != nil {
		content = c.resolveReplyTo(ctx, domain.Telegram, *delivery.Text)
	}

	req := domain.SendTextRequest{
		OperationKey:  delivery.OperationKey,
		TelegramTopic: topic,
		Content:       content,
	}

	return c.tgPort.SendText(ctx, req)
}

func (c *Coordinator) sendWhatsApp(ctx context.Context, delivery domain.Delivery) (domain.SendResult, error) {
	content := domain.TextContent{}
	if delivery.Text != nil {
		content = c.resolveReplyTo(ctx, domain.WhatsApp, *delivery.Text)
	}

	req := domain.SendTextRequest{
		OperationKey:   delivery.OperationKey,
		WhatsAppSendID: delivery.WhatsAppSendID,
		WhatsAppChat:   delivery.EchoActor,
		Content:        content,
	}

	return c.waPort.SendText(ctx, req)
}

func (c *Coordinator) resolveReplyTo(ctx context.Context, targetPlatform domain.Platform, content domain.TextContent) domain.TextContent {
	if content.ReplyTo == nil {
		return content
	}

	if content.ReplyTo.Platform == targetPlatform {
		if part, err := c.store.Part(ctx, *content.ReplyTo); err == nil && part.Deleted {
			content.ReplyTo = nil
		}
		return content
	}

	parts, err := c.store.FindParts(ctx, *content.ReplyTo)
	if err == nil {
		for _, p := range parts {
			if p.Ref.Platform == targetPlatform && !p.Deleted {
				mapped := p.Ref
				content.ReplyTo = &mapped
				return content
			}
		}
	}

	content.ReplyTo = nil
	return content
}

func (c *Coordinator) buildFallbackParts(delivery domain.Delivery) []domain.DeliveredPart {
	remoteChat := delivery.EchoActor
	remoteAuthor := delivery.EchoActor
	remoteID := delivery.WhatsAppSendID
	if remoteID == "" {
		remoteID = strconv.FormatInt(delivery.ID, 10)
	}

	if delivery.Platform == domain.Telegram {
		remoteChat = strconv.FormatInt(c.cfg.Telegram.ChatID, 10)
		remoteAuthor = strconv.FormatInt(c.cfg.Telegram.ChatID, 10)
	}

	return []domain.DeliveredPart{
		{
			Ref: domain.MessageRef{
				Platform: delivery.Platform,
				Chat:     remoteChat,
				Author:   remoteAuthor,
				ID:       remoteID,
			},
			Index:           0,
			Role:            domain.RolePrimary,
			BridgeOwned:     true,
			Capabilities:    domain.CanDelete | domain.CanRevoke | domain.CanEditText,
			TopicGeneration: delivery.TopicGeneration,
		},
	}
}

func (c *Coordinator) handleDeliveryError(ctx context.Context, d domain.Delivery, sendErr error, sendStarted bool, now time.Time) error {
	var adapterErr domain.AdapterError
	if !errors.As(sendErr, &adapterErr) {
		adapterErr = domain.AdapterError{
			Class: domain.Permanent,
			Cause: sendErr,
		}
	}

	if !sendStarted {
		if adapterErr.Class == domain.Transient || adapterErr.Class == domain.RateLimited {
			delay := adapterErr.RetryAfter
			if delay <= 0 {
				delay = c.cfg.Runtime.RetryBase
			}
			if delay <= 0 {
				delay = time.Second
			}
			if c.cfg.Runtime.RetryMax > 0 && delay > c.cfg.Runtime.RetryMax {
				delay = c.cfg.Runtime.RetryMax
			}
			decision := domain.RecoveryDecision{
				State:       "pending",
				ErrorClass:  adapterErr.Class,
				AvailableAt: now.Add(delay),
			}
			return c.store.FailDelivery(ctx, d.ID, d.LeaseToken, decision, now)
		}

		state := "dead"
		if adapterErr.Class == domain.Permission {
			state = "blocked"
		}
		decision := domain.RecoveryDecision{
			State:      state,
			ErrorClass: adapterErr.Class,
		}
		return c.store.FailDelivery(ctx, d.ID, d.LeaseToken, decision, now)
	}

	if adapterErr.Class == domain.Transient || adapterErr.Class == domain.RateLimited || adapterErr.Class == domain.Uncertain || adapterErr.MayHaveApplied {
		decision := domain.RecoveryDecision{
			State:      "uncertain",
			ErrorClass: domain.Uncertain,
		}
		return c.store.FailDelivery(ctx, d.ID, d.LeaseToken, decision, now)
	}

	if adapterErr.Class == domain.Permission {
		decision := domain.RecoveryDecision{
			State:      "blocked",
			ErrorClass: domain.Permission,
		}
		return c.store.FailDelivery(ctx, d.ID, d.LeaseToken, decision, now)
	}

	decision := domain.RecoveryDecision{
		State:      "dead",
		ErrorClass: adapterErr.Class,
	}
	return c.store.FailDelivery(ctx, d.ID, d.LeaseToken, decision, now)
}
