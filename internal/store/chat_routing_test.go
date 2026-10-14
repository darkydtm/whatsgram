package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestTargetlessEventsCannotChooseForeignChat(t *testing.T) {
	for _, kind := range []domain.EventKind{domain.ChatTitle, domain.ChatTimer, domain.TopicState, domain.ChatDiscovered} {
		t.Run(string(kind), func(t *testing.T) {
			s := testStore(t)
			chat := activeChat(t, s)
			ctx := context.Background()
			now := time.Now().Add(time.Second)
			e := event("foreign-chat")
			e.Source = domain.WhatsApp
			e.Chat = "999@g.us"
			e.Target = nil
			e.Kind = kind
			leased, token := leaseEvent(t, s, e, now)
			d := intent(chat, "foreign-chat")
			d.Platform = domain.Telegram
			d.Kind = "topic.rename"
			d.WhatsAppSendID = ""
			d.ExpectedVersion = e.Version
			err := s.PlanEvent(ctx, leased, token, []domain.Delivery{d}, now)
			if !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalid) {
				t.Fatal("foreign chat event consumed", err)
			}
			assertUnconsumedInbox(t, s, leased.InboxID)
		})
	}
}

func assertUnconsumedInbox(t *testing.T, s *Store, inboxID int64) {
	t.Helper()
	var state string
	var count int
	if err := s.db.QueryRow("SELECT state FROM bridge_inbox WHERE id=?", inboxID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM bridge_outbox").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if state != "leased" || count != 0 {
		t.Fatalf("failed plan consumed work: inbox=%s outbox=%d", state, count)
	}
}

func TestTelegramChatEventRequiresNativeThreadContext(t *testing.T) {
	s := testStore(t)
	chat := activeChat(t, s)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e := event("missing-thread")
	e.Target = nil
	e.Kind = domain.ChatTitle
	leased, token := leaseEvent(t, s, e, now)
	d := intent(chat, "missing-thread")
	d.Platform = domain.Telegram
	d.Kind = "topic.rename"
	d.WhatsAppSendID = ""
	d.ExpectedVersion = e.Version
	if err := s.PlanEvent(ctx, leased, token, []domain.Delivery{d}, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("ambiguous Telegram chat event consumed", err)
	}
	assertUnconsumedInbox(t, s, leased.InboxID)
}
