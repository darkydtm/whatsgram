package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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

func TestPollingRateLimitCancellationCapsAndEpochDiagnostic(t *testing.T) {
	cases := []struct {
		name, result string
		offset       int64
		old          bool
		class        domain.ErrorClass
	}{
		{"rate_limit", `{"ok":false,"error_code":429,"description":"` + testToken + `","parameters":{"retry_after":2}}`, 0, false, ""},
		{"unidentifiable", `{"ok":true,"result":[{"message":{}}]}`, 0, false, domain.Permanent},
		{"oversize_update", `{"ok":true,"result":[{"update_id":1,"future":"` + strings.Repeat("x", MaxUpdateBytes) + `"}]}`, 0, false, domain.Permanent},
		{"batch_limit", `{"ok":true,"result":[` + strings.TrimSuffix(strings.Repeat(`{"update_id":1},`, 101), ",") + `]}`, 0, false, domain.Permanent},
		{"lower_epoch", `{"ok":true,"result":[{"update_id":1}]}`, 43, false, ""},
		{"idle_epoch", `{"ok":true,"result":[]}`, 43, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.AcceptTelegramBatch(context.Background(), nil, tc.offset, "epoch"); err != nil {
				t.Fatal(err)
			}
			var count atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if initialization(w, r) {
					return
				}
				count.Add(1)
				fmt.Fprint(w, tc.result)
			}))
			defer srv.Close()
			c, err := New(testConfig(srv.URL), srv.Client(), nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			options := PollOptions{LastContact: time.Now()}
			if tc.old {
				options.LastContact = time.Now().Add(-8 * 24 * time.Hour)
			}
			err = c.RunPolling(ctx, db, options)
			if tc.name == "rate_limit" {
				if !errors.Is(err, context.DeadlineExceeded) || count.Load() != 1 {
					t.Fatal("429 delay/cancellation violated")
				}
				time.Sleep(50 * time.Millisecond)
				if count.Load() != 1 {
					t.Fatal("polling continued after return")
				}
			} else if strings.Contains(tc.name, "epoch") {
				if !errors.Is(err, ErrEpochReviewRequired) {
					t.Fatalf("epoch diagnostic = %v", err)
				}
				if tc.old && count.Load() != 0 {
					t.Fatal("sent unsafe old offset")
				}
			} else {
				var classified domain.AdapterError
				if !errors.As(err, &classified) || classified.Class != tc.class || count.Load() != 1 {
					t.Fatalf("caps/error = %v", err)
				}
			}
			cursor, err := db.Cursor(context.Background(), "telegram")
			if err != nil || cursor.NextOffset != tc.offset || cursor.Epoch != "epoch" {
				t.Fatal("invalid/rate limited response checkpointed")
			}
		})
	}
}

func TestPollingRequestCancellationAndMissingBatchBinding(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	started := make(chan struct{})
	finished := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if initialization(w, r) {
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(finished)
	}))
	defer srv.Close()
	c, err := New(testConfig(srv.URL), srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Run(context.Background(), nil); !errors.Is(err, ErrBatchStoreRequired) {
		t.Fatalf("missing batch store = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	c.BindPolling(db, PollOptions{})
	go func() { result <- c.Run(ctx, nil) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("poll never started")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("poll did not cancel")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("wire request did not cancel")
	}
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
