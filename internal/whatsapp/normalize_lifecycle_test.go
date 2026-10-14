package whatsapp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/darkydtm/whatsgram/internal/domain"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func TestNormalizeReactionTargetAuthorDiffersFromActorAndRemoves(t *testing.T) {
	c, err := New(context.Background(), deviceFixture(t, true), Options{})
	if err != nil {
		t.Fatal(err)
	}
	actor := types.NewJID("15550000003", types.DefaultUserServer)
	for _, text := range []string{"🐈", "❤️", ""} {
		m := messageFixture("REACTION", actor, &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{Key: &waCommon.MessageKey{ID: proto.String("TARGET"), RemoteJID: proto.String("120000@g.us"), Participant: proto.String("15550000002@s.whatsapp.net"), FromMe: proto.Bool(false)}, Text: proto.String(text), SenderTimestampMS: proto.Int64(1357000)}})
		got, err := c.NormalizeEvent(context.Background(), m)
		if err != nil || len(got) != 1 {
			t.Fatalf("normalize: %v", err)
		}
		e := got[0]
		if e.Kind != domain.ReactionSet || e.Actor != actor.String() || e.Target.Author != "15550000002@s.whatsapp.net" || e.Target.ID != "TARGET" || e.Text.Text != text || e.Version.Order != 1357000 || e.Version.Tie != "REACTION" {
			t.Fatal("reaction actor/target/remove/version conflated")
		}
		if e.Actor == e.Target.Author {
			t.Fatal("reaction must not impersonate target")
		}
	}
}

func TestNormalizeRevokeUnknownEncryptedAndViewOnceHaveLinkedFallback(t *testing.T) {
	c, err := New(context.Background(), deviceFixture(t, true), Options{})
	if err != nil {
		t.Fatal(err)
	}
	actor := types.NewJID("15550000003", types.DefaultUserServer)
	cases := []struct {
		name   string
		raw    *waE2E.Message
		kind   domain.EventKind
		target string
	}{
		{"revoke", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("TARGET"), Participant: proto.String("15550000002@s.whatsapp.net")}, TimestampMS: proto.Int64(2000000)}}, domain.MessageRevoke, "TARGET"},
		{"unknown", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("CANARY-media")}}, "unsupported", "EVENT"},
		{"encrypted", &waE2E.Message{EncReactionMessage: &waE2E.EncReactionMessage{TargetMessageKey: &waCommon.MessageKey{ID: proto.String("TARGET"), Participant: proto.String("15550000002@s.whatsapp.net")}}}, "unsupported", "TARGET"},
		{"view-once", &waE2E.Message{ViewOnceMessage: &waE2E.FutureProofMessage{Message: &waE2E.Message{Conversation: proto.String("CANARY-view-once")}}}, "unsupported", "EVENT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.NormalizeEvent(context.Background(), messageFixture("EVENT", actor, tc.raw))
			if err != nil || len(got) != 1 {
				t.Fatalf("normalize: %v", err)
			}
			e := got[0]
			if e.Validate() != nil || e.Kind != tc.kind || e.Target == nil || e.Target.ID != tc.target {
				t.Fatal("fallback/revoke linkage lost")
			}
			if tc.name == "view-once" && (e.Text != nil || strings.Contains(string(e.Raw), "CANARY") || strings.Contains(string(e.Raw), "Q0FOQVJZ")) {
				t.Fatal("view-once content retained")
			}
		})
	}
}

func TestNormalizeEqualRevisionDifferentPayloadIsConflict(t *testing.T) {
	c, err := New(context.Background(), deviceFixture(t, true), Options{})
	if err != nil {
		t.Fatal(err)
	}
	chat := types.NewJID("120000", types.GroupServer)
	var versions []domain.SourceVersion
	for _, text := range []string{"one", "two"} {
		raw := c.client.BuildEdit(chat, "TARGET", &waE2E.Message{Conversation: proto.String(text)})
		raw.GetEditedMessage().GetMessage().GetProtocolMessage().TimestampMS = proto.Int64(1234567)
		m := messageFixture("EVENT", c.device.GetJID(), raw)
		got, err := c.NormalizeEvent(context.Background(), m)
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, got[0].Version)
	}
	if _, err := versions[0].Compare(versions[1]); err != domain.ErrConflict {
		t.Fatal("same revision different raw must conflict")
	}
	m := messageFixture("EVENT", c.device.GetJID(), &waE2E.Message{Conversation: proto.String(strings.Repeat("x", domain.MaxEventBytes))})
	if _, err := c.NormalizeEvent(context.Background(), m); err == nil {
		t.Fatal("oversize raw accepted")
	}
	m = messageFixture("EVENT", c.device.GetJID(), &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("TARGET"), Participant: proto.String("15550000002@s.whatsapp.net"), RemoteJID: proto.String("OTHER@g.us")}}})
	if _, err := c.NormalizeEvent(context.Background(), m); err == nil {
		t.Fatal("cross-chat target accepted")
	}
	got, err := c.NormalizeEvent(context.Background(), messageFixture("EVENT", c.device.GetJID(), &waE2E.Message{Conversation: proto.String("CANARY-private")}))
	if err != nil {
		t.Fatal(err)
	}
	var raw rawEnvelope
	if json.Unmarshal(got[0].Raw, &raw) != nil || len(raw.Payload) == 0 {
		t.Fatal("native payload must not use redacted public domain JSON")
	}
}
