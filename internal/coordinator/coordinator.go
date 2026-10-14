package coordinator

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/darkydtm/whatsgram/internal/admin"
	"github.com/darkydtm/whatsgram/internal/config"
	"github.com/darkydtm/whatsgram/internal/domain"
)

type Coordinator struct {
	cfg         config.Config
	store       Store
	tgPort      domain.TelegramPort
	waPort      domain.WhatsAppPort
	pairingPort domain.PairingPort
	policy      admin.Policy
	executor    *AdminExecutor
	options     Options

	sentWhatsAppIDs sync.Map
	pendingCount    atomic.Int64
	activeChatCount atomic.Int64
}

func New(
	cfg config.Config,
	st Store,
	tgPort domain.TelegramPort,
	waPort domain.WhatsAppPort,
	pairingPort domain.PairingPort,
	policy admin.Policy,
	opts ...Option,
) (*Coordinator, error) {
	if st == nil || tgPort == nil || waPort == nil || pairingPort == nil {
		return nil, domain.ErrInvalid
	}

	options := Options{
		LeaseDuration: 30 * time.Second,
		PollInterval:  50 * time.Millisecond,
		Version:       "whatsgram-phase2-coordinator-v0.2.0",
	}
	if cfg.Runtime.OperationTimeout > 0 {
		options.LeaseDuration = cfg.Runtime.OperationTimeout
	}
	for _, opt := range opts {
		opt(&options)
	}

	c := &Coordinator{
		cfg:         cfg,
		store:       st,
		tgPort:      tgPort,
		waPort:      waPort,
		pairingPort: pairingPort,
		policy:      policy,
		options:     options,
	}
	c.executor = newAdminExecutor(c)
	return c, nil
}

func (c *Coordinator) Store() Store {
	return c.store
}

func (c *Coordinator) TelegramPort() domain.TelegramPort {
	return c.tgPort
}

func (c *Coordinator) WhatsAppPort() domain.WhatsAppPort {
	return c.waPort
}

func (c *Coordinator) PairingPort() domain.PairingPort {
	return c.pairingPort
}

func (c *Coordinator) Policy() admin.Policy {
	return c.policy
}

func (c *Coordinator) Config() config.Config {
	return c.cfg
}

func (c *Coordinator) recordBridgeSend(id string) {
	if id != "" {
		c.sentWhatsAppIDs.Store(id, time.Now())
	}
}

func (c *Coordinator) isKnownBridgeSend(key, targetID string) bool {
	if targetID != "" {
		if _, ok := c.sentWhatsAppIDs.Load(targetID); ok {
			return true
		}
	}
	if key != "" {
		if _, ok := c.sentWhatsAppIDs.Load(key); ok {
			return true
		}
	}
	return false
}

func (c *Coordinator) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	errCh := make(chan error, 3)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.RunInboxLoop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			select {
			case errCh <- err:
			default:
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.RunDeliveryLoop(ctx); err != nil && !errors.Is(err, context.Canceled) {
			select {
			case errCh <- err:
			default:
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.runLeaseRecoveryLoop(ctx)
	}()

	<-ctx.Done()
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Coordinator) runLeaseRecoveryLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.store.RecoverExpiredLeases(ctx, time.Now())
		}
	}
}
