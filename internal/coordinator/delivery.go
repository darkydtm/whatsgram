package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/media"
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

	switch delivery.Kind {
	case "message.create":
		content := domain.TextContent{}
		if delivery.Text != nil {
			content = c.resolveReplyTo(ctx, domain.Telegram, *delivery.Text)
		}

		isPhoto := strings.Contains(delivery.OperationKey, ":photo")
		if isPhoto && len(delivery.RawPayload) > 0 {
			photoData, err := c.waPort.DownloadMedia(ctx, delivery.RawPayload)
			if err == nil && len(photoData) > 0 {
				req := domain.SendPhotoRequest{
					OperationKey:  delivery.OperationKey,
					TelegramTopic: topic,
					Caption:       content.Text,
					Entities:      content.Entities,
					FileName:      "photo.jpg",
					Data:          photoData,
				}
				return c.tgPort.SendPhoto(ctx, req)
			}
		}

		isCaption := strings.Contains(delivery.OperationKey, "caption")
		limit := media.TelegramTextLimit
		if isCaption {
			limit = media.TelegramCaptionLimit
		}

		runes := []rune(content.Text)
		if len(runes) > limit {
			var chunks []string
			var roles []string

			if isCaption {
				p, o := media.SplitCaption(content.Text, media.TelegramCaptionLimit)
				chunks = append(chunks, p)
				roles = append(roles, domain.RolePrimary)
				if o != "" {
					overflows := media.SplitText(o, media.TelegramTextLimit)
					for _, ov := range overflows {
						if ov != "" {
							chunks = append(chunks, ov)
							roles = append(roles, domain.RoleCaptionOverflow)
						}
					}
				}
			} else {
				chunks = media.SplitText(content.Text, media.TelegramTextLimit)
				for i := range chunks {
					if i == 0 {
						roles = append(roles, domain.RolePrimary)
					} else {
						roles = append(roles, domain.RoleText)
					}
				}
			}

			var allParts []domain.DeliveredPart
			for i, chunk := range chunks {
				req := domain.SendTextRequest{
					OperationKey:  fmt.Sprintf("%s:part:%d", delivery.OperationKey, i),
					TelegramTopic: topic,
					Content: domain.TextContent{
						Text: chunk,
					},
				}
				if i == 0 {
					req.Content.ReplyTo = content.ReplyTo
					req.Content.Entities = content.Entities
					req.Content.ForwardLabel = content.ForwardLabel
					req.Content.MentionTargets = content.MentionTargets
				}
				res, err := c.tgPort.SendText(ctx, req)
				if err != nil {
					return domain.SendResult{}, err
				}
				for _, p := range res.Parts {
					p.Index = i
					p.Role = roles[i]
					allParts = append(allParts, p)
				}
			}
			return domain.SendResult{
				Parts:      allParts,
				AcceptedAt: time.Now().UTC(),
			}, nil
		}

		req := domain.SendTextRequest{
			OperationKey:  delivery.OperationKey,
			TelegramTopic: topic,
			Content:       content,
		}

		res, err := c.tgPort.SendText(ctx, req)
		if err != nil {
			return domain.SendResult{}, err
		}
		if strings.Contains(delivery.OperationKey, "fallback") && len(res.Parts) > 0 {
			res.Parts[0].Role = domain.RoleFallback
		}
		return res, nil

	case "message.edit":
		if delivery.Target == nil {
			return domain.SendResult{}, domain.AdapterError{Class: domain.Permanent, Code: "invalid_target"}
		}
		content := domain.TextContent{}
		if delivery.Text != nil {
			content = c.resolveReplyTo(ctx, domain.Telegram, *delivery.Text)
		}

		targetRef := *delivery.Target
		isCaption := strings.Contains(delivery.OperationKey, "caption")
		parts, err := c.store.FindParts(ctx, *delivery.Target)
		if err == nil {
			for _, p := range parts {
				if p.Ref.Platform == domain.Telegram && !p.Deleted {
					if isCaption && (p.Role == domain.RoleCaptionOverflow || p.Capabilities&domain.CanEditCaption != 0) {
						targetRef = p.Ref
						break
					} else if !isCaption && (p.Role == domain.RolePrimary || p.Index == 0) {
						targetRef = p.Ref
						break
					}
				}
			}
		}

		part, err := c.store.Part(ctx, targetRef)
		canEdit := err == nil && part.BridgeOwned && (part.Capabilities&(domain.CanEditText|domain.CanEditCaption) != 0)
		if canEdit {
			editErr := c.tgPort.EditText(ctx, targetRef, content)
			if editErr == nil {
				return domain.SendResult{AcceptedAt: time.Now().UTC()}, nil
			}
			if errors.Is(editErr, context.Canceled) || errors.Is(editErr, context.DeadlineExceeded) {
				return domain.SendResult{}, editErr
			}
			var adapterErr domain.AdapterError
			if errors.As(editErr, &adapterErr) && (adapterErr.Class == domain.Transient || adapterErr.Class == domain.RateLimited) {
				return domain.SendResult{}, editErr
			}
		}

		// Fallback notice when Telegram representation cannot be edited
		noticeText := fmt.Sprintf("✏️ Message edited: %s", content.Text)
		req := domain.SendTextRequest{
			OperationKey:  delivery.OperationKey,
			TelegramTopic: topic,
			Content: domain.TextContent{
				Text:    noticeText,
				ReplyTo: &targetRef,
			},
		}
		res, sendErr := c.tgPort.SendText(ctx, req)
		if sendErr != nil {
			return domain.SendResult{}, sendErr
		}
		if len(res.Parts) > 0 {
			existingParts, _ := c.store.FindParts(ctx, targetRef)
			res.Parts[0].Index = len(existingParts)
			res.Parts[0].Role = domain.RoleRevisionNotice
		}
		return res, nil

	case "message.revoke":
		if delivery.Target == nil {
			return domain.SendResult{}, domain.AdapterError{Class: domain.Permanent, Code: "invalid_target"}
		}
		parts, err := c.store.FindParts(ctx, *delivery.Target)
		var tgParts []domain.DeliveredPart
		if err == nil {
			for _, p := range parts {
				if p.Ref.Platform == domain.Telegram {
					tgParts = append(tgParts, p)
				}
			}
		}
		if len(tgParts) == 0 {
			tgParts = []domain.DeliveredPart{{Ref: *delivery.Target}}
		}

		var deletedParts []domain.DeliveredPart
		var lastErr error
		for _, p := range tgParts {
			delErr := c.tgPort.Delete(ctx, p.Ref)
			if delErr != nil {
				lastErr = delErr
			} else {
				p.Deleted = true
				deletedParts = append(deletedParts, p)
			}
		}

		if len(deletedParts) > 0 && lastErr == nil {
			return domain.SendResult{AcceptedAt: time.Now().UTC()}, nil
		}
		if lastErr != nil {
			if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
				return domain.SendResult{}, lastErr
			}
			var adapterErr domain.AdapterError
			if errors.As(lastErr, &adapterErr) && (adapterErr.Class == domain.Transient || adapterErr.Class == domain.RateLimited) {
				return domain.SendResult{}, lastErr
			}
		}

		// If deletion fails (too old or rights lost): post notification "🗑 Message revoked by sender"
		req := domain.SendTextRequest{
			OperationKey:  delivery.OperationKey,
			TelegramTopic: topic,
			Content: domain.TextContent{
				Text:    "🗑 Message revoked by sender",
				ReplyTo: delivery.Target,
			},
		}
		res, sendErr := c.tgPort.SendText(ctx, req)
		if sendErr != nil {
			if lastErr != nil {
				return domain.SendResult{}, lastErr
			}
			return domain.SendResult{}, sendErr
		}
		if len(res.Parts) > 0 {
			existingParts, _ := c.store.FindParts(ctx, *delivery.Target)
			res.Parts[0].Index = len(existingParts)
			res.Parts[0].Role = domain.RoleFallback
		}
		return res, nil

	case "reaction.set":
		if delivery.Target == nil {
			return domain.SendResult{}, domain.AdapterError{Class: domain.Permanent, Code: "invalid_target"}
		}
		emoji := ""
		if delivery.Text != nil {
			emoji = delivery.Text.Text
		}
		if err := c.tgPort.SetOwnReaction(ctx, *delivery.Target, emoji); err != nil {
			return domain.SendResult{}, err
		}
		return domain.SendResult{AcceptedAt: time.Now().UTC()}, nil

	default:
		return domain.SendResult{}, domain.AdapterError{Class: domain.Permanent, Code: "invalid_target"}
	}
}

func (c *Coordinator) sendWhatsApp(ctx context.Context, delivery domain.Delivery) (domain.SendResult, error) {
	switch delivery.Kind {
	case "message.create":
		content := domain.TextContent{}
		if delivery.Text != nil {
			content = c.resolveReplyTo(ctx, domain.WhatsApp, *delivery.Text)
		}

		isPhoto := strings.Contains(delivery.OperationKey, ":photo")
		if isPhoto && len(delivery.RawPayload) > 0 {
			var meta struct {
				FileID string `json:"file_id"`
			}
			if json.Unmarshal(delivery.RawPayload, &meta) == nil && meta.FileID != "" {
				fileBytes, err := c.tgPort.DownloadFile(ctx, meta.FileID)
				if err == nil && len(fileBytes) > 0 {
					sendID := delivery.WhatsAppSendID
					if sendID == "" {
						sendID = c.waPort.AllocateSendID()
					}
					c.recordBridgeSend(sendID)
					req := domain.SendPhotoRequest{
						OperationKey:   delivery.OperationKey,
						WhatsAppChat:   delivery.EchoActor,
						WhatsAppSendID: sendID,
						Caption:        content.Text,
						Data:           fileBytes,
					}
					return c.waPort.SendPhoto(ctx, req)
				}
			}
		}

		runes := []rune(content.Text)
		if len(runes) > media.WhatsAppTextLimit {
			chunks := media.SplitText(content.Text, media.WhatsAppTextLimit)
			var allParts []domain.DeliveredPart
			for i, chunk := range chunks {
				sendID := c.waPort.AllocateSendID()
				c.recordBridgeSend(sendID)
				req := domain.SendTextRequest{
					OperationKey:   fmt.Sprintf("%s:part:%d", delivery.OperationKey, i),
					WhatsAppSendID: sendID,
					WhatsAppChat:   delivery.EchoActor,
					Content: domain.TextContent{
						Text: chunk,
					},
				}
				if i == 0 {
					req.Content.ReplyTo = content.ReplyTo
				}
				res, err := c.waPort.SendText(ctx, req)
				if err != nil {
					return domain.SendResult{}, err
				}
				for _, p := range res.Parts {
					p.Index = i
					if i == 0 {
						p.Role = domain.RolePrimary
					} else {
						p.Role = domain.RoleText
					}
					allParts = append(allParts, p)
				}
			}
			return domain.SendResult{
				Parts:      allParts,
				AcceptedAt: time.Now().UTC(),
			}, nil
		}

		req := domain.SendTextRequest{
			OperationKey:   delivery.OperationKey,
			WhatsAppSendID: delivery.WhatsAppSendID,
			WhatsAppChat:   delivery.EchoActor,
			Content:        content,
		}

		return c.waPort.SendText(ctx, req)

	case "message.edit":
		if delivery.Target == nil {
			return domain.SendResult{}, domain.AdapterError{Class: domain.Permanent, Code: "invalid_target"}
		}
		content := domain.TextContent{}
		if delivery.Text != nil {
			content = c.resolveReplyTo(ctx, domain.WhatsApp, *delivery.Text)
		}

		targetRef := *delivery.Target
		isCaption := strings.Contains(delivery.OperationKey, "caption")
		parts, err := c.store.FindParts(ctx, *delivery.Target)
		if err == nil {
			for _, p := range parts {
				if p.Ref.Platform == domain.WhatsApp && !p.Deleted {
					if isCaption && (p.Role == domain.RoleCaptionOverflow || p.Capabilities&domain.CanEditCaption != 0) {
						targetRef = p.Ref
						break
					} else if !isCaption && (p.Role == domain.RolePrimary || p.Index == 0) {
						targetRef = p.Ref
						break
					}
				}
			}
		}

		err = c.waPort.EditOwnText(ctx, targetRef, content, delivery.WhatsAppSendID)
		if err != nil {
			return domain.SendResult{}, err
		}
		return domain.SendResult{AcceptedAt: time.Now().UTC()}, nil

	case "message.revoke":
		if delivery.Target == nil {
			return domain.SendResult{}, domain.AdapterError{Class: domain.Permanent, Code: "invalid_target"}
		}
		parts, err := c.store.FindParts(ctx, *delivery.Target)
		var waParts []domain.DeliveredPart
		if err == nil {
			for _, p := range parts {
				if p.Ref.Platform == domain.WhatsApp {
					waParts = append(waParts, p)
				}
			}
		}
		if len(waParts) == 0 {
			waParts = []domain.DeliveredPart{{Ref: *delivery.Target}}
		}

		var deletedParts []domain.DeliveredPart
		for _, p := range waParts {
			sendID := delivery.WhatsAppSendID
			if len(waParts) > 1 {
				sendID = c.waPort.AllocateSendID()
				c.recordBridgeSend(sendID)
			}
			err := c.waPort.Revoke(ctx, p.Ref, sendID)
			if err != nil {
				return domain.SendResult{}, err
			}
			p.Deleted = true
			deletedParts = append(deletedParts, p)
		}
		return domain.SendResult{AcceptedAt: time.Now().UTC()}, nil

	case "reaction.set":
		if delivery.Target == nil {
			return domain.SendResult{}, domain.AdapterError{Class: domain.Permanent, Code: "invalid_target"}
		}
		emoji := ""
		if delivery.Text != nil {
			emoji = delivery.Text.Text
		}
		err := c.waPort.SetOwnReaction(ctx, *delivery.Target, emoji, delivery.WhatsAppSendID)
		if err != nil {
			return domain.SendResult{}, err
		}
		return domain.SendResult{AcceptedAt: time.Now().UTC()}, nil

	default:
		return domain.SendResult{}, domain.AdapterError{Class: domain.Permanent, Code: "invalid_target"}
	}
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
		var fallback *domain.MessageRef
		for _, p := range parts {
			if p.Ref.Platform == targetPlatform && !p.Deleted {
				if p.Index == 0 || p.Role == domain.RolePrimary {
					mapped := p.Ref
					content.ReplyTo = &mapped
					return content
				}
				if fallback == nil {
					ref := p.Ref
					fallback = &ref
				}
			}
		}
		if fallback != nil {
			content.ReplyTo = fallback
			return content
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
