package whatsapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
)

type pairingNetwork struct {
	*fakeNetwork
	qrChan chan whatsmeow.QRChannelItem
	code   string
	err    error
}

func (p *pairingNetwork) GetQRChannel(ctx context.Context) (<-chan whatsmeow.QRChannelItem, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.qrChan, nil
}

func (p *pairingNetwork) PairPhone(ctx context.Context, phone string, showPush bool, clientType whatsmeow.PairClientType, displayName string) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	return p.code, nil
}

func TestPairingRejectsAlreadyPairedOrInvalid(t *testing.T) {
	c, err := New(context.Background(), deviceFixture(t, true), Options{})
	if err != nil {
		t.Fatal(err)
	}
	c.network = &fakeNetwork{Client: c.client, connected: true}
	if _, err := c.BeginQR(context.Background()); err == nil {
		t.Fatal("expected error on BeginQR for already paired device")
	}
	if _, err := c.BeginPhoneCode(context.Background(), "15551234567"); err == nil {
		t.Fatal("expected error on BeginPhoneCode for already paired device")
	}
	if _, err := c.BeginPhoneCode(context.Background(), "invalid-phone"); err == nil {
		t.Fatal("expected error on BeginPhoneCode for invalid phone")
	}
}

func TestPairingSuccess(t *testing.T) {
	c, err := New(context.Background(), deviceFixture(t, false), Options{})
	if err != nil {
		t.Fatal(err)
	}
	qrCh := make(chan whatsmeow.QRChannelItem, 2)
	qrCh <- whatsmeow.QRChannelItem{Event: "code", Code: "TEST-QR-CODE", Timeout: 10 * time.Second}
	close(qrCh)

	pn := &pairingNetwork{fakeNetwork: &fakeNetwork{Client: c.client}, qrChan: qrCh, code: "123-456"}
	c.network = pn

	out, err := c.BeginQR(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	item := <-out
	if item.Kind != "qr" || item.Secret != "TEST-QR-CODE" {
		t.Fatalf("unexpected QR notice: %+v", item)
	}

	notice, err := c.BeginPhoneCode(context.Background(), "15551234567")
	if err != nil {
		t.Fatal(err)
	}
	if notice.Kind != "code" || notice.Secret != "123-456" {
		t.Fatalf("unexpected code notice: %+v", notice)
	}
}

func TestPairingNetworkError(t *testing.T) {
	c, err := New(context.Background(), deviceFixture(t, false), Options{})
	if err != nil {
		t.Fatal(err)
	}
	pn := &pairingNetwork{fakeNetwork: &fakeNetwork{Client: c.client}, err: errors.New("network error")}
	c.network = pn

	if _, err := c.BeginQR(context.Background()); err == nil {
		t.Fatal("expected error on BeginQR network failure")
	}
	if _, err := c.BeginPhoneCode(context.Background(), "15551234567"); err == nil {
		t.Fatal("expected error on BeginPhoneCode network failure")
	}
}

func TestValidPhone(t *testing.T) {
	cases := []struct {
		phone string
		valid bool
	}{
		{"", false},
		{"123456", false},           // too short (< 7)
		{"1234567890123456", false}, // too long (> 15)
		{"012345678", false},        // starts with 0
		{"12345abc89", false},       // non-digit
		{"15551234567", true},
		{"79991234567", true},
	}
	for _, tc := range cases {
		if validPhone(tc.phone) != tc.valid {
			t.Errorf("phone %q: expected %v, got %v", tc.phone, tc.valid, !tc.valid)
		}
	}
}
