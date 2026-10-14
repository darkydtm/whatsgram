package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestAbsoluteTimerSurvivesRestartAndFencesCompletion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "timer.db")
	s, err := Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Second)
	e, d := sending(t, s, now)
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, completion(d), now); err != nil {
		t.Fatal(err)
	}
	state, err := s.Message(ctx, *e.Target)
	if err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(time.Hour)
	id, err := s.ScheduleTimer(ctx, TimerSpec{MessageID: state.ID, Kind: TimerExpire, Version: state.Version, Deadline: deadline})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.ScheduleTimer(ctx, TimerSpec{MessageID: state.ID, Kind: TimerExpire, Version: state.Version, Deadline: deadline})
	if err != nil || duplicate != id {
		t.Fatal(duplicate, err)
	}
	if _, err := s.ScheduleTimer(ctx, TimerSpec{MessageID: state.ID, Kind: TimerExpire, Version: state.Version, Deadline: deadline.Add(time.Second)}); !errors.Is(err, ErrConflict) {
		t.Fatal("deadline reset", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ClaimTimer(ctx, deadline.Add(-time.Millisecond), time.Second); !errors.Is(err, ErrNoWork) {
		t.Fatal("timer fired early", err)
	}
	timer, err := s.ClaimTimer(ctx, deadline, time.Second)
	if err != nil || timer.ID != id || !timer.Deadline.Equal(time.UnixMilli(deadline.UnixMilli())) || len(timer.Parts) != 3 {
		t.Fatal(timer, err)
	}
	if _, err := s.ClaimTimer(ctx, deadline, time.Second); !errors.Is(err, ErrNoWork) {
		t.Fatal("double claim", err)
	}
	if err := s.CompleteTimer(ctx, id, "wrong", nil, deadline); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("wrong token", err)
	}
	if err := s.CompleteTimer(ctx, id, timer.LeaseToken, nil, deadline.Add(time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("expired token", err)
	}
	if err := s.RecoverExpiredLeases(ctx, deadline.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	timer, err = s.ClaimTimer(ctx, deadline.Add(time.Second), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	deletions := []domain.Delivery{}
	for i, p := range timer.Parts {
		if !p.BridgeOwned {
			continue
		}
		del := intent(Chat{ID: d.ChatID, Generation: d.TopicGeneration}, "expire-"+p.Ref.ID)
		del.Kind = string(domain.MessageRevoke)
		del.Target = &timer.Parts[i].Ref
		del.ExpectedVersion = timer.Version
		del.MessageID = timer.MessageID
		deletions = append(deletions, del)
	}
	if err := s.CompleteTimer(ctx, id, timer.LeaseToken, deletions, deadline.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	state, err = s.Message(ctx, *e.Target)
	if err != nil || !state.Tombstone {
		t.Fatal(state, err)
	}
	del, err := s.ClaimDelivery(ctx, deadline.Add(time.Second), time.Minute)
	if err != nil || del.Kind != string(domain.MessageRevoke) {
		t.Fatal("expiry did not durably plan delete", del, err)
	}
}

func TestTimerSuppressesKeptStaleAndTombstonedWork(t *testing.T) {
	for _, mode := range []string{"kept", "stale", "tombstone"} {
		t.Run(mode, func(t *testing.T) {
			s := testStore(t)
			activeChat(t, s)
			ctx := context.Background()
			now := time.Now().Add(time.Second)
			e, tok := leaseEvent(t, s, event("1"), now)
			if err := s.PlanEvent(ctx, e, tok, nil, now); err != nil {
				t.Fatal(err)
			}
			state, err := s.Message(ctx, *e.Target)
			if err != nil {
				t.Fatal(err)
			}
			id, err := s.ScheduleTimer(ctx, TimerSpec{MessageID: state.ID, Kind: TimerExpire, Version: state.Version, Deadline: now})
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "kept":
				if err := s.SetKept(ctx, *e.Target, state.Version, true); err != nil {
					t.Fatal(err)
				}
			case "stale", "tombstone":
				update := event(mode)
				update.Target = e.Target
				update.Kind = domain.MessageEdit
				if mode == "tombstone" {
					update.Kind = domain.MessageRevoke
				}
				update.Version.Order = 2
				leased, token := leaseEvent(t, s, update, now)
				if err := s.PlanEvent(ctx, leased, token, nil, now); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ClaimTimer(ctx, now, time.Minute); !errors.Is(err, ErrNoWork) {
				t.Fatal("invalid expiry claimed", err)
			}
			var status string
			if err := s.db.QueryRowContext(ctx, "SELECT state FROM bridge_timer WHERE id=?", id).Scan(&status); err != nil || status != "cancelled" {
				t.Fatal(status, err)
			}
		})
	}
}

func TestTimerRechecksKeepAtDeletionPlanningBoundary(t *testing.T) {
	s := testStore(t)
	activeChat(t, s)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, tok := leaseEvent(t, s, event("1"), now)
	if err := s.PlanEvent(ctx, e, tok, nil, now); err != nil {
		t.Fatal(err)
	}
	state, err := s.Message(ctx, *e.Target)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.ScheduleTimer(ctx, TimerSpec{MessageID: state.ID, Kind: TimerExpire, Version: state.Version, Deadline: now})
	if err != nil {
		t.Fatal(err)
	}
	timer, err := s.ClaimTimer(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetKept(ctx, *e.Target, state.Version, true); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTimer(ctx, id, timer.LeaseToken, nil, now); !errors.Is(err, ErrConflict) {
		t.Fatal("keep not rechecked", err)
	}
	state, err = s.Message(ctx, *e.Target)
	if err != nil || state.Tombstone {
		t.Fatal("kept message deleted", state, err)
	}
}
