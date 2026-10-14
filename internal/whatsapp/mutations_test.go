package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
)

func TestEditUsesRealBuilderKnownWindowAndDurableOperationTimestamp(t *testing.T) {
	d := deviceFixture(t, true)
	now := time.Unix(2000, 0)
	created := now.Add(-14 * time.Minute)
	c, err := New(context.Background(), d, Options{Now: func() time.Time { return now }, LookupMessage: func(context.Context, domain.MessageRef) (MessageMetadata, error) {
		return MessageMetadata{CreatedAt: created, Text: true}, nil
	}, OperationTime: func(context.Context, string) (time.Time, error) { return now.Add(-time.Second), nil }})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeNetwork{Client: c.client, connected: true, response: whatsmeow.SendResponse{ID: "EDIT-OP"}}
	c.network = f
	ref := domain.MessageRef{Platform: domain.WhatsApp, Chat: "120000@g.us", Author: d.GetJID().String(), ID: "TARGET"}
	if err := c.EditOwnText(context.Background(), ref, domain.TextContent{Text: "changed"}, "EDIT-OP"); err != nil {
		t.Fatal(err)
	}
	p := f.message.GetEditedMessage().GetMessage().GetProtocolMessage()
	if f.extra.ID != "EDIT-OP" || p.GetType() != waE2E.ProtocolMessage_MESSAGE_EDIT || p.GetKey().GetID() != "TARGET" || p.GetKey().GetRemoteJID() != ref.Chat || !p.GetKey().GetFromMe() || p.GetEditedMessage().GetConversation() != "changed" || p.GetTimestampMS() != now.Add(-time.Second).UnixMilli() {
		t.Fatal("real edit builder/operation fields lost")
	}
	for _, elapsed := range []time.Duration{15 * time.Minute, 16 * time.Minute} {
		created = now.Add(-elapsed)
		before := f.sends
		if err := c.EditOwnText(context.Background(), ref, domain.TextContent{Text: "changed"}, "EDIT-OP"); err == nil || f.sends != before {
			t.Fatal("expired edit must fail before transport")
		}
	}
	created = time.Time{}
	if err := c.EditOwnText(context.Background(), ref, domain.TextContent{Text: "changed"}, "EDIT-OP"); err == nil {
		t.Fatal("unknown creation time must not assume fresh")
	}
	ref.Author = "15550000002@s.whatsapp.net"
	if err := c.EditOwnText(context.Background(), ref, domain.TextContent{Text: "changed"}, "EDIT-OP"); err == nil {
		t.Fatal("cannot edit another author's text")
	}
}

func TestRevokeAndReactionUseOriginalAuthorAndExactOperationID(t *testing.T) {
	d := deviceFixture(t, true)
	now := time.Unix(2000, 0)
	c, err := New(context.Background(), d, Options{Now: func() time.Time { return now }, LookupMessage: func(context.Context, domain.MessageRef) (MessageMetadata, error) {
		return MessageMetadata{CreatedAt: now.Add(-time.Hour), Text: true}, nil
	}, OperationTime: func(context.Context, string) (time.Time, error) { return now, nil }})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeNetwork{Client: c.client, connected: true, response: whatsmeow.SendResponse{ID: "REVOKE-OP"}}
	c.network = f
	ref := domain.MessageRef{Platform: domain.WhatsApp, Chat: "120000@g.us", Author: d.GetJID().String(), ID: "TARGET"}
	if err := c.Revoke(context.Background(), ref, "REVOKE-OP"); err != nil {
		t.Fatal(err)
	}
	p := f.message.GetProtocolMessage()
	if f.extra.ID != "REVOKE-OP" || p.GetType() != waE2E.ProtocolMessage_REVOKE || p.GetKey().GetID() != "TARGET" || !p.GetKey().GetFromMe() {
		t.Fatal("revoke must target original account message")
	}
	ref.Author = "15550000002@s.whatsapp.net"
	before := f.sends
	if err := c.Revoke(context.Background(), ref, "REVOKE-OP"); err == nil || f.sends != before {
		t.Fatal("remote revoke cannot assume group admin")
	}
	for _, text := range []string{"🐈", "❤️", whatsmeow.RemoveReactionText} {
		f.response.ID = "REACTION-OP"
		if err := c.SetOwnReaction(context.Background(), ref, text, "REACTION-OP"); err != nil {
			t.Fatal(err)
		}
		r := f.message.GetReactionMessage()
		if f.extra.ID != "REACTION-OP" || r.GetKey().GetID() != "TARGET" || r.GetKey().GetParticipant() != ref.Author || r.GetKey().GetFromMe() || r.GetText() != text || r.GetSenderTimestampMS() != now.UnixMilli() {
			t.Fatal("reaction actor/target/empty-removal incorrect")
		}
	}
	f.err = context.DeadlineExceeded
	var ae domain.AdapterError
	if err := c.SetOwnReaction(context.Background(), ref, "🐈", "REACTION-OP"); !errors.As(err, &ae) || ae.Class != domain.Uncertain {
		t.Fatal("mutation timeout may have applied")
	}
}
