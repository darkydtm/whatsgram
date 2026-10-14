package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestMutationErrorsAreTypedSafeAndNeverBlindlyRetried(t *testing.T) {
	cases := []struct {
		name, body string
		class      domain.ErrorClass
		retry      time.Duration
	}{
		{"429", `{"ok":false,"error_code":429,"description":"` + testToken + ` private body","parameters":{"retry_after":3}}`, domain.RateLimited, 3 * time.Second},
		{"403", `{"ok":false,"error_code":403,"description":"` + testToken + ` private body"}`, domain.Permission, 0},
		{"400", `{"ok":false,"error_code":400,"description":"` + testToken + ` private body"}`, domain.Permanent, 0},
		{"409", `{"ok":false,"error_code":409,"description":"` + testToken + ` private body"}`, domain.Conflict, 0},
		{"malformed", testToken + ` private body not JSON`, domain.Uncertain, 0},
		{"invalid_result", `{"ok":true,"result":null}`, domain.Uncertain, 0},
		{"too_large", strings.Repeat("x", maxResponseBytes+1), domain.Uncertain, 0},
		{"timeout", "", domain.Uncertain, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			var logs bytes.Buffer
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if initialization(w, r) {
					return
				}
				requests.Add(1)
				if tc.name == "timeout" {
					select {
					case <-r.Context().Done():
					case <-time.After(250 * time.Millisecond):
					}
					return
				}
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c, err := New(testConfig(srv.URL), srv.Client(), slog.New(slog.NewJSONHandler(&logs, nil)))
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Initialize(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err = c.SendText(ctx, domain.SendTextRequest{TelegramTopic: domain.TopicRef{ChatID: -10042, ThreadID: 7}, Content: domain.TextContent{Text: "private body"}})
			var classified domain.AdapterError
			if !errors.As(err, &classified) || classified.Class != tc.class || classified.RetryAfter != tc.retry || classified.MayHaveApplied != (tc.class == domain.Uncertain) {
				t.Fatalf("classification = %v", err)
			}
			if requests.Load() != 1 {
				t.Fatal("mutation automatically retried")
			}
			if classified.Cause != nil {
				t.Fatal("raw cause escaped boundary")
			}
			encoded, _ := json.Marshal(err)
			for _, text := range []string{fmt.Sprintf("%v %+v %#v", err, err, err), string(encoded), logs.String()} {
				if strings.Contains(text, testToken) || strings.Contains(text, "private body") {
					t.Fatal("sensitive error escaped")
				}
			}
		})
	}
}

func TestSendTextRejectsInvalidOrUninitializedTargetsBeforeWire(t *testing.T) {
	var mutations atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if initialization(w, r) {
			return
		}
		mutations.Add(1)
		writeOK(w, "null")
	}))
	defer srv.Close()
	c, err := New(testConfig(srv.URL), srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.SendTextRequest{TelegramTopic: domain.TopicRef{ChatID: -10042, ThreadID: 7}, Content: domain.TextContent{Text: "text"}}
	if _, err := c.SendText(context.Background(), request); err == nil {
		t.Fatal("uninitialized send")
	}
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	invalid := []domain.SendTextRequest{request, request, request, request, request}
	invalid[0].Content.Text = strings.Repeat("x", 4097)
	invalid[1].Content.ReplyTo = &domain.MessageRef{Platform: domain.WhatsApp, Chat: "wa", Author: "wa", ID: "5"}
	invalid[2].TelegramTopic.ChatID = 12
	invalid[3].Content.Entities = []domain.TextEntity{{Kind: "bold", StartUTF16: 10, LengthUTF16: 1}}
	invalid[4].Content.Text = ""
	for _, request := range invalid {
		if _, err := c.SendText(context.Background(), request); err == nil {
			t.Fatal("invalid send")
		}
	}
	if mutations.Load() != 0 {
		t.Fatal("invalid request reached wire")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.SendText(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-transport cancellation = %v", err)
	}
}

func TestRightsLossBlocksNewTraffic(t *testing.T) {
	var lost atomic.Bool
	var mutations atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lost.Load() && strings.HasSuffix(r.URL.Path, "getChatMember") {
			writeOK(w, `{"status":"member","user":{"id":901,"is_bot":true}}`)
			return
		}
		if initialization(w, r) {
			return
		}
		mutations.Add(1)
		writeOK(w, "null")
	}))
	defer srv.Close()
	c, err := New(testConfig(srv.URL), srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	lost.Store(true)
	_, err = c.SendText(context.Background(), domain.SendTextRequest{TelegramTopic: domain.TopicRef{ChatID: -10042, ThreadID: 7}, Content: domain.TextContent{Text: "hi"}})
	var classified domain.AdapterError
	if !errors.As(err, &classified) || classified.Class != domain.Permission || mutations.Load() != 0 {
		t.Fatal("rights loss did not block send")
	}
}
