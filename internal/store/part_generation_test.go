package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestMappedPartGenerationMustMatchDelivery(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	chat := activeChat(t, s)
	e := event("wa-source")
	e.Source = domain.WhatsApp
	e.Chat = "123@g.us"
	e.Actor = "user"
	e.Target = &domain.MessageRef{Platform: domain.WhatsApp, Chat: e.Chat, Author: e.Actor, ID: "wa-source"}
	leased, token := leaseEvent(t, s, e, now)
	d := intent(chat, "tg-create")
	d.Platform = domain.Telegram
	d.WhatsAppSendID = ""
	d.ExpectedVersion = e.Version
	if err := s.PlanEvent(ctx, leased, token, []domain.Delivery{d}, now); err != nil {
		t.Fatal(err)
	}
	create, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSend(ctx, create.ID, create.LeaseToken, now); err != nil {
		t.Fatal(err)
	}
	ref := domain.MessageRef{Platform: domain.Telegram, Chat: "-100", Author: "bot", ID: "tg-old-topic-part"}
	c := domain.Completion{AppliedVersion: e.Version, Result: domain.SendResult{Parts: []domain.DeliveredPart{{Ref: ref, Index: 0, Role: domain.RolePrimary, BridgeOwned: true, Capabilities: domain.CanEditText | domain.CanDelete}}}}
	if err := s.CompleteDelivery(ctx, create.ID, create.LeaseToken, c, now); err != nil {
		t.Fatal(err)
	}
	part, err := s.Part(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if part.TopicGeneration != 1 {
		t.Fatalf("expected part generation 1, got %d", part.TopicGeneration)
	}
	if err := s.SetChatState(ctx, chat.ID, 1, ChatMissing); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTopic(ctx, "123@g.us", domain.TopicRef{ChatID: -100, ThreadID: 10, Generation: 2}); err != nil {
		t.Fatal(err)
	}
	edit := e
	edit.Key = "wa-edit"
	edit.Kind = domain.MessageEdit
	edit.Version.Order = 2
	edit.Version.Digest = "edit-two"
	leased, token = leaseEvent(t, s, edit, now)
	d = intent(Chat{ID: chat.ID, Generation: 2}, "tg-old-target-edit")
	d.Platform = domain.Telegram
	d.WhatsAppSendID = ""
	d.Kind = "message.edit"
	d.Target = &ref
	d.ExpectedVersion = edit.Version
	err = s.PlanEvent(ctx, leased, token, []domain.Delivery{d}, now)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict planning edit for old generation part, got %v", err)
	}
}
