package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestQueuedCreateDoesNotSendAfterTombstone(t *testing.T) {
	s := testStore(t)
	chat := activeChat(t, s)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, tok := leaseEvent(t, s, event("1"), now)
	if err := s.PlanEvent(ctx, e, tok, []domain.Delivery{intent(chat, "create")}, now); err != nil {
		t.Fatal(err)
	}
	revoke := event("revoke")
	revoke.Target = e.Target
	revoke.Kind = domain.MessageRevoke
	leased, token := leaseEvent(t, s, revoke, now)
	if err := s.PlanEvent(ctx, leased, token, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDelivery(ctx, now, time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatal("tombstone create sent", err)
	}
	var status string
	if err := s.db.QueryRowContext(ctx, "SELECT state FROM bridge_outbox WHERE operation_key='create'").Scan(&status); err != nil || status != "superseded" {
		t.Fatal(status, err)
	}
}

func TestBeginRevalidatesMappedTargetAndCapabilities(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, create := sending(t, s, now)
	if err := s.CompleteDelivery(ctx, create.ID, create.LeaseToken, completion(create), now); err != nil {
		t.Fatal(err)
	}
	chat, err := s.Chat(ctx, "123@g.us")
	if err != nil {
		t.Fatal(err)
	}
	edit := event("edit")
	edit.Kind = domain.MessageEdit
	edit.Target = e.Target
	edit.Version.Order = 2
	leased, token := leaseEvent(t, s, edit, now)
	d := intent(chat, "edit")
	d.Kind = string(domain.MessageEdit)
	d.ExpectedVersion = edit.Version
	d.Target = &domain.MessageRef{Platform: domain.WhatsApp, Chat: "123@g.us", Author: "account", ID: "unmapped"}
	if err := s.PlanEvent(ctx, leased, token, []domain.Delivery{d}, now); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSend(ctx, claimed.ID, claimed.LeaseToken, now); !errors.Is(err, ErrConflict) {
		t.Fatal("unmapped target began", err)
	}
}

func TestTwoStoresClaimDeliveryOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var sequence int
	var name, path string
	if err := s.db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	now := time.Now().Add(time.Second)
	chat := activeChat(t, s)
	e, tok := leaseEvent(t, s, event("1"), now)
	if err := s.PlanEvent(ctx, e, tok, []domain.Delivery{intent(chat, "create")}, now); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, worker := range []*Store{s, other} {
		wg.Add(1)
		go func(worker *Store) {
			defer wg.Done()
			_, err := worker.ClaimDelivery(ctx, now, time.Minute)
			out <- err
		}(worker)
	}
	wg.Wait()
	close(out)
	count := 0
	for err := range out {
		if err == nil {
			count++
		} else if !errors.Is(err, ErrNoWork) {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatal("double delivery claim", count)
	}
}

func TestInboxAliasesOfOneLogicalMessageCannotLeaseTogether(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, d := sending(t, s, now)
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, completion(d), now); err != nil {
		t.Fatal(err)
	}
	edit := event("tg-edit")
	edit.Target = e.Target
	edit.Kind = domain.MessageEdit
	edit.Version.Order = 2
	waEdit := event("wa-edit")
	waEdit.Source = domain.WhatsApp
	waEdit.Chat = "123@g.us"
	waEdit.Target = &completion(d).Result.Parts[0].Ref
	waEdit.Kind = domain.MessageEdit
	if _, err := s.Accept(ctx, edit); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(ctx, waEdit); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimInbox(ctx, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimInbox(ctx, now, time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatal("logical message aliases concurrently leased", err)
	}
}

func TestLeaseCannotBeExtendedByPassingOldNow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	chat := activeChat(t, s)
	now := time.Now().Add(time.Second)
	e, tok := leaseEvent(t, s, event("1"), now)
	if err := s.PlanEvent(ctx, e, tok, []domain.Delivery{intent(chat, "create")}, now); err != nil {
		t.Fatal(err)
	}
	claimedAt := now.Add(time.Second)
	d, err := s.ClaimDelivery(ctx, claimedAt, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	wait := time.Until(d.LeaseUntil) + 5*time.Millisecond
	if wait > 0 {
		time.Sleep(wait)
	}
	if err := s.BeginSend(ctx, d.ID, d.LeaseToken, claimedAt); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("old now extended expired lease", err)
	}
}

func TestPlannerRejectsForeignChatBinding(t *testing.T) {
	s := testStore(t)
	chat := activeChat(t, s)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e := event("1")
	e.Source = domain.WhatsApp
	e.Chat = "999@g.us"
	e.Target = &domain.MessageRef{Platform: domain.WhatsApp, Chat: e.Chat, Author: "user", ID: "1"}
	leased, tok := leaseEvent(t, s, e, now)
	d := intent(chat, "bad")
	d.Platform = domain.Telegram
	d.WhatsAppSendID = ""
	if err := s.PlanEvent(ctx, leased, tok, []domain.Delivery{d}, now); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign chat attached", err)
	}
}
