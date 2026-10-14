package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func event(key string) domain.Event {
	return domain.Event{Key: key, Source: domain.Telegram, Kind: domain.MessageCreate, Chat: "-100", Actor: "42", Target: &domain.MessageRef{Platform: domain.Telegram, Chat: "-100", Author: "42", ID: key}, Version: domain.SourceVersion{Kind: "tg", Order: 1, Digest: "digest-" + key, StreamEpoch: "epoch"}, Text: &domain.TextContent{Text: "hello"}, Raw: []byte("synthetic")}
}

func TestChatBindingRequiresExplicitGeneration(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	candidate := domain.ChatCandidate{WhatsAppJID: "123@g.us", Title: "a", Source: "observed", HistoryPartial: true}
	if err := s.DiscoverChat(ctx, candidate, -100); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscoverChat(ctx, candidate, -100); err != nil {
		t.Fatal(err)
	}
	chat, err := s.Chat(ctx, "123@g.us")
	if err != nil || chat.Topic != nil || chat.State != ChatDiscovered {
		t.Fatal(chat, err)
	}
	ref := domain.TopicRef{ChatID: -100, ThreadID: 9, Generation: 1}
	if err := s.BindTopic(ctx, candidate.WhatsAppJID, ref); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTopic(ctx, candidate.WhatsAppJID, ref); err != nil {
		t.Fatal(err)
	}
	ref.ThreadID = 10
	if err := s.BindTopic(ctx, candidate.WhatsAppJID, ref); !errors.Is(err, ErrConflict) {
		t.Fatal("same generation rebound", err)
	}
	if err := s.SetChatState(ctx, chat.ID, 1, ChatMissing); err != nil {
		t.Fatal(err)
	}
	ref.Generation = 2
	if err := s.BindTopic(ctx, candidate.WhatsAppJID, ref); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChatState(ctx, chat.ID, 1, ChatActive); !errors.Is(err, ErrConflict) {
		t.Fatal("stale generation changed chat", err)
	}
	if err := s.DiscoverChat(ctx, domain.ChatCandidate{WhatsAppJID: "123:7@g.us", Title: "bad", Source: "observed"}, -100); !errors.Is(err, ErrInvalid) {
		t.Fatal("noncanonical JID admitted", err)
	}
	if err := s.DiscoverChat(ctx, domain.ChatCandidate{WhatsAppJID: "124@g.us", Title: "a", Source: "observed"}, -100); err != nil {
		t.Fatal(err)
	}
	if err := s.BindTopic(ctx, "124@g.us", domain.TopicRef{ChatID: -100, ThreadID: 10, Generation: 1}); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate topic admitted", err)
	}
}

func TestAdmissionDeduplicatesAndRejectsConflictingPayload(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := event("1")
	a, err := s.Accept(ctx, e)
	if err != nil || !a.Inserted || a.InboxID <= 0 {
		t.Fatal(a, err)
	}
	b, err := s.Accept(ctx, e)
	if err != nil || b.Inserted || b.InboxID != a.InboxID {
		t.Fatal(b, err)
	}
	e.Text.Text = "conflict"
	if _, err := s.Accept(ctx, e); !errors.Is(err, ErrConflict) {
		t.Fatal("same key rewritten", err)
	}
	e = event("future")
	e.Kind = "unknown.kind"
	if _, err := s.Accept(ctx, e); err != nil {
		t.Fatal("unknown dropped", err)
	}
	var state string
	if err := s.db.QueryRowContext(ctx, "SELECT state FROM bridge_inbox WHERE dedupe_key='future'").Scan(&state); err != nil || state != "quarantined" {
		t.Fatal(state, err)
	}
}

func TestTelegramBatchCursorCommitsOrRollsBackTogether(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.AcceptTelegramBatch(ctx, []domain.Event{event("1"), event("2")}, 3, "epoch"); err != nil {
		t.Fatal(err)
	}
	cursor, err := s.Cursor(ctx, "telegram")
	if err != nil || cursor.NextOffset != 3 || cursor.Epoch != "epoch" {
		t.Fatal(cursor, err)
	}
	bad := event("2")
	bad.Raw = []byte("conflict")
	if err := s.AcceptTelegramBatch(ctx, []domain.Event{event("3"), bad}, 4, "epoch"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	cursor, err = s.Cursor(ctx, "telegram")
	if err != nil || cursor.NextOffset != 3 {
		t.Fatal("cursor advanced", cursor, err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_inbox WHERE dedupe_key='3'").Scan(&n); err != nil || n != 0 {
		t.Fatal("partial batch", n, err)
	}
	if err := s.AcceptTelegramBatch(ctx, nil, 2, "epoch"); !errors.Is(err, ErrConflict) {
		t.Fatal("cursor regressed", err)
	}
	if err := s.AcceptTelegramBatch(ctx, nil, 1, "new-epoch"); !errors.Is(err, ErrConflict) {
		t.Fatal("unapproved epoch replaced", err)
	}
}

func TestAdmissionCapacityRejectsWithoutAcknowledging(t *testing.T) {
	ctx := context.Background()
	options := DefaultOptions()
	options.MaxInboxCount = 1
	s, err := OpenWithOptions(ctx, filepath.Join(t.TempDir(), "bounded.db"), time.Second, options)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Accept(ctx, event("1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Accept(ctx, event("1")); err != nil {
		t.Fatal("duplicate rejected at capacity", err)
	}
	if err := s.AcceptTelegramBatch(ctx, []domain.Event{event("2")}, 3, "epoch"); !errors.Is(err, ErrSaturated) {
		t.Fatal(err)
	}
	if _, err := s.Cursor(ctx, "telegram"); !errors.Is(err, ErrNoWork) {
		t.Fatal("failed batch cursor exists", err)
	}
	options = DefaultOptions()
	options.MaxInboxBytes = 1
	s2, err := OpenWithOptions(ctx, filepath.Join(t.TempDir(), "bytes.db"), time.Second, options)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, err := s2.Accept(ctx, event("1")); !errors.Is(err, ErrSaturated) {
		t.Fatal("byte bound ignored", err)
	}
}
