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
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

type connectionNetwork struct {
	*fakeNetwork
	online      atomic.Bool
	connects    atomic.Int32
	disconnects atomic.Int32
	ready       chan struct{}
	connectErr  error
}

func (f *connectionNetwork) IsConnected() bool { return f.online.Load() }
func (f *connectionNetwork) IsLoggedIn() bool  { return f.online.Load() }
func (f *connectionNetwork) ConnectContext(context.Context) error {
	f.connects.Add(1)
	if f.connectErr == nil {
		f.online.Store(true)
	}
	select {
	case f.ready <- struct{}{}:
	default:
	}
	return f.connectErr
}
func (f *connectionNetwork) Disconnect() { f.online.Store(false); f.disconnects.Add(1) }

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("callback timeout")
	}
}

func TestRealSuccessHandlerWaitsForAdmissionAndFailsClosed(t *testing.T) {
	for _, failure := range []string{"none", "storage", "panic"} {
		t.Run(failure, func(t *testing.T) {
			c, err := New(context.Background(), deviceFixture(t, true), Options{})
			if err != nil {
				t.Fatal(err)
			}
			f := &connectionNetwork{fakeNetwork: &fakeNetwork{Client: c.client}, ready: make(chan struct{}, 1)}
			c.network = f
			ctx, cancel := context.WithCancel(context.Background())
			entered, release := make(chan struct{}), make(chan struct{})
			run := make(chan error, 1)
			var accepted atomic.Int32
			go func() {
				run <- c.Run(ctx, func(ctx context.Context, e domain.Event) (domain.Admission, error) {
					accepted.Add(1)
					if e.Actor != "15550000001@s.whatsapp.net" || e.Target.ID != "OWNER-PHONE" {
						return domain.Admission{}, errors.New("unexpected normalization")
					}
					close(entered)
					<-release
					if failure == "panic" {
						panic("CANARY-private")
					}
					if failure == "storage" {
						return domain.Admission{}, errors.New("CANARY-storage")
					}
					return domain.Admission{Inserted: true, InboxID: 1}, nil
				})
			}()
			await(t, f.ready)
			info := types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("120000", types.GroupServer), Sender: c.device.GetJID(), IsGroup: true, IsFromMe: true}, ID: "OWNER-PHONE", Timestamp: time.Unix(1000, 0)}
			result := make(chan bool, 1)
			go func() {
				result <- c.client.DangerousInternals().HandleDecryptedMessage(context.Background(), &info, &waE2E.Message{Conversation: proto.String("CANARY-body")}, 0)
			}()
			await(t, entered)
			select {
			case <-result:
				t.Fatal("ordinary handler returned before durable sink")
			default:
			}
			close(release)
			select {
			case failed := <-result:
				if failed != (failure != "none") {
					t.Fatal("admission failure/panic reported success")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("admission hung")
			}
			cancel()
			select {
			case <-run:
			case <-time.After(3 * time.Second):
				t.Fatal("shutdown hung")
			}
			if f.disconnects.Load() == 0 {
				t.Fatal("shutdown must disconnect")
			}
			before := accepted.Load()
			c.client.DangerousInternals().DispatchEvent(messageFixture("LATE", c.device.GetJID(), &waE2E.Message{Conversation: proto.String("late")}))
			if accepted.Load() != before {
				t.Fatal("handler leaked after shutdown")
			}
		})
	}
}

func TestRunNeverImplicitlyPairsAndStopsOnLoggedOut(t *testing.T) {
	var output bytes.Buffer
	c, err := New(context.Background(), deviceFixture(t, false), Options{Logger: slog.New(slog.NewJSONHandler(&output, nil))})
	if err != nil {
		t.Fatal(err)
	}
	f := &connectionNetwork{fakeNetwork: &fakeNetwork{Client: c.client}, ready: make(chan struct{}, 1)}
	c.network = f
	if err := c.Run(context.Background(), func(context.Context, domain.Event) (domain.Admission, error) { return domain.Admission{}, nil }); err == nil || f.connects.Load() != 0 {
		t.Fatal("Run must not silently initiate pairing")
	}
	c, err = New(context.Background(), deviceFixture(t, true), Options{Logger: slog.New(slog.NewJSONHandler(&output, nil)), ReconnectBase: time.Millisecond, ReconnectMax: 3 * time.Millisecond, MaxConnectAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	f = &connectionNetwork{fakeNetwork: &fakeNetwork{Client: c.client}, ready: make(chan struct{}, 1)}
	c.network = f
	run := make(chan error, 1)
	go func() {
		run <- c.Run(context.Background(), func(context.Context, domain.Event) (domain.Admission, error) { return domain.Admission{}, nil })
	}()
	await(t, f.ready)
	c.client.DangerousInternals().DispatchEvent(&events.LoggedOut{})
	select {
	case err := <-run:
		if err == nil {
			t.Fatal("logout must block")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("logout ignored")
	}
	if f.connects.Load() != 1 {
		t.Fatal("logout retried or paired")
	}
	if !bytes.Contains(output.Bytes(), []byte("logged_out")) {
		t.Fatal("safe operator diagnostic missing")
	}
}
