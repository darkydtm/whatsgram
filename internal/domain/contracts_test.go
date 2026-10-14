package domain

import (
	"errors"
	"fmt"
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
	other.Kind = "different"
	if _, err := v.Compare(other); !errors.Is(err, ErrConflict) {
		t.Fatal("cross-source order invented", err)
	}
}

func TestSensitiveTypesFormatSafely(t *testing.T) {
	secret := "DO-NOT-EXPOSE"
	values := []any{PairingNotice{Kind: "qr", Secret: secret}, Event{Text: &TextContent{Text: secret}, Raw: []byte(secret), Metadata: []byte(secret)}, AdapterError{Class: Transient, Code: secret, Cause: errors.New(secret)}}
	for _, v := range values {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, v), secret) {
				t.Fatalf("secret leaked by %T %s", v, format)
			}
		}
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
