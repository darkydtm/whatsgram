package whatsapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
)

func TestSendErrorsSeparateAcknowledgedRejectionFromUncertainWrite(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		class domain.ErrorClass
	}{
		{"timeout", whatsmeow.ErrMessageTimedOut, domain.Uncertain},
		{"cancel-after-send", context.Canceled, domain.Uncertain},
		{"transport", errors.New("CANARY-stanza-token"), domain.Uncertain},
		{"ack-rate", fmt.Errorf("%w 429", whatsmeow.ErrServerReturnedError), domain.RateLimited},
		{"ack-temporary", fmt.Errorf("%w 503", whatsmeow.ErrServerReturnedError), domain.Transient},
		{"ack-permission", fmt.Errorf("%w 403", whatsmeow.ErrServerReturnedError), domain.Permission},
		{"permission-before-send", whatsmeow.ErrNoPermissionInGroup, domain.Permission},
		{"invalid-target", whatsmeow.ErrRecipientADJID, domain.Permanent},
		{"ack-invalid", fmt.Errorf("%w 400", whatsmeow.ErrServerReturnedError), domain.Permanent},
		{"wrapped-secret", fmt.Errorf("CANARY %w 429", whatsmeow.ErrServerReturnedError), domain.Uncertain},
	}
	c, err := New(context.Background(), deviceFixture(t, true), Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeNetwork{Client: c.client, connected: true}
	c.network = f
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.err = tc.err
			_, err := c.SendText(context.Background(), domain.SendTextRequest{OperationKey: "op", WhatsAppSendID: "EXACT", WhatsAppChat: "120000@g.us", Content: domain.TextContent{Text: "CANARY-body"}})
			var ae domain.AdapterError
			if !errors.As(err, &ae) || ae.Class != tc.class || ae.MayHaveApplied != (tc.class == domain.Uncertain) {
				t.Fatalf("incorrect class: %v", err)
			}
			data, _ := json.Marshal(ae)
			for _, text := range []string{fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), fmt.Sprint(errors.Unwrap(err)), string(data)} {
				if strings.Contains(text, "CANARY") {
					t.Fatal("raw cause leaked")
				}
			}
		})
	}
	f.err = nil
	f.connected = false
	before := f.sends
	_, err = c.SendText(context.Background(), domain.SendTextRequest{OperationKey: "op", WhatsAppSendID: "EXACT", WhatsAppChat: "120000@g.us", Content: domain.TextContent{Text: "hello"}})
	var ae domain.AdapterError
	if !errors.As(err, &ae) || ae.Class != domain.Transient || f.sends != before {
		t.Fatal("offline send must not invoke transport")
	}
}

func TestLibraryLoggerNeverFormatsPrivateArgumentsOrContext(t *testing.T) {
	var out bytes.Buffer
	c, err := New(context.Background(), deviceFixture(t, false), Options{Logger: slog.New(slog.NewJSONHandler(&out, nil))})
	if err != nil {
		t.Fatal(err)
	}
	canary := errors.New("CANARY-qr-session-stanza")
	for _, logger := range []interface {
		Warnf(string, ...any)
		Errorf(string, ...any)
		Infof(string, ...any)
		Debugf(string, ...any)
	}{c.client.Log, c.device.Log, c.client.Log.Sub("CANARY")} {
		logger.Warnf("CANARY %v", canary)
		logger.Errorf("CANARY %v", canary)
		logger.Infof("CANARY %v", canary)
		logger.Debugf("CANARY %v", canary)
	}
	unsafe := zerolog.New(&out).WithContext(context.Background())
	zerolog.Ctx(privateContext(unsafe)).Error().Err(canary).Msg("CANARY")
	if out.Len() != 0 {
		t.Fatal("dependency logging boundary leaked")
	}
}
