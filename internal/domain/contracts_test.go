package domain

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestBoundedContextualEvent(t *testing.T) {
	ref := MessageRef{Platform: WhatsApp, Chat: "123@g.us", Author: "321@s.whatsapp.net", ID: "native"}
	if err := ref.Validate(); err != nil {
		t.Fatal(err)
	}
	ref.Author = ""
	if !errors.Is(ref.Validate(), ErrInvalid) {
		t.Fatal("authorless reference admitted")
	}
	e := Event{Key: "key", Source: Telegram, Kind: "future.kind", Chat: "-100", Actor: "42", Version: SourceVersion{Kind: "tg", Digest: "digest", StreamEpoch: "epoch"}}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Raw = make([]byte, MaxEventBytes+1)
	if !errors.Is(e.Validate(), ErrInvalid) {
		t.Fatal("unbounded event admitted")
	}
}

func TestSourceVersionDoesNotInventCrossSourceOrder(t *testing.T) {
	v := SourceVersion{Kind: "edit", Order: 2, Tie: "a", Digest: "one", StreamEpoch: "epoch"}
	if n, err := v.Compare(v); n != 0 || err != nil {
		t.Fatal(n, err)
	}
	other := v
	other.Order = 1
	if n, err := v.Compare(other); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	other = v
	other.Digest = "two"
	if _, err := v.Compare(other); !errors.Is(err, ErrConflict) {
		t.Fatal("digest conflict missing", err)
	}
	other = v
	other.StreamEpoch = "new"
	if _, err := v.Compare(other); !errors.Is(err, ErrConflict) {
		t.Fatal("epoch conflict missing", err)
	}
	other = v
	other.Tie = "z"
	if _, err := v.Compare(other); !errors.Is(err, ErrConflict) {
		t.Fatal("opaque tie acquired invented order", err)
	}
	other = v
	other.Kind = "different"
	if _, err := v.Compare(other); !errors.Is(err, ErrConflict) {
		t.Fatal("cross-source order invented", err)
	}
}

func TestSensitiveTypesFormatSafely(t *testing.T) {
	secret := "DO-NOT-EXPOSE"
	values := []any{PairingNotice{Kind: "qr", Secret: NewPairingSecret(secret)}, Event{Text: &TextContent{Text: secret}, Raw: []byte(secret), Metadata: []byte(secret)}, AdapterError{Class: Transient, Code: secret, Cause: errors.New(secret)}, SendPhotoRequest{Caption: secret}}
	for _, v := range values {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, v), secret) {
				t.Fatalf("secret leaked by %T %s", v, format)
			}
		}
	}
}

func TestSensitiveTypesDoNotLeakThroughStructuredLogging(t *testing.T) {
	secret := "DO-NOT-EXPOSE"
	values := []any{Event{Text: &TextContent{Text: secret}, Raw: []byte(secret)}, PairingNotice{Secret: NewPairingSecret(secret)}, TextContent{Text: secret}, Delivery{Text: &TextContent{Text: secret}}, AdapterError{Code: secret, Cause: errors.New(secret)}, SendPhotoRequest{Caption: secret}}
	for _, value := range values {
		var out bytes.Buffer
		slog.New(slog.NewJSONHandler(&out, nil)).Info("safe", "value", value)
		if strings.Contains(out.String(), secret) {
			t.Fatalf("structured log leaked %T", value)
		}
	}
}

func TestMessageOperationsRequireContextAndValidUTF16Boundaries(t *testing.T) {
	e := Event{Key: "key", Source: Telegram, Kind: MessageEdit, Chat: "-100", Actor: "42", Version: SourceVersion{Kind: "tg", Digest: "digest", StreamEpoch: "epoch"}}
	if !errors.Is(e.Validate(), ErrInvalid) {
		t.Fatal("targetless edit admitted")
	}
	content := TextContent{Text: "🙂", Entities: []TextEntity{{Kind: "bold", StartUTF16: 1, LengthUTF16: 1}}}
	if !errors.Is(content.Validate(), ErrInvalid) {
		t.Fatal("entity split surrogate pair")
	}
	content.Entities[0].StartUTF16 = 0
	content.Entities[0].LengthUTF16 = 2
	if err := content.Validate(); err != nil {
		t.Fatal("valid UTF16 span rejected", err)
	}
}

func TestPartValidatesOwnershipRoleAndCapabilities(t *testing.T) {
	p := DeliveredPart{Ref: MessageRef{Platform: Telegram, Chat: "-100", Author: "bot", ID: "1"}, Role: RolePrimary, BridgeOwned: true, Capabilities: CanEditText | CanDelete}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Capabilities = 1 << 63
	if !errors.Is(p.Validate(), ErrInvalid) {
		t.Fatal("unknown capability admitted")
	}
	p.Capabilities = CanEditText
	p.BridgeOwned = false
	if !errors.Is(p.Validate(), ErrInvalid) {
		t.Fatal("non-owner edit admitted")
	}
	p.Capabilities = 0
	p.Role = "unknown"
	if !errors.Is(p.Validate(), ErrInvalid) {
		t.Fatal("unknown role admitted")
	}
}

func TestTopicRefURL(t *testing.T) {
	cases := []struct {
		topic TopicRef
		want  string
	}{
		{TopicRef{ChatID: -1001234567890, ThreadID: 42}, "https://t.me/c/1234567890/42"},
		{TopicRef{ChatID: 1234567890, ThreadID: 5}, "https://t.me/c/1234567890/5"},
		{TopicRef{ChatID: -1234567890, ThreadID: 10}, "https://t.me/c/1234567890/10"},
	}
	for _, tc := range cases {
		if got := tc.topic.URL(); got != tc.want {
			t.Fatalf("expected %s, got %s", tc.want, got)
		}
	}
}
