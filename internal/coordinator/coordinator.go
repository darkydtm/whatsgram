package coordinator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/darkydtm/whatsgram/internal/admin"
	"github.com/darkydtm/whatsgram/internal/config"
	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/store"
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
	chatTimers      sync.Map
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
		if idx := strings.LastIndex(key, "/"); idx >= 0 && idx < len(key)-1 {
			if _, ok := c.sentWhatsAppIDs.Load(key[idx+1:]); ok {
				return true
			}
		}
	}
	return false
}

func (c *Coordinator) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.RunInboxLoop(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			select {
			case errCh <- err:
			default:
			}
			cancel()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.RunDeliveryLoop(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			select {
			case errCh <- err:
			default:
			}
			cancel()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := c.RunTimerLoop(runCtx); err != nil && !errors.Is(err, context.Canceled) {
			select {
			case errCh <- err:
			default:
			}
			cancel()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.runLeaseRecoveryLoop(runCtx)
	}()

	var firstErr error
	select {
	case <-ctx.Done():
	case firstErr = <-errCh:
		cancel()
	}

	wg.Wait()
	close(errCh)

	if firstErr != nil {
		return firstErr
	}
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

func (c *Coordinator) setChatTimer(ctx context.Context, jid string, d time.Duration) {
	c.chatTimers.Store(jid, d)
	_ = c.store.SetChatTimer(ctx, jid, d)
}

func (c *Coordinator) getChatTimer(ctx context.Context, jid string) (time.Duration, bool) {
	if val, ok := c.chatTimers.Load(jid); ok {
		if d, ok := val.(time.Duration); ok {
			return d, true
		}
	}
	d, err := c.store.GetChatTimer(ctx, jid)
	if err == nil && d > 0 {
		c.chatTimers.Store(jid, d)
		return d, true
	}
	return 0, false
}

func (c *Coordinator) RunTimerLoop(ctx context.Context) error {
	ticker := time.NewTicker(c.options.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			for {
				err := c.StepTimer(ctx)
				if err != nil {
					if errors.Is(err, store.ErrNoWork) || errors.Is(err, context.Canceled) {
						break
					}
					break
				}
			}
		}
	}
}

func (c *Coordinator) StepTimer(ctx context.Context) error {
	now := time.Now()
	timer, err := c.store.ClaimTimer(ctx, now, c.options.LeaseDuration)
	if err != nil {
		return err
	}

	var deliveries []domain.Delivery
	if timer.Kind == store.TimerExpire {
		for _, part := range timer.Parts {
			if part.BridgeOwned && !part.Deleted && !part.Retired {
				capability := domain.CanRevoke
				if part.Ref.Platform == domain.Telegram {
					capability = domain.CanDelete
				}
				if part.Capabilities&capability != 0 {
					target := part.Ref
					d := domain.Delivery{
						OperationKey:    fmt.Sprintf("timer:revoke:%d:%s:%s", timer.ID, part.Ref.Platform, part.Ref.ID),
						Platform:        part.Ref.Platform,
						Kind:            "message.revoke",
						MessageID:       timer.MessageID,
						ChatID:          timer.ChatID,
						TopicGeneration: part.TopicGeneration,
						Target:          &target,
						ExpectedVersion: timer.Version,
					}
					if part.Ref.Platform == domain.WhatsApp {
						sendID := c.waPort.AllocateSendID()
						c.recordBridgeSend(sendID)
						d.WhatsAppSendID = sendID
						d.EchoActor = part.Ref.Chat
					}
					deliveries = append(deliveries, d)
				}
			}
		}
	}

	return c.store.CompleteTimer(ctx, timer.ID, timer.LeaseToken, deliveries, now)
}
