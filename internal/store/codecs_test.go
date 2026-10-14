package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
)

func TestPrivatePersistenceRetainsPayloadAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private.db")
	s, err := Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	chat := activeChat(t, s)
	e := event("private-payload")
	e.Raw = []byte("synthetic raw")
	e.Metadata = []byte("synthetic metadata")
	e.Text = &domain.TextContent{Text: "🙂 private text", Entities: []domain.TextEntity{{Kind: "bold", StartUTF16: 0, LengthUTF16: 2, URL: "https://example.invalid", Mention: "user"}}, ReplyTo: e.Target, ForwardLabel: "synthetic forward", MentionTargets: []domain.IdentityLink{{TelegramUserID: 42, WhatsAppJID: "321@s.whatsapp.net"}}}
	if _, err := s.Accept(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(time.Second)
	leased, token, err := s.ClaimInbox(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	original := leased
	original.InboxID = 0
	if !reflect.DeepEqual(original, e) {
		t.Fatal("private event did not round-trip")
	}
	d := intent(chat, "private-intent")
	d.Text = e.Text
	d.ExpectedVersion = e.Version
	if err := s.PlanEvent(ctx, leased, token, []domain.Delivery{d}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimDelivery(ctx, now, time.Minute)
	if err != nil || !reflect.DeepEqual(claimed.Text, e.Text) {
		t.Fatal("private delivery did not round-trip", err)
	}
	var payload []byte
	if err := s.db.QueryRowContext(ctx, "SELECT state_payload FROM bridge_message WHERE id=?", claimed.MessageID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var text domain.TextContent
	if err := json.Unmarshal(payload, &text); err != nil || !reflect.DeepEqual(&text, e.Text) {
		t.Fatal("effective text lost persisted data", err)
	}
}
