package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestLateCompletionPreservesNewerObservedSourceRevision(t *testing.T) {
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
	edit := event("edit-two")
	edit.Kind = domain.MessageEdit
	edit.Target = e.Target
	edit.Version.Order = 2
	leased, token := leaseEvent(t, s, edit, now)
	d := intent(chat, "edit-two")
	d.Kind = string(domain.MessageEdit)
	d.ExpectedVersion = edit.Version
	d.Target = &completion(create).Result.Parts[0].Ref
	if err := s.PlanEvent(ctx, leased, token, []domain.Delivery{d}, now); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSend(ctx, claimed.ID, claimed.LeaseToken, now); err != nil {
		t.Fatal(err)
	}
	newer := event("owner-three")
	newer.Source = domain.WhatsApp
	newer.Chat = "123@g.us"
	newer.Actor = "account"
	newer.Target = d.Target
	newer.Kind = domain.MessageEdit
	newer.Version.Order = 3
	newer.Version.Digest = "third-revision"
	leased, token = leaseEvent(t, s, newer, now)
	if err := s.PlanEvent(ctx, leased, token, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(ctx, claimed.ID, claimed.LeaseToken, domain.Completion{AppliedVersion: edit.Version}, now); err != nil {
		t.Fatal(err)
	}
	version, _, err := s.SourceState(ctx, *d.Target)
	if err != nil {
		t.Fatal(err)
	}
	message, err := s.Message(ctx, *d.Target)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("LATE_COMPLETION_SOURCE order=%d effective_order=%d", version.Order, message.Version.Order)
	if version != newer.Version {
		t.Error("late completion regressed newer admitted source state")
	}
}

func TestCompletionConflictsRatherThanReplacingIncomparableSourceState(t *testing.T) {
	for _, conflict := range []string{"epoch", "equal-different", "tombstone"} {
		t.Run(conflict, func(t *testing.T) {
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
			edit := event("edit-two")
			edit.Kind = domain.MessageEdit
			edit.Target = e.Target
			edit.Version.Order = 2
			leased, token := leaseEvent(t, s, edit, now)
			d := intent(chat, "edit-two")
			d.Kind = string(domain.MessageEdit)
			d.ExpectedVersion = edit.Version
			d.Target = &completion(create).Result.Parts[0].Ref
			if err := s.PlanEvent(ctx, leased, token, []domain.Delivery{d}, now); err != nil {
				t.Fatal(err)
			}
			claimed, err := s.ClaimDelivery(ctx, now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.BeginSend(ctx, claimed.ID, claimed.LeaseToken, now); err != nil {
				t.Fatal(err)
			}
			observed := edit.Version
			observed.Digest = "observed-different"
			if conflict == "epoch" {
				observed.StreamEpoch = "different-epoch"
			}
			if conflict == "tombstone" {
				observed.Order = 3
			}
			payload, err := json.Marshal(observed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE bridge_source_state SET version=?,deleted=? WHERE part_id=(SELECT id FROM bridge_part WHERE platform=? AND remote_chat=? AND remote_author=? AND remote_id=?)`, payload, conflict == "tombstone", d.Target.Platform, d.Target.Chat, d.Target.Author, d.Target.ID); err != nil {
				t.Fatal(err)
			}
			err = s.CompleteDelivery(ctx, claimed.ID, claimed.LeaseToken, domain.Completion{AppliedVersion: edit.Version}, now)
			if conflict == "tombstone" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrConflict) {
				t.Fatal("conflicting observation overwritten", err)
			}
			version, deleted, err := s.SourceState(ctx, *d.Target)
			if err != nil || version != observed || deleted != (conflict == "tombstone") {
				t.Fatal("source metadata regressed", err)
			}
			stored, err := s.Delivery(ctx, claimed.ID)
			if err != nil {
				t.Fatal(err)
			}
			if conflict != "tombstone" && stored.State != "sending" {
				t.Fatal("conflict partially completed")
			}
		})
	}
}
