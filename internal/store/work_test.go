package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func activeChat(t *testing.T, s *Store) Chat {
	t.Helper()
	ctx := context.Background()
	if err := s.DiscoverChat(ctx, domain.ChatCandidate{WhatsAppJID: "123@g.us", Title: "chat", Source: "observed"}, -100); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTopic(ctx, "123@g.us", domain.TopicRef{ChatID: -100, ThreadID: 9, Generation: 1}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Chat(ctx, "123@g.us")
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func leaseEvent(t *testing.T, s *Store, e domain.Event, now time.Time) (domain.Event, string) {
	t.Helper()
	if _, err := s.Accept(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	e, token, err := s.ClaimInbox(context.Background(), now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return e, token
}
func intent(chat Chat, key string) domain.Delivery {
	return domain.Delivery{OperationKey: key, Platform: domain.WhatsApp, Kind: string(domain.MessageCreate), ChatID: chat.ID, TopicGeneration: chat.Generation, WhatsAppSendID: "wa-" + key, EchoActor: "account", ExpectedVersion: domain.SourceVersion{Kind: "tg", Order: 1, Digest: "digest-1", StreamEpoch: "epoch"}, Text: &domain.TextContent{Text: "hello"}}
}

func TestInboxClaimsExcludeSameTargetAndFenceExpiredTokens(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e := event("1")
	if _, err := s.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	e2 := event("2")
	e2.Target = e.Target
	if _, err := s.Accept(ctx, e2); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, err := s.ClaimInbox(ctx, now, time.Second); results <- err }()
	}
	wg.Wait()
	close(results)
	claimed, empty := 0, 0
	for err := range results {
		if err == nil {
			claimed++
		} else if errors.Is(err, ErrNoWork) {
			empty++
		} else {
			t.Fatal(err)
		}
	}
	if claimed != 1 || empty != 1 {
		t.Fatal(claimed, empty)
	}
	if err := s.RecoverExpiredLeases(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	leased, token, err := s.ClaimInbox(ctx, now.Add(time.Second), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PlanEvent(ctx, leased, "wrong", nil, now.Add(time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("wrong token", err)
	}
	if err := s.PlanEvent(ctx, leased, token, nil, now.Add(2*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("expired token", err)
	}
}

func TestPlanCommitsMessageSourceAndIntentAtomically(t *testing.T) {
	s := testStore(t)
	chat := activeChat(t, s)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, token := leaseEvent(t, s, event("1"), now)
	bad := intent(chat, "bad")
	bad.TopicGeneration = 2
	if err := s.PlanEvent(ctx, e, token, []domain.Delivery{bad}, now); !errors.Is(err, ErrConflict) {
		t.Fatal("stale chat plan", err)
	}
	var n int
	for _, table := range []string{"bridge_message", "bridge_part", "bridge_source_state", "bridge_outbox"} {
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial plan", table, n, err)
		}
	}
	if err := s.PlanEvent(ctx, e, token, []domain.Delivery{intent(chat, "create")}, now); err != nil {
		t.Fatal(err)
	}
	parts, err := s.FindParts(ctx, *e.Target)
	if err != nil || len(parts) != 1 || parts[0].Ref != *e.Target {
		t.Fatal(parts, err)
	}
	state, err := s.Message(ctx, *e.Target)
	if err != nil || state.Tombstone || state.Version.Digest != e.Version.Digest {
		t.Fatal(state, err)
	}
	if err := s.PlanEvent(ctx, e, token, nil, now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("replayed lease", err)
	}
}

func TestDeliveryRecoveryNeverBlindlyResendsBegunSend(t *testing.T) {
	s := testStore(t)
	chat := activeChat(t, s)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, token := leaseEvent(t, s, event("1"), now)
	if err := s.PlanEvent(ctx, e, token, []domain.Delivery{intent(chat, "create")}, now); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery(ctx, now, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverExpiredLeases(ctx, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	newD, err := s.ClaimDelivery(ctx, now.Add(time.Second), time.Second)
	if err != nil || newD.ID != d.ID || newD.LeaseToken == d.LeaseToken {
		t.Fatal(newD, err)
	}
	if err := s.BeginSend(ctx, d.ID, d.LeaseToken, now.Add(time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("old worker began", err)
	}
	if err := s.BeginSend(ctx, newD.ID, newD.LeaseToken, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	match, err := s.MatchEcho(ctx, Echo{Platform: domain.WhatsApp, Chat: "123@g.us", Actor: "account", EventID: newD.WhatsAppSendID, Kind: newD.Kind, Digest: newD.ExpectedVersion.Digest})
	if err != nil || !match {
		t.Fatal("echo not reserved before send", match, err)
	}
	if err := s.RecoverExpiredLeases(ctx, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Delivery(ctx, d.ID)
	if err != nil || stored.State != "uncertain" {
		t.Fatal(stored, err)
	}
	if _, err := s.ClaimDelivery(ctx, now.Add(2*time.Second), time.Second); !errors.Is(err, ErrNoWork) {
		t.Fatal("uncertain automatically retried", err)
	}
}

func TestLateEditCannotResurrectTombstoneAndVersionConflictRollsBack(t *testing.T) {
	s := testStore(t)
	activeChat(t, s)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, token := leaseEvent(t, s, event("1"), now)
	if err := s.PlanEvent(ctx, e, token, nil, now); err != nil {
		t.Fatal(err)
	}
	edit := event("edit")
	edit.Target = e.Target
	edit.Kind = domain.MessageEdit
	edit.Version = e.Version
	edit.Version.Digest = "conflict"
	leased, tok := leaseEvent(t, s, edit, now)
	if err := s.PlanEvent(ctx, leased, tok, nil, now); !errors.Is(err, ErrConflict) {
		t.Fatal("same revision overwritten", err)
	}
	if err := s.DeferInbox(ctx, leased.InboxID, tok, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	revoke := event("revoke")
	revoke.Target = e.Target
	revoke.Kind = domain.MessageRevoke
	revoke.Version.Order = 2
	leased, tok = leaseEvent(t, s, revoke, now)
	if err := s.PlanEvent(ctx, leased, tok, nil, now); err != nil {
		t.Fatal(err)
	}
	late := event("late")
	late.Target = e.Target
	late.Kind = domain.MessageEdit
	late.Version.Order = 99
	leased, tok = leaseEvent(t, s, late, now)
	if err := s.PlanEvent(ctx, leased, tok, nil, now); err != nil {
		t.Fatal(err)
	}
	state, err := s.Message(ctx, *e.Target)
	if err != nil || !state.Tombstone || state.Version.Order != 2 {
		t.Fatal("resurrection", state, err)
	}
	parts, err := s.FindParts(ctx, *e.Target)
	if err != nil || !parts[0].Deleted {
		t.Fatal("tombstone metadata lost", parts, err)
	}
}
