package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/store"
)

type checkpoint struct {
	*store.Store
	mu     sync.Mutex
	fail   bool
	events []domain.Event
}

func (s *checkpoint) AcceptTelegramBatch(ctx context.Context, events []domain.Event, offset int64, epoch string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(events) > 0 {
		s.events = append(s.events, events...)
		if s.fail {
			return store.ErrStorage
		}
	}
	return s.Store.AcceptTelegramBatch(ctx, events, offset, epoch)
}

func TestPollingCommitsWholeBatchBeforeAcknowledgmentAndRestarts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "inbox.db")
	db, err := store.Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	recorder := &checkpoint{Store: db, fail: true}
	var mu sync.Mutex
	var offsets []int64
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if initialization(w, r) {
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/bot"+testToken+"/getUpdates" {
			t.Error("unexpected polling wire method")
		}
		var request struct {
			Offset  int64    `json:"offset"`
			Limit   int      `json:"limit"`
			Timeout int      `json:"timeout"`
			Allowed []string `json:"allowed_updates"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Limit != 100 || request.Timeout >= 30 || !reflect.DeepEqual(request.Allowed, []string{"message", "edited_message", "message_reaction", "message_reaction_count", "poll", "poll_answer", "my_chat_member", "chat_member"}) {
			t.Error("incorrect polling admission bounds/allowed_updates")
		}
		mu.Lock()
		offsets = append(offsets, request.Offset)
		mu.Unlock()
		if request.Offset == 0 {
			writeOK(w, `[{"update_id":41,"message":{"message_id":8,"message_thread_id":7,"date":100,"from":{"id":12},"chat":{"id":-10042},"text":"hi"}},{"update_id":42,"message":"malformed-but-identifiable"}]`)
			return
		}
		cursor, err := recorder.Cursor(ctx, "telegram")
		if err != nil || cursor.NextOffset != 43 {
			t.Error("ACK sent before durable checkpoint")
		}
		cancel()
		writeOK(w, "[]")
	}))
	defer srv.Close()
	c, err := New(testConfig(srv.URL), srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = c.RunPolling(pollCtx, recorder, PollOptions{})
	if err == nil {
		t.Fatal("failed commit reported success")
	}
	cursor, err := recorder.Cursor(ctx, "telegram")
	if err != nil || cursor.NextOffset != 0 || cursor.Epoch == "" {
		t.Fatal("initial epoch not durable or failed commit advanced offset")
	}
	if len(offsets) != 1 || offsets[0] != 0 {
		t.Fatal("failed admission acknowledged")
	}
	failed := append([]domain.Event(nil), recorder.events...)
	if len(failed) != 2 || failed[1].Kind != "telegram.invalid" {
		t.Fatal("did not quarantine identifiable invalid update")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder.Store, recorder.fail, recorder.events = db, false, nil
	c, err = New(testConfig(srv.URL), srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = c.RunPolling(pollCtx, recorder, PollOptions{LastContact: time.Now()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown = %v", err)
	}
	if len(recorder.events) != 2 || !reflect.DeepEqual(failed, recorder.events) {
		t.Fatal("restart replay changed normalized identity/raw/version")
	}
	cursor, err = db.Cursor(ctx, "telegram")
	if err != nil || cursor.NextOffset != 43 {
		t.Fatal("restart lost committed cursor")
	}
	if err := db.AcceptTelegramBatch(ctx, failed, 43, cursor.Epoch); err != nil {
		t.Fatalf("stable duplicates rejected: %v", err)
	}
}
