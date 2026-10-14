package whatsapp

import (
	"context"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"go.mau.fi/whatsmeow"
)

func validPhone(phone string) bool {
	if len(phone) < 7 || len(phone) > 15 {
		return false
	}
	if phone[0] == '0' {
		return false
	}
	for i := 0; i < len(phone); i++ {
		if phone[i] < '0' || phone[i] > '9' {
			return false
		}
	}
	return true
}

func (c *Client) BeginQR(ctx context.Context) (<-chan domain.PairingNotice, error) {
	if ctx == nil {
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	c.mu.Lock()
	if !c.device.GetJID().IsEmpty() || c.network.IsLoggedIn() {
		c.mu.Unlock()
		return nil, adapterError(domain.Permanent, "invalid_target")
	}
	c.mu.Unlock()

	qrChan, err := c.network.GetQRChannel(ctx)
	if err != nil {
		return nil, adapterError(domain.Transient, "not_connected")
	}
	out := make(chan domain.PairingNotice, 8)
	go func() {
		defer close(out)
		for item := range qrChan {
			if item.Event == "code" {
				timeout := item.Timeout
				if timeout <= 0 {
					timeout = 20 * time.Second
				}
				select {
				case out <- domain.PairingNotice{
					Kind:      "qr",
					Secret:    domain.NewPairingSecret(item.Code),
					ExpiresAt: c.options.Now().Add(timeout),
				}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func (c *Client) BeginPhoneCode(ctx context.Context, phone string) (domain.PairingNotice, error) {
	if ctx == nil || !validPhone(phone) {
		return domain.PairingNotice{}, adapterError(domain.Permanent, "invalid_target")
	}
	c.mu.Lock()
	if !c.device.GetJID().IsEmpty() || c.network.IsLoggedIn() {
		c.mu.Unlock()
		return domain.PairingNotice{}, adapterError(domain.Permanent, "invalid_target")
	}
	c.mu.Unlock()

	code, err := c.network.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
	if err != nil {
		return domain.PairingNotice{}, adapterError(domain.Transient, "not_connected")
	}
	return domain.PairingNotice{
		Kind:      "code",
		Secret:    domain.NewPairingSecret(code),
		ExpiresAt: c.options.Now().Add(160 * time.Second),
	}, nil
}
