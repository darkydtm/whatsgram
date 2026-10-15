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
	loggedIn  bool
	sends     int
	to        types.JID
	message   *waE2E.Message
	extra     whatsmeow.SendRequestExtra
	response  whatsmeow.SendResponse
	groups    []*types.GroupInfo
	err       error
}

func (f *fakeNetwork) IsConnected() bool { return f.connected }
func (f *fakeNetwork) IsLoggedIn() bool {
	if f.loggedIn {
		return true
	}
	if f.Client != nil && f.Client.Store != nil && f.Client.Store.ID != nil && !f.Client.Store.ID.IsEmpty() {
		return f.connected
	}
	return false
}
func (f *fakeNetwork) ConnectContext(context.Context) error {
	if f.err != nil {
		return f.err
	}
	f.connected = true
	return nil
}
func (f *fakeNetwork) GetJoinedGroups(context.Context) ([]*types.GroupInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.groups, nil
}
func (f *fakeNetwork) GetGroupInfo(_ context.Context, jid types.JID) (*types.GroupInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, g := range f.groups {
		if g != nil && g.JID.ToNonAD() == jid.ToNonAD() {
			return g, nil
		}
	}
	return nil, errors.New("group not found")
}
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

func TestDiscoverChats(t *testing.T) {
	d := deviceFixture(t, true)
	c, err := New(context.Background(), d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	userJID := types.NewJID("15551234567", types.DefaultUserServer)
	if err := d.Contacts.PutContactName(context.Background(), userJID, "Alice", "Alice Smith"); err != nil {
		t.Fatal(err)
	}

	groupJID := types.NewJID("120363000000000000", types.GroupServer)
	fn := &fakeNetwork{
		Client:    c.client,
		connected: true,
		groups: []*types.GroupInfo{
			{
				JID:       groupJID,
				GroupName: types.GroupName{Name: "Test Group"},
				Participants: []types.GroupParticipant{
					{JID: userJID},
				},
			},
		},
	}
	c.network = fn
	candidates, err := c.DiscoverChats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates (group and contact), got %d: %+v", len(candidates), candidates)
	}

	contactMeta, err := c.GetContactMeta(context.Background(), userJID.String())
	if err != nil {
		t.Fatalf("GetContactMeta failed: %v", err)
	}
	if contactMeta.Name != "Alice Smith" || contactMeta.Phone != "+15551234567" {
		t.Fatalf("unexpected contact meta: %+v", contactMeta)
	}

	commonGroups, err := c.GetCommonGroups(context.Background(), userJID.String())
	if err != nil {
		t.Fatalf("GetCommonGroups failed: %v", err)
	}
	if len(commonGroups) != 1 || commonGroups[0].Name != "Test Group" {
		t.Fatalf("unexpected common groups: %+v", commonGroups)
	}

	groupMeta, err := c.GetGroupMeta(context.Background(), groupJID.String())
	if err != nil {
		t.Fatalf("GetGroupMeta failed: %v", err)
	}
	if groupMeta.Name != "Test Group" || len(groupMeta.Participants) != 1 || groupMeta.Participants[0].Name != "Alice Smith" {
		t.Fatalf("unexpected group meta: %+v", groupMeta)
	}
}

func TestFormatContactPhoneAndTitleLIDHandling(t *testing.T) {
	lidJID := types.NewJID("104829375028475", types.HiddenUserServer)
	pnJID := types.NewJID("15551234567", types.DefaultUserServer)

	// LID without phone must not format as phone number
	infoEmpty := types.ContactInfo{}
	if phone := FormatContactPhone(infoEmpty, lidJID); phone != "" {
		t.Fatalf("expected empty phone for LID JID, got %s", phone)
	}

	// PN JID formats with +
	if phone := FormatContactPhone(infoEmpty, pnJID); phone != "+15551234567" {
		t.Fatalf("expected +15551234567 for PN JID, got %s", phone)
	}

	// Priority: ContactName -> PushName -> Phone
	infoFullName := types.ContactInfo{FullName: "Full Name", PushName: "Push Name"}
	if title := FormatContactTitle(infoFullName, pnJID); title != "Full Name" {
		t.Fatalf("expected Full Name, got %s", title)
	}

	infoPushOnly := types.ContactInfo{PushName: "Push Name"}
	if title := FormatContactTitle(infoPushOnly, pnJID); title != "Push Name" {
		t.Fatalf("expected Push Name, got %s", title)
	}

	infoPhoneOnly := types.ContactInfo{}
	if title := FormatContactTitle(infoPhoneOnly, pnJID); title != "+15551234567" {
		t.Fatalf("expected +15551234567, got %s", title)
	}

	// LID with no name and no phone returns non-AD string, not fake phone
	if title := FormatContactTitle(infoPhoneOnly, lidJID); title != lidJID.ToNonAD().String() {
		t.Fatalf("expected %s, got %s", lidJID.ToNonAD().String(), title)
	}
}
