package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestConnectionRetriesAreBoundedAndCancellationStopsBackoff(t *testing.T) {
	for _, cancelDuringBackoff := range []bool{false, true} {
		c, err := New(context.Background(), deviceFixture(t, true), Options{ReconnectBase: time.Millisecond, ReconnectMax: 4 * time.Millisecond, MaxConnectAttempts: 3})
		if err != nil {
			t.Fatal(err)
		}
		f := &connectionNetwork{fakeNetwork: &fakeNetwork{Client: c.client}, ready: make(chan struct{}, 1), connectErr: errors.New("CANARY-connection")}
		c.network = f
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			result <- c.Run(ctx, func(context.Context, domain.Event) (domain.Admission, error) { return domain.Admission{}, nil })
		}()
		await(t, f.ready)
		if cancelDuringBackoff {
			cancel()
		}
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("failed connection reported success")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("reconnect unbounded")
		}
		cancel()
		if f.connects.Load() > 3 || (!cancelDuringBackoff && f.connects.Load() != 3) {
			t.Fatal("connect attempts exceeded bounds")
		}
	}
}

func TestRunReconnectsAndDoesNotACKOnCanceledAdmission(t *testing.T) {
	c, err := New(context.Background(), deviceFixture(t, true), Options{ReconnectBase: time.Millisecond, ReconnectMax: 4 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	f := &connectionNetwork{fakeNetwork: &fakeNetwork{Client: c.client}, ready: make(chan struct{}, 2)}
	c.network = f
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	var accepts atomic.Int32
	go func() {
		result <- c.Run(ctx, func(context.Context, domain.Event) (domain.Admission, error) {
			accepts.Add(1)
			return domain.Admission{}, nil
		})
	}()
	await(t, f.ready)
	f.online.Store(false)
	c.client.DangerousInternals().DispatchEvent(&events.Disconnected{})
	await(t, f.ready)
	if f.connects.Load() != 2 {
		t.Fatal("disconnect did not reconnect")
	}
	cancel()
	select {
	case <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown timeout")
	}
	signal := make(chan struct{}, 1)
	if c.handle(ctx, func(context.Context, domain.Event) (domain.Admission, error) {
		accepts.Add(1)
		return domain.Admission{}, nil
	}, messageFixture("EVENT", c.device.GetJID(), &waE2E.Message{Conversation: proto.String("text")}), signal) {
		t.Fatal("canceled event admission reported success")
	}
	if accepts.Load() != 0 {
		t.Fatal("canceled sink invoked")
	}
}

func TestAsyncStateAdmissionFailureIsDiagnosedWithoutPrivateData(t *testing.T) {
	var out bytes.Buffer
	c, err := New(context.Background(), deviceFixture(t, true), Options{Logger: slog.New(slog.NewJSONHandler(&out, nil))})
	if err != nil {
		t.Fatal(err)
	}
	signal := make(chan struct{}, 1)
	event := &events.GroupInfo{JID: c.device.GetJID(), Name: nil}
	c.handle(context.Background(), func(context.Context, domain.Event) (domain.Admission, error) {
		return domain.Admission{}, errors.New("CANARY-stanza")
	}, event, signal)
	if bytes.Contains(out.Bytes(), []byte("CANARY")) {
		t.Fatal("state failure leaked")
	}
	c.mu.Lock()
	c.terminal = "logged_out"
	c.mu.Unlock()
	f := &fakeNetwork{Client: c.client, connected: true}
	c.network = f
	if _, err := c.SendText(context.Background(), domain.SendTextRequest{OperationKey: "op", WhatsAppSendID: "ID", WhatsAppChat: "120000@g.us", Content: domain.TextContent{Text: "hi"}}); err == nil || f.sends != 0 {
		t.Fatal("terminal account sent using stale transport")
	}
}
