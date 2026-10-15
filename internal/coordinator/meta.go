package coordinator

import (
	"context"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/store"
)

func (c *Coordinator) EnsureTopic(ctx context.Context, waJID string, titleHint string) (domain.TopicRef, error) {
	if waJID == "" {
		return domain.TopicRef{}, domain.ErrInvalid
	}

	existingChat, err := c.store.Chat(ctx, waJID)
	if err == nil && existingChat.Topic != nil && existingChat.State == store.ChatActive {
		return *existingChat.Topic, nil
	}

	chatID := c.cfg.Telegram.ChatID
	isGroup := strings.HasSuffix(waJID, "@g.us")
	source := "contact"
	title := titleHint

	var contactMeta domain.ContactMeta
	var groupMeta domain.GroupMeta

	if isGroup {
		source = "group"
		if title == "" {
			groupMeta, _ = c.waPort.GetGroupMeta(ctx, waJID)
			title = groupMeta.Name
		}
	} else {
		contactMeta, _ = c.waPort.GetContactMeta(ctx, waJID)
		if contactMeta.Name != "" {
			title = contactMeta.Name
		} else if title != "" && title != waJID && !strings.Contains(title, "@") {
			// Title already provided as non-JID hint
		} else if contactMeta.Phone != "" {
			title = contactMeta.Phone
		}
	}

	if title == "" {
		title = waJID
	}

	_ = c.store.DiscoverChat(ctx, domain.ChatCandidate{
		WhatsAppJID:    waJID,
		Title:          title,
		Source:         source,
		HistoryPartial: false,
	}, chatID)

	topicRef, err := c.tgPort.CreateTopic(ctx, chatID, title)
	if err != nil {
		return domain.TopicRef{}, err
	}

	gen := int64(1)
	if ch, err := c.store.Chat(ctx, waJID); err == nil {
		gen = ch.Generation + 1
	}
	topicRef.Generation = gen

	if err := c.store.BindTopic(ctx, waJID, topicRef); err != nil {
		return domain.TopicRef{}, err
	}
	c.activeChatCount.Add(1)

	var metaContent domain.TextContent
	if isGroup {
		metaContent = c.buildGroupMetaCard(ctx, waJID, groupMeta)
	} else {
		metaContent = c.buildContactMetaCard(ctx, waJID, contactMeta)
	}

	avatarBytes, _ := c.waPort.GetAvatar(ctx, waJID)
	sent := false
	if len(avatarBytes) > 0 && utf8.RuneCountInString(metaContent.Text) <= 1024 {
		_, err := c.tgPort.SendPhoto(ctx, domain.SendPhotoRequest{
			TelegramTopic: topicRef,
			Caption:       metaContent.Text,
			Entities:      metaContent.Entities,
			FileName:      "avatar.jpg",
			Data:          avatarBytes,
		})
		if err == nil {
			sent = true
		}
	}
	if !sent {
		_, _ = c.tgPort.SendText(ctx, domain.SendTextRequest{
			TelegramTopic: topicRef,
			Content:       metaContent,
		})
	}

	return topicRef, nil
}

func (c *Coordinator) buildContactMetaCard(ctx context.Context, waJID string, meta domain.ContactMeta) domain.TextContent {
	if meta.Name == "" || meta.Phone == "" || meta.JID == "" {
		m, err := c.waPort.GetContactMeta(ctx, waJID)
		if err == nil {
			if meta.JID == "" {
				meta.JID = m.JID
			}
			if meta.Name == "" {
				meta.Name = m.Name
			}
			if meta.Phone == "" {
				meta.Phone = m.Phone
			}
		}
	}
	if meta.JID == "" {
		meta.JID = waJID
	}
	name := meta.Name
	if name == "" {
		name = "не указано"
	}
	phone := meta.Phone
	if phone == "" {
		phone = "не указан"
	}

	commonGroups, _ := c.waPort.GetCommonGroups(ctx, waJID)

	var sb strings.Builder
	sb.WriteString("👤 Справка контакта:\n")
	sb.WriteString("ID: " + meta.JID + "\n")
	sb.WriteString("Номер: " + phone + "\n")
	sb.WriteString("Имя: " + name + "\n")
	sb.WriteString("Общие группы:")

	var entities []domain.TextEntity

	if len(commonGroups) == 0 {
		sb.WriteString(" нет")
	} else {
		for _, g := range commonGroups {
			sb.WriteString("\n• ")
			start := len(utf16.Encode([]rune(sb.String())))
			gName := g.Name
			if gName == "" {
				gName = g.JID
			}
			sb.WriteString(gName)
			length := len(utf16.Encode([]rune(gName)))

			chat, err := c.store.Chat(ctx, g.JID)
			if err == nil && chat.Topic != nil {
				entities = append(entities, domain.TextEntity{
					Kind:        "text_link",
					StartUTF16:  start,
					LengthUTF16: length,
					URL:         chat.Topic.URL(),
				})
			}
		}
	}

	return domain.TextContent{
		Text:     sb.String(),
		Entities: entities,
	}
}

func (c *Coordinator) buildGroupMetaCard(ctx context.Context, waJID string, meta domain.GroupMeta) domain.TextContent {
	if meta.Name == "" || len(meta.Participants) == 0 || meta.JID == "" {
		m, err := c.waPort.GetGroupMeta(ctx, waJID)
		if err == nil {
			if meta.JID == "" {
				meta.JID = m.JID
			}
			if meta.Name == "" {
				meta.Name = m.Name
			}
			if len(meta.Participants) == 0 {
				meta.Participants = m.Participants
			}
		}
	}
	if meta.JID == "" {
		meta.JID = waJID
	}
	name := meta.Name
	if name == "" {
		name = waJID
	}

	var sb strings.Builder
	sb.WriteString("👥 Справка группы:\n")
	sb.WriteString("ID: " + meta.JID + "\n")
	sb.WriteString("Название: " + name + "\n")
	sb.WriteString("Участники:")

	var entities []domain.TextEntity

	if len(meta.Participants) == 0 {
		sb.WriteString(" нет")
	} else {
		for _, p := range meta.Participants {
			sb.WriteString("\n• ")
			start := len(utf16.Encode([]rune(sb.String())))
			pName := p.Name
			if pName == "" {
				if p.Phone != "" {
					pName = p.Phone
				} else {
					pName = p.JID
				}
			}
			sb.WriteString(pName)
			length := len(utf16.Encode([]rune(pName)))

			var topicURL string
			chat, err := c.store.Chat(ctx, p.JID)
			if err == nil && chat.Topic != nil {
				topicURL = chat.Topic.URL()
			}
			if topicURL != "" {
				entities = append(entities, domain.TextEntity{
					Kind:        "text_link",
					StartUTF16:  start,
					LengthUTF16: length,
					URL:         topicURL,
				})
			}
		}
	}

	return domain.TextContent{
		Text:     sb.String(),
		Entities: entities,
	}
}

func appendHyperlink(original *domain.TextContent, label string, url string) *domain.TextContent {
	if original == nil {
		original = &domain.TextContent{}
	}
	baseText := original.Text
	newText := baseText
	if newText != "" {
		newText += "\n"
	}
	start := len(utf16.Encode([]rune(newText)))
	length := len(utf16.Encode([]rune(label)))
	newText += label

	var entities []domain.TextEntity
	if len(original.Entities) > 0 {
		entities = make([]domain.TextEntity, len(original.Entities))
		copy(entities, original.Entities)
	}

	if url != "" && length > 0 {
		entities = append(entities, domain.TextEntity{
			Kind:        "text_link",
			StartUTF16:  start,
			LengthUTF16: length,
			URL:         url,
		})
	}

	return &domain.TextContent{
		Text:           newText,
		Entities:       entities,
		ReplyTo:        original.ReplyTo,
		ForwardLabel:   original.ForwardLabel,
		MentionTargets: original.MentionTargets,
	}
}
