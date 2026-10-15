package whatsapp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func messageFixture(id string, sender types.JID, raw *waE2E.Message) *events.Message {
	return (&events.Message{Info: types.MessageInfo{MessageSource: types.MessageSource{Chat: types.NewJID("120000", types.GroupServer), Sender: sender, IsGroup: true}, ID: id, Timestamp: time.Unix(1000, 0)}, RawMessage: raw}).UnwrapRaw()
}

func TestNormalizeOwnerOtherDeviceAndCanonicalMappedIdentity(t *testing.T) {
	d := deviceFixture(t, true)
	c, err := New(context.Background(), d, Options{})
	if err != nil {
		t.Fatal(err)
	}
	pn := types.NewJID("15550000002", types.DefaultUserServer)
	lid := types.NewJID("900002", types.HiddenUserServer)
	if err := d.LIDs.PutLIDMapping(context.Background(), lid, pn); err != nil {
		t.Fatal(err)
	}
	for _, sender := range []types.JID{d.GetJID(), types.NewADJID(lid.User, types.LIDDomain, 7)} {
		m := messageFixture("ORIGINAL", sender, &waE2E.Message{DeviceSentMessage: &waE2E.DeviceSentMessage{DestinationJID: proto.String("120000@g.us"), Message: &waE2E.Message{Conversation: proto.String("private 🐈")}}})
		m.Info.IsFromMe = sender.User == d.GetJID().User
		got, err := c.NormalizeEvent(context.Background(), m)
		if err != nil || len(got) != 1 {
			t.Fatalf("normalize: %v", err)
		}
		e := got[0]
		want := pn.String()
		if m.Info.IsFromMe {
			want = d.GetJID().ToNonAD().String()
		}
		if e.Validate() != nil || e.Kind != domain.MessageCreate || e.Target.Author != want || e.Target.Device != sender.String() || e.Target.ID != "ORIGINAL" || e.Actor != want || e.Text.Text != "private 🐈" {
			t.Fatal("owner-other-device or verified alias lost")
		}
		var envelope rawEnvelope
		if err := json.Unmarshal(e.Raw, &envelope); err != nil {
			t.Fatal(err)
		}
		var original waE2E.Message
		if proto.Unmarshal(envelope.Payload, &original) != nil || original.GetDeviceSentMessage().GetMessage().GetConversation() != "private 🐈" || envelope.ID != "ORIGINAL" {
			t.Fatal("raw envelope lost")
		}
	}
}

func TestNormalizeHistoryEditRetainsEventIDBeforeParseRewrite(t *testing.T) {
	c, err := New(context.Background(), deviceFixture(t, true), Options{})
	if err != nil {
		t.Fatal(err)
	}
	chat := types.NewJID("120000", types.GroupServer)
	raw := c.client.BuildEdit(chat, "TARGET", &waE2E.Message{Conversation: proto.String("changed")})
	raw.GetEditedMessage().GetMessage().GetProtocolMessage().TimestampMS = proto.Int64(1234567)
	web := &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{RemoteJID: proto.String(chat.String()), FromMe: proto.Bool(true), ID: proto.String("EDIT-EVENT")}, Message: raw, MessageTimestamp: proto.Uint64(2000)}
	m, err := c.client.ParseWebMessage(chat, web)
	if err != nil {
		t.Fatal(err)
	}
	if m.Info.ID != "TARGET" || m.Message.GetConversation() != "changed" {
		t.Fatal("fixture must exercise selected ParseWebMessage rewrite")
	}
	got, err := c.NormalizeEvent(context.Background(), m)
	if err != nil || len(got) != 1 {
		t.Fatalf("normalize: %v", err)
	}
	e := got[0]
	if e.Kind != domain.MessageEdit || e.Target.ID != "TARGET" || e.Version.Order != 1234567 || e.Version.Tie != "EDIT-EVENT" || e.Text.Text != "changed" {
		t.Fatal("raw edit target/event/version lost")
	}
	var envelope rawEnvelope
	if json.Unmarshal(e.Raw, &envelope) != nil || envelope.ID != "EDIT-EVENT" {
		t.Fatal("raw event ID rewritten")
	}
	if e.OccurredAt != time.UnixMilli(1234567) {
		t.Fatal("edit order must use protocol timestamp")
	}
}

func TestNormalizeHistorySyncChronologicalOrder(t *testing.T) {
	c, err := New(context.Background(), deviceFixture(t, true), Options{})
	if err != nil {
		t.Fatal(err)
	}
	chat := types.NewJID("120000", types.GroupServer)
	sender := types.NewJID("15550000002", types.DefaultUserServer)

	// Create 3 messages with timestamps: 3000 (newest), 1000 (oldest), 2000 (middle)
	makeWebMsg := func(id, text string, ts uint64) *waWeb.WebMessageInfo {
		raw := &waE2E.Message{Conversation: proto.String(text)}
		return &waWeb.WebMessageInfo{
			Key: &waCommon.MessageKey{
				RemoteJID:   proto.String(chat.String()),
				FromMe:      proto.Bool(false),
				ID:          proto.String(id),
				Participant: proto.String(sender.String()),
			},
			Message:          raw,
			MessageTimestamp: proto.Uint64(ts),
		}
	}

	m3 := makeWebMsg("MSG-3", "third message (newest)", 3000)
	m1 := makeWebMsg("MSG-1", "first message (oldest)", 1000)
	m2 := makeWebMsg("MSG-2", "second message (middle)", 2000)

	syncEvt := &events.HistorySync{
		Data: &waHistorySync.HistorySync{
			Conversations: []*waHistorySync.Conversation{
				{
					ID: proto.String(chat.String()),
					Messages: []*waHistorySync.HistorySyncMsg{
						{Message: m3, MsgOrderID: proto.Uint64(3)},
						{Message: m1, MsgOrderID: proto.Uint64(1)},
						{Message: m2, MsgOrderID: proto.Uint64(2)},
					},
				},
			},
		},
	}

	events, err := c.NormalizeEvent(context.Background(), syncEvt)
	if err != nil {
		t.Fatalf("NormalizeEvent failed: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}

	// Verify chronological order: MSG-1 (1000) -> MSG-2 (2000) -> MSG-3 (3000)
	expectedOrder := []string{"first message (oldest)", "second message (middle)", "third message (newest)"}
	expectedTimestamps := []int64{1000 * 1000, 2000 * 1000, 3000 * 1000} // seconds to millis

	for i, ev := range events {
		if ev.Text.Text != expectedOrder[i] {
			t.Errorf("at index %d: expected text %q, got %q", i, expectedOrder[i], ev.Text.Text)
		}
		if ev.OccurredAt.Unix() != int64(expectedTimestamps[i]/1000) {
			t.Errorf("at index %d: expected timestamp %d, got %d", i, expectedTimestamps[i]/1000, ev.OccurredAt.Unix())
		}
	}
}
