package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func sending(t *testing.T, s *Store, now time.Time) (domain.Event, domain.Delivery) {
	t.Helper()
	chat := activeChat(t, s)
	e, tok := leaseEvent(t, s, event("1"), now)
	if err := s.PlanEvent(context.Background(), e, tok, []domain.Delivery{intent(chat, "create")}, now); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery(context.Background(), now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeginSend(context.Background(), d.ID, d.LeaseToken, now); err != nil {
		t.Fatal(err)
	}
	return e, d
}
func completion(d domain.Delivery) domain.Completion {
	return domain.Completion{AppliedVersion: d.ExpectedVersion, Result: domain.SendResult{Parts: []domain.DeliveredPart{{Ref: domain.MessageRef{Platform: domain.WhatsApp, Chat: "123@g.us", Author: "account", ID: "wa-primary"}, Index: 0, Role: domain.RolePrimary, BridgeOwned: true, Capabilities: domain.CanEditText | domain.CanRevoke}, {Ref: domain.MessageRef{Platform: domain.WhatsApp, Chat: "123@g.us", Author: "account", ID: "wa-overflow"}, Index: 1, Role: domain.RoleText, BridgeOwned: true, Capabilities: domain.CanEditText | domain.CanRevoke}}}}
}

func TestCompletionAllPartsMappingAndSuccessAreOneTransaction(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, d := sending(t, s, now)
	c := completion(d)
	c.Result.Parts[1].Index = 0
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, c, now); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate index accepted", err)
	}
	parts, err := s.FindParts(ctx, *e.Target)
	if err != nil || len(parts) != 1 {
		t.Fatal("partial mapping", parts, err)
	}
	stored, err := s.Delivery(ctx, d.ID)
	if err != nil || stored.State != "sending" {
		t.Fatal("partial success", stored, err)
	}
	c = completion(d)
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, c, now); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []domain.MessageRef{*e.Target, c.Result.Parts[0].Ref, c.Result.Parts[1].Ref} {
		parts, err = s.FindParts(ctx, ref)
		if err != nil || len(parts) != 3 {
			t.Fatal("missing all-part linkage", parts, err)
		}
	}
	wrong := c.Result.Parts[0].Ref
	wrong.Chat = "other@g.us"
	if _, err := s.FindParts(ctx, wrong); !errors.Is(err, ErrNoWork) {
		t.Fatal("bare ID leaked mapping", err)
	}
	stored, err = s.Delivery(ctx, d.ID)
	if err != nil || stored.State != "succeeded" {
		t.Fatal(stored, err)
	}
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, c, now); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("completion replay", err)
	}
}

func TestCompletionAfterTombstoneRetainsPartsWithoutResurrection(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, d := sending(t, s, now)
	revoke := event("revoke")
	revoke.Target = e.Target
	revoke.Kind = domain.MessageRevoke
	revoke.Version.Order = 2
	leased, token := leaseEvent(t, s, revoke, now)
	if err := s.PlanEvent(ctx, leased, token, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, completion(d), now); err != nil {
		t.Fatal(err)
	}
	state, err := s.Message(ctx, *e.Target)
	if err != nil || !state.Tombstone || state.Version.Order != 2 {
		t.Fatal(state, err)
	}
	parts, err := s.FindParts(ctx, *e.Target)
	if err != nil || len(parts) != 3 {
		t.Fatal(parts, err)
	}
	for _, p := range parts {
		if !p.Deleted {
			t.Fatal("live part on tombstone", p)
		}
	}
}

func TestDeliveryFailureClassifiesUncertaintyAndFencesExpiredWorker(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	_, d := sending(t, s, now)
	if err := s.FailDelivery(ctx, d.ID, d.LeaseToken, domain.RecoveryDecision{State: "pending", ErrorClass: domain.Transient, AvailableAt: now}, now); !errors.Is(err, ErrInvalid) {
		t.Fatal("begun send blindly retried", err)
	}
	if err := s.FailDelivery(ctx, d.ID, d.LeaseToken, domain.RecoveryDecision{State: "uncertain", ErrorClass: domain.Uncertain}, now.Add(time.Minute)); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("expired failure changed state", err)
	}
	if err := s.FailDelivery(ctx, d.ID, d.LeaseToken, domain.RecoveryDecision{State: "uncertain", ErrorClass: domain.Uncertain}, now); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Delivery(ctx, d.ID)
	if err != nil || stored.State != "uncertain" || stored.ErrorClass != domain.Uncertain {
		t.Fatal(stored, err)
	}
}

func TestDependentMutationWaitsForCreateWhileOtherMessagesProceed(t *testing.T) {
	s := testStore(t)
	chat := activeChat(t, s)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	e, tok := leaseEvent(t, s, event("1"), now)
	if err := s.PlanEvent(ctx, e, tok, []domain.Delivery{intent(chat, "create")}, now); err != nil {
		t.Fatal(err)
	}
	create, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	edit := event("edit")
	edit.Target = e.Target
	edit.Kind = domain.MessageEdit
	edit.Version.Order = 2
	leased, token := leaseEvent(t, s, edit, now)
	mutation := intent(chat, "edit")
	mutation.Kind = string(domain.MessageEdit)
	mutation.ExpectedVersion = edit.Version
	mutation.DependsOnID = create.ID
	mutation.Target = &domain.MessageRef{Platform: domain.WhatsApp, Chat: "123@g.us", Author: "account", ID: "wa-primary"}
	if err := s.PlanEvent(ctx, leased, token, []domain.Delivery{mutation}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimDelivery(ctx, now, time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatal("mutation overtook create", err)
	}
	e2, tok := leaseEvent(t, s, event("2"), now)
	other := intent(chat, "other")
	other.ExpectedVersion = e2.Version
	if err := s.PlanEvent(ctx, e2, tok, []domain.Delivery{other}, now); err != nil {
		t.Fatal(err)
	}
	unrelated, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || unrelated.OperationKey != "other" {
		t.Fatal("unrelated message blocked", unrelated, err)
	}
	if err := s.BeginSend(ctx, create.ID, create.LeaseToken, now); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(ctx, create.ID, create.LeaseToken, completion(create), now); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || next.OperationKey != "edit" {
		t.Fatal("dependency not released", next, err)
	}
}

func TestLeaseCompletionAndChatGenerationMustStillBeCurrent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	_, d := sending(t, s, now)
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, completion(d), now.Add(time.Minute)); !errors.Is(err, ErrLeaseLost) {
		t.Fatal("expired completion", err)
	}
	if err := s.SetChatState(ctx, d.ChatID, 1, ChatMissing); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTopic(ctx, "123@g.us", domain.TopicRef{ChatID: -100, ThreadID: 10, Generation: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, completion(d), now); !errors.Is(err, ErrConflict) {
		t.Fatal("stale generation completed", err)
	}
}
