package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestTopicCreationResultBindsAtomicallyAndFencesReplacement(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	if err := s.DiscoverChat(ctx, domain.ChatCandidate{WhatsAppJID: "123@g.us", Title: "chat", Source: "observed"}, -100); err != nil {
		t.Fatal(err)
	}
	chat, err := s.Chat(ctx, "123@g.us")
	if err != nil {
		t.Fatal(err)
	}
	e := event("discover")
	e.Source = domain.WhatsApp
	e.Chat = "123@g.us"
	e.Target = nil
	e.Kind = domain.ChatDiscovered
	leased, tok := leaseEvent(t, s, e, now)
	d := intent(chat, "topic")
	d.Platform = domain.Telegram
	d.Kind = "topic.create"
	d.WhatsAppSendID = ""
	d.Text = &domain.TextContent{Text: "chat"}
	if err := s.PlanEvent(ctx, leased, tok, []domain.Delivery{d}, now); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSend(ctx, claimed.ID, claimed.LeaseToken, now); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTopicDelivery(ctx, claimed.ID, claimed.LeaseToken, domain.TopicRef{ChatID: -100, ThreadID: 9, Generation: 2}, now); !errors.Is(err, ErrConflict) {
		t.Fatal("generation skipped", err)
	}
	if err := s.CompleteTopicDelivery(ctx, claimed.ID, claimed.LeaseToken, domain.TopicRef{ChatID: -100, ThreadID: 9, Generation: 1}, now); err != nil {
		t.Fatal(err)
	}
	chat, err = s.ChatByTopic(ctx, -100, 9)
	if err != nil || chat.State != ChatActive || chat.Generation != 1 {
		t.Fatal(chat, err)
	}
	stored, err := s.Delivery(ctx, claimed.ID)
	if err != nil || stored.State != "succeeded" {
		t.Fatal("topic bound without success", stored, err)
	}
	if err := s.CompleteTopicDelivery(ctx, claimed.ID, claimed.LeaseToken, domain.TopicRef{ChatID: -100, ThreadID: 9, Generation: 1}, now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("topic completion replay", err)
	}
}

func TestSourceVersionAndExactPartMetadataAccessible(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, d := sending(t, s, now)
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, completion(d), now); err != nil {
		t.Fatal(err)
	}
	version, deleted, err := s.SourceState(ctx, *e.Target)
	if err != nil || deleted || version != e.Version {
		t.Fatal(version, deleted, err)
	}
	part, err := s.Part(ctx, completion(d).Result.Parts[1].Ref)
	if err != nil || part.Index != 1 || part.Role != domain.RoleText || part.Capabilities&domain.CanRevoke == 0 {
		t.Fatal(part, err)
	}
	if err := s.RetirePart(ctx, part.Ref, version); err != nil {
		t.Fatal(err)
	}
	part, err = s.Part(ctx, part.Ref)
	if err != nil || !part.Retired {
		t.Fatal(part, err)
	}
	parts, err := s.FindParts(ctx, *e.Target)
	if err != nil || len(parts) != 3 {
		t.Fatal("retired mapping erased", parts, err)
	}
}

func TestQuarantineInboxReleasesLeaseWithoutLosingEvent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, tok := leaseEvent(t, s, event("1"), now)
	if err := s.QuarantineInbox(ctx, e.InboxID, tok, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimInbox(ctx, now, time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatal("quarantined work retried", err)
	}
	var payload []byte
	if err := s.db.QueryRowContext(ctx, "SELECT payload FROM bridge_inbox WHERE id=? AND state='quarantined'", e.InboxID).Scan(&payload); err != nil || len(payload) == 0 {
		t.Fatal("quarantine erased", err)
	}
}

func TestOutboxCapacityRollsBackWholePlan(t *testing.T) {
	ctx := context.Background()
	options := DefaultOptions()
	options.MaxOutboxCount = 1
	s := testStore(t)
	s.options = options
	chat := activeChat(t, s)
	now := time.Now().Add(time.Second)
	e, tok := leaseEvent(t, s, event("1"), now)
	if err := s.PlanEvent(ctx, e, tok, []domain.Delivery{intent(chat, "one"), intent(chat, "two")}, now); !errors.Is(err, ErrSaturated) {
		t.Fatal("outbox count ignored", err)
	}
	var n int
	for _, table := range []string{"bridge_message", "bridge_part", "bridge_outbox"} {
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial bounded plan", table, n, err)
		}
	}
	s.options.MaxOutboxCount = 100
	s.options.MaxOutboxBytes = 1
	if err := s.PlanEvent(ctx, e, tok, []domain.Delivery{intent(chat, "one")}, now); !errors.Is(err, ErrSaturated) {
		t.Fatal("outbox bytes ignored", err)
	}
}
