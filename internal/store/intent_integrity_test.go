package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestIntentVersionBindsAdmittedMessageRevision(t *testing.T) {
	for _, kind := range []domain.EventKind{domain.MessageCreate, domain.MessageEdit, domain.MessageRevoke} {
		t.Run(string(kind), func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			now := time.Now().Add(time.Second)
			chat := activeChat(t, s)
			e := event("1")
			if kind != domain.MessageCreate {
				leased, token := leaseEvent(t, s, e, now)
				if err := s.PlanEvent(ctx, leased, token, nil, now); err != nil {
					t.Fatal(err)
				}
				e.Key = "mutation"
				e.Kind = kind
				e.Version.Order = 2
			}
			leased, token := leaseEvent(t, s, e, now)
			d := intent(chat, "mismatched")
			d.Kind = string(kind)
			if kind != domain.MessageCreate {
				d.Target = &domain.MessageRef{Platform: domain.WhatsApp, Chat: "123@g.us", Author: "account", ID: "mapped-target"}
			}
			d.ExpectedVersion = e.Version
			d.ExpectedVersion.Order = 99
			d.ExpectedVersion.Digest = "unrelated"
			err := s.PlanEvent(ctx, leased, token, []domain.Delivery{d}, now)
			var state string
			var count int
			if readErr := s.db.QueryRowContext(ctx, "SELECT state FROM bridge_inbox WHERE id=?", leased.InboxID).Scan(&state); readErr != nil {
				t.Fatal(readErr)
			}
			if readErr := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_outbox").Scan(&count); readErr != nil {
				t.Fatal(readErr)
			}
			if !errors.Is(err, ErrConflict) || state != "leased" || count != 0 {
				t.Fatalf("mismatched revision accepted: error=%v inbox=%s outbox=%d", err, state, count)
			}
			if kind != domain.MessageCreate {
				m, err := s.Message(ctx, *e.Target)
				if err != nil || m.Version.Order != 1 || m.Tombstone {
					t.Fatal("rejected intent changed observed state", err)
				}
			}
		})
	}
}

func TestBeginSendRejectsCorruptedIntentVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	chat := activeChat(t, s)
	e, token := leaseEvent(t, s, event("1"), now)
	d := intent(chat, "corrupt-version")
	d.Text = &domain.TextContent{Text: "deliberately transformed output"}
	if err := s.PlanEvent(ctx, e, token, []domain.Delivery{d}, now); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	d.MessageID = claimed.MessageID
	d.ExpectedVersion.Order = 99
	payload, err := marshalDelivery(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE bridge_outbox SET payload=? WHERE id=?", payload, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSend(ctx, claimed.ID, claimed.LeaseToken, now); !errors.Is(err, ErrConflict) {
		t.Fatal("corrupt expected state began sending", err)
	}
	stored, err := s.Delivery(ctx, claimed.ID)
	if err != nil || stored.State != "leased" || stored.Delivery.SendStartedAt != nil {
		t.Fatal("corrupt begin partially committed", err)
	}
}
