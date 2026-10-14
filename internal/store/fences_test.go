package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestInboxMutationRejectsExpiryAfterWriterWait(t *testing.T) {
	for _, operation := range []string{"defer", "quarantine"} {
		t.Run(operation, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			if _, err := s.Accept(ctx, event("writer-fence")); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			e, token, err := s.ClaimInbox(ctx, now, 200*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback()
			started := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				close(started)
				if operation == "defer" {
					result <- s.DeferInbox(ctx, e.InboxID, token, now.Add(time.Hour), now)
				} else {
					result <- s.QuarantineInbox(ctx, e.InboxID, token, now)
				}
			}()
			<-started
			select {
			case err := <-result:
				t.Fatal("mutation did not wait for writer", err)
			case <-time.After(400 * time.Millisecond):
			}
			if err := lock.Commit(); err != nil {
				t.Fatal(err)
			}
			err = <-result
			var state, storedToken string
			if readErr := s.db.QueryRowContext(ctx, "SELECT state,COALESCE(lease_token,'') FROM bridge_inbox WHERE id=?", e.InboxID).Scan(&state, &storedToken); readErr != nil {
				t.Fatal(readErr)
			}
			if !errors.Is(err, ErrLeaseLost) || state != "leased" || storedToken != token {
				t.Fatalf("post-lock fence failed: error=%v state=%s token_unchanged=%t", err, state, storedToken == token)
			}
		})
	}
}

func TestClaimsStartLeaseAfterWriterWait(t *testing.T) {
	for _, kind := range []string{"inbox", "delivery", "timer"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			now := time.Now()
			if kind == "inbox" {
				if _, err := s.Accept(ctx, event("claim-wait")); err != nil {
					t.Fatal(err)
				}
			} else {
				chat := activeChat(t, s)
				e, token := leaseEvent(t, s, event("claim-wait"), now.Add(time.Second))
				var deliveries []domain.Delivery
				if kind == "delivery" {
					d := intent(chat, "claim-wait")
					d.ExpectedVersion = e.Version
					deliveries = []domain.Delivery{d}
				}
				if err := s.PlanEvent(ctx, e, token, deliveries, now); err != nil {
					t.Fatal(err)
				}
				if kind == "timer" {
					m, err := s.Message(ctx, *e.Target)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.ScheduleTimer(ctx, TimerSpec{MessageID: m.ID, Kind: TimerExpire, Version: m.Version, Deadline: now}); err != nil {
						t.Fatal(err)
					}
				}
			}
			now = time.Now()
			lock, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback()
			type claimResult struct {
				until time.Time
				err   error
			}
			result := make(chan claimResult, 1)
			go func() {
				switch kind {
				case "inbox":
					e, _, err := s.ClaimInbox(ctx, now, 200*time.Millisecond)
					var until int64
					if err == nil {
						err = s.db.QueryRowContext(ctx, "SELECT lease_until_ms FROM bridge_inbox WHERE id=?", e.InboxID).Scan(&until)
					}
					result <- claimResult{time.UnixMilli(until), err}
				case "delivery":
					d, err := s.ClaimDelivery(ctx, now, 200*time.Millisecond)
					result <- claimResult{d.LeaseUntil, err}
				case "timer":
					timer, err := s.ClaimTimer(ctx, now, 200*time.Millisecond)
					result <- claimResult{timer.LeaseUntil, err}
				}
			}()
			select {
			case r := <-result:
				t.Fatal("claim did not wait for writer", r.err)
			case <-time.After(400 * time.Millisecond):
			}
			if err := lock.Commit(); err != nil {
				t.Fatal(err)
			}
			r := <-result
			if r.err != nil || !r.until.After(time.Now()) {
				t.Fatalf("claim returned expired work: error=%v until=%v", r.err, r.until)
			}
		})
	}
}
