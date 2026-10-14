package whatsapp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	bridgeStore "github.com/darkydtm/whatsgram/internal/store"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func deviceFixture(t *testing.T, paired bool) *store.Device {
	t.Helper()
	s, err := bridgeStore.Open(context.Background(), filepath.Join(t.TempDir(), "fixture.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	d := s.SessionStore().NewDevice()
	if paired {
		id := types.NewJID("15550000001", types.DefaultUserServer)
		d.ID = &id
		d.LID = types.NewJID("900001", types.HiddenUserServer)
		d.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
		if err := d.Save(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

type fakeNetwork struct {
	*whatsmeow.Client
	connected bool
	sends     int
	to        types.JID
	message   *waE2E.Message
	extra     whatsmeow.SendRequestExtra
	response  whatsmeow.SendResponse
	err       error
}

func (f *fakeNetwork) IsConnected() bool { return f.connected }
func (f *fakeNetwork) IsLoggedIn() bool  { return f.connected }
func (f *fakeNetwork) SendMessage(_ context.Context, to types.JID, m *waE2E.Message, extra ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	f.sends++
	f.to, f.message, f.extra = to, m, extra[0]
	return f.response, f.err
}

func TestSendTextUsesPersistedIDAndRealReplyProto(t *testing.T) {
	d := deviceFixture(t, true)
	c, err := New(context.Background(), d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c.client.IsConnected() || !c.client.SynchronousAck || !c.client.EnableDecryptedEventBuffer {
		t.Fatal("constructor must be offline with durable admission flags")
	}
	f := &fakeNetwork{Client: c.client, connected: true, response: whatsmeow.SendResponse{ID: "PERSISTED", Sender: d.GetJID(), Timestamp: time.Unix(1000, 0)}}
	c.network = f
	reply := domain.MessageRef{Platform: domain.WhatsApp, Chat: "120000@g.us", Author: "15550000002@s.whatsapp.net", ID: "ORIGINAL"}
	r, err := c.SendText(context.Background(), domain.SendTextRequest{OperationKey: "op", WhatsAppSendID: "PERSISTED", WhatsAppChat: reply.Chat, Content: domain.TextContent{Text: "hello 🐈", ReplyTo: &reply}})
	if err != nil {
		t.Fatal(err)
	}
	ci := f.message.GetExtendedTextMessage().GetContextInfo()
	if f.extra.ID != "PERSISTED" || f.to.String() != reply.Chat || ci.GetStanzaID() != reply.ID || ci.GetParticipant() != reply.Author || ci.GetRemoteJID() != reply.Chat {
		t.Fatal("persisted ID or original reply scope changed")
	}
	if len(r.Parts) != 1 || r.Parts[0].Ref.ID != "PERSISTED" || r.Parts[0].Ref.Author != d.GetJID().ToNonAD().String() || !r.Parts[0].BridgeOwned || r.Parts[0].Capabilities != domain.CanEditText|domain.CanRevoke|domain.CanReact {
		t.Fatalf("incorrect delivered part: %+v", r.Parts)
	}
	if r.AcceptedAt != f.response.Timestamp {
		t.Fatal("acceptance must use response timestamp")
	}
	id := c.AllocateSendID()
	if id == "" || id == c.AllocateSendID() {
		t.Fatal("real GenerateMessageID must allocate distinct IDs")
	}
}

func TestSendTextRejectsMissingIDBeforeTransport(t *testing.T) {
	c, err := New(context.Background(), deviceFixture(t, true), Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeNetwork{Client: c.client, connected: true}
	c.network = f
	_, err = c.SendText(context.Background(), domain.SendTextRequest{OperationKey: "op", WhatsAppChat: "120000@g.us", Content: domain.TextContent{Text: "hello"}})
	var ae domain.AdapterError
	if !errors.As(err, &ae) || ae.Class != domain.Permanent || f.sends != 0 {
		t.Fatal("missing durable ID must fail before transport")
	}
}
