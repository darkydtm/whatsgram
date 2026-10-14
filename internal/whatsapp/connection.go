package whatsapp

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types/events"
)

func (c *Client) Run(ctx context.Context, sink domain.EventSink) error {
	if ctx == nil || sink == nil {
		return adapterError(domain.Permanent, "invalid_target")
	}
	c.mu.Lock()
	if c.running || c.terminal != "" || c.device.GetJID().IsEmpty() || c.device.Deleted {
		c.mu.Unlock()
		return adapterError(domain.Permanent, "invalid_target")
	}
	c.running = true
	c.mu.Unlock()
	signal := make(chan struct{}, 1)
	id := c.client.AddEventHandlerWithSuccessStatus(func(event any) bool {
		return c.handle(ctx, sink, event, signal)
	})
	defer func() {
		c.network.Disconnect()
		c.client.RemoveEventHandler(id)
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()
	attempts := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.mu.Lock()
		terminal := c.terminal
		c.mu.Unlock()
		if terminal != "" || c.device.GetJID().IsEmpty() || c.device.Deleted {
			return adapterError(domain.Permanent, "invalid_target")
		}
		if !c.network.IsConnected() {
			if attempts >= c.options.MaxConnectAttempts {
				return adapterError(domain.Transient, "not_connected")
			}
			if attempts > 0 {
				delay := c.options.ReconnectBase
				for range attempts - 1 {
					delay = min(c.options.ReconnectMax, delay*2)
				}
				delay = min(c.options.ReconnectMax, delay+time.Duration(rand.Int64N(max(1, int64(delay/4)))))
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-signal:
					timer.Stop()
					continue
				case <-timer.C:
				}
			}
			attempts++
			err := c.network.ConnectContext(privateContext(ctx))
			if err != nil && !errors.Is(err, whatsmeow.ErrAlreadyConnected) {
				c.diagnostic("connect_failed")
				continue
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-signal:
			if c.network.IsConnected() && c.network.IsLoggedIn() {
				attempts = 0
			}
		}
	}
}

func (c *Client) handle(ctx context.Context, sink domain.EventSink, event any, signal chan<- struct{}) (success bool) {
	if ctx.Err() != nil {
		return false
	}
	defer func() {
		if recover() != nil {
			success = false
			c.diagnostic("admission_panic")
		}
	}()
	notify := func() {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
	switch event.(type) {
	case *events.Connected:
		c.diagnostic("connected")
		notify()
	case *events.Disconnected, *events.ManualLoginReconnect:
		c.diagnostic("disconnected")
		notify()
	case *events.LoggedOut, *events.StreamReplaced, *events.ClientOutdated, *events.TemporaryBan, *events.ConnectFailure:
		code := "connection_blocked"
		switch event.(type) {
		case *events.LoggedOut:
			code = "logged_out"
		case *events.StreamReplaced:
			code = "stream_replaced"
		case *events.ClientOutdated:
			code = "client_outdated"
		}
		c.mu.Lock()
		c.terminal = code
		c.mu.Unlock()
		c.diagnostic(code)
		notify()
	}
	normalized, err := c.NormalizeEvent(ctx, event)
	if err != nil {
		c.diagnostic("normalization_failed")
		return false
	}
	for _, e := range normalized {
		if _, err := sink(ctx, e); err != nil {
			c.diagnostic("admission_failed")
			return false
		}
	}
	return true
}

func (c *Client) diagnostic(code string) {
	c.logger.Warn("WhatsApp adapter state", "code", code)
}
