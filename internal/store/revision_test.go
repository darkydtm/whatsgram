package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestPlanAndTimerCommitAsOneTransaction(t *testing.T) {
	s := testStore(t)
	chat := activeChat(t, s)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, tok := leaseEvent(t, s, event("1"), now)
	bad := TimerSpec{Kind: TimerExpire, Version: e.Version}
	if err := s.PlanEventWithTimers(ctx, e, tok, []domain.Delivery{intent(chat, "create")}, []TimerSpec{bad}, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid atomic timer accepted", err)
	}
	var n int
	for _, table := range []string{"bridge_message", "bridge_outbox", "bridge_timer"} {
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial timed plan", table, n, err)
		}
	}
	spec := TimerSpec{Kind: TimerExpire, Version: e.Version, Deadline: now.Add(time.Hour)}
	if err := s.PlanEventWithTimers(ctx, e, tok, []domain.Delivery{intent(chat, "create")}, []TimerSpec{spec}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_timer WHERE deadline_ms=?", spec.Deadline.UnixMilli()).Scan(&n); err != nil || n != 1 {
		t.Fatal("timer missing from admission plan", n, err)
	}
}

func TestSourceEditRequiresNormalizedOwnerActor(t *testing.T) {
	s := testStore(t)
	activeChat(t, s)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, tok := leaseEvent(t, s, event("1"), now)
	if err := s.PlanEvent(ctx, e, tok, nil, now); err != nil {
		t.Fatal(err)
	}
	edit := event("forged-edit")
	edit.Target = e.Target
	edit.Kind = domain.MessageEdit
	edit.Actor = "other"
	edit.Version.Order = 2
	leased, token := leaseEvent(t, s, edit, now)
	if err := s.PlanEvent(ctx, leased, token, nil, now); !errors.Is(err, ErrConflict) {
		t.Fatal("non-owner source edit changed state", err)
	}
}

func TestOlderSourceRevisionCannotReplaceNewerEffectiveVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, d := sending(t, s, now)
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, completion(d), now); err != nil {
		t.Fatal(err)
	}
	edit := event("third")
	edit.Target = e.Target
	edit.Kind = domain.MessageEdit
	edit.Version.Order = 3
	leased, tok := leaseEvent(t, s, edit, now)
	if err := s.PlanEvent(ctx, leased, tok, nil, now); err != nil {
		t.Fatal(err)
	}
	other := event("second")
	other.Source = domain.WhatsApp
	other.Chat = "123@g.us"
	other.Actor = "account"
	other.Target = &completion(d).Result.Parts[0].Ref
	other.Kind = domain.MessageEdit
	other.Version.Order = 2
	leased, tok = leaseEvent(t, s, other, now)
	if err := s.PlanEvent(ctx, leased, tok, nil, now); err != nil {
		t.Fatal(err)
	}
	state, err := s.Message(ctx, *e.Target)
	if err != nil || state.Version.Order != 3 {
		t.Fatal("effective state regressed", state, err)
	}
}

func TestStaleUnsentEditDoesNotOvertakeNewerEffectiveState(t *testing.T) {
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
	for order := int64(2); order <= 3; order++ {
		key := "edit-two"
		if order == 3 {
			key = "edit-three"
		}
		edit := event(key)
		edit.Kind = domain.MessageEdit
		edit.Target = e.Target
		edit.Version.Order = order
		leased, tok := leaseEvent(t, s, edit, now)
		d := intent(chat, key)
		d.Kind = string(domain.MessageEdit)
		d.ExpectedVersion = edit.Version
		d.Target = &completion(create).Result.Parts[0].Ref
		if err := s.PlanEvent(ctx, leased, tok, []domain.Delivery{d}, now); err != nil {
			t.Fatal(err)
		}
	}
	d, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || d.OperationKey != "edit-three" {
		t.Fatal("stale unsent edit selected", d, err)
	}
}

func TestCompletionAcknowledgesMutationPartSourceState(t *testing.T) {
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
	edit.Target = e.Target
	edit.Kind = domain.MessageEdit
	edit.Version.Order = 2
	leased, tok := leaseEvent(t, s, edit, now)
	d := intent(chat, "edit")
	d.Kind = string(domain.MessageEdit)
	d.ExpectedVersion = edit.Version
	d.Target = &completion(create).Result.Parts[0].Ref
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
	if err := s.CompleteDelivery(ctx, claimed.ID, claimed.LeaseToken, domain.Completion{AppliedVersion: edit.Version}, now); err != nil {
		t.Fatal(err)
	}
	version, deleted, err := s.SourceState(ctx, *d.Target)
	if err != nil || deleted || version != edit.Version {
		t.Fatal("target source not acknowledged", version, deleted, err)
	}
}

func TestTimerRequiresEveryDeletableOwnedPart(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, d := sending(t, s, now)
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, completion(d), now); err != nil {
		t.Fatal(err)
	}
	message, err := s.Message(ctx, *e.Target)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.ScheduleTimer(ctx, TimerSpec{MessageID: message.ID, Kind: TimerExpire, Version: message.Version, Deadline: now})
	if err != nil {
		t.Fatal(err)
	}
	timer, err := s.ClaimTimer(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTimer(ctx, id, timer.LeaseToken, nil, now); !errors.Is(err, ErrConflict) {
		t.Fatal("expiry silently dropped remote parts", err)
	}
}
