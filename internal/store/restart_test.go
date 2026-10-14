package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestRecoveredSendingRemainsUncertainAcrossReopen(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	_, d := sending(t, s, now)
	var seq int
	var name, path string
	if err := s.db.QueryRowContext(ctx, "PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.RecoverExpiredLeases(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	stored, err := reopened.Delivery(ctx, d.ID)
	if err != nil || stored.State != "uncertain" {
		t.Fatal(stored, err)
	}
	if _, err := reopened.ClaimDelivery(ctx, now.Add(time.Minute), time.Minute); !errors.Is(err, ErrNoWork) {
		t.Fatal("reopen resent uncertain", err)
	}
	echo := Echo{Platform: domain.WhatsApp, Chat: "123@g.us", Actor: "account", EventID: d.WhatsAppSendID, Kind: d.Kind, Digest: d.ExpectedVersion.Digest}
	matched, err := reopened.MatchEcho(ctx, echo)
	if err != nil || !matched {
		t.Fatal("reservation lost", matched, err)
	}
	echo.Actor = "other-device"
	matched, err = reopened.MatchEcho(ctx, echo)
	if err != nil || matched {
		t.Fatal("owner-other-device suppressed", matched, err)
	}
}

func TestSQLiteBusyHasBoundedSafeError(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK")
	started := time.Now()
	_, err = s.Accept(ctx, event("busy"))
	elapsed := time.Since(started)
	if !errors.Is(err, ErrBusy) || elapsed > 5*time.Second {
		t.Fatal("busy unbounded or exposed", elapsed, err)
	}
}

func TestCompletedEchoReservationDoesNotUsePrefixOrFromMe(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().Add(time.Second)
	_, d := sending(t, s, now)
	c := completion(d)
	c.EchoKeys = []string{d.WhatsAppSendID, "exact-secondary"}
	if err := s.CompleteDelivery(ctx, d.ID, d.LeaseToken, c, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"exact-secondary", d.WhatsAppSendID} {
		match, err := s.MatchEcho(ctx, Echo{Platform: domain.WhatsApp, Chat: "123@g.us", Actor: "account", EventID: id, Kind: d.Kind, Digest: d.ExpectedVersion.Digest})
		if err != nil || !match {
			t.Fatal(match, err)
		}
	}
	for _, echo := range []Echo{{Platform: domain.WhatsApp, Chat: "123@g.us", Actor: "account", EventID: d.WhatsAppSendID + "suffix", Kind: d.Kind, Digest: d.ExpectedVersion.Digest}, {Platform: domain.WhatsApp, Chat: "123@g.us", Actor: "account", EventID: d.WhatsAppSendID, Kind: "message.edit", Digest: d.ExpectedVersion.Digest}, {Platform: domain.WhatsApp, Chat: "123@g.us", Actor: "account", EventID: d.WhatsAppSendID, Kind: d.Kind, Digest: "other"}} {
		match, err := s.MatchEcho(ctx, echo)
		if err != nil || match {
			t.Fatal("inexact echo suppressed", match, err)
		}
	}
}

func TestTimerMappingWaitIsStructuralNotRemoteDeletion(t *testing.T) {
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
	id, err := s.ScheduleTimer(ctx, TimerSpec{MessageID: state.ID, Kind: TimerMappingWait, Version: state.Version, Deadline: now})
	if err != nil {
		t.Fatal(err)
	}
	timer, err := s.ClaimTimer(ctx, now, time.Minute)
	if err != nil || timer.Kind != TimerMappingWait {
		t.Fatal(timer, err)
	}
	if err := s.CompleteTimer(ctx, id, timer.LeaseToken, nil, now); err != nil {
		t.Fatal(err)
	}
	state, err = s.Message(ctx, *e.Target)
	if err != nil || state.Tombstone {
		t.Fatal("mapping wait deleted", state, err)
	}
}
