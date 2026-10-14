package coordinator

import (
	"context"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/store"
)

type Store interface {
	ClaimInbox(ctx context.Context, now time.Time, ttl time.Duration) (domain.Event, string, error)
	PlanEvent(ctx context.Context, e domain.Event, token string, deliveries []domain.Delivery, now time.Time) error
	ClaimDelivery(ctx context.Context, now time.Time, ttl time.Duration) (domain.Delivery, error)
	BeginSend(ctx context.Context, id int64, token string, now time.Time) error
	CompleteDelivery(ctx context.Context, id int64, token string, c domain.Completion, now time.Time) error
	FailDelivery(ctx context.Context, id int64, token string, decision domain.RecoveryDecision, now time.Time) error
	RecoverExpiredLeases(ctx context.Context, now time.Time) error
	FindParts(ctx context.Context, ref domain.MessageRef) ([]domain.DeliveredPart, error)
	Part(ctx context.Context, ref domain.MessageRef) (domain.DeliveredPart, error)
	DiscoverChat(ctx context.Context, candidate domain.ChatCandidate, telegramChatID int64) error
	BindTopic(ctx context.Context, waJID string, topic domain.TopicRef) error
	Chat(ctx context.Context, waJID string) (store.Chat, error)
	ChatByTopic(ctx context.Context, telegramChatID, threadID int64) (store.Chat, error)
	QuarantineInbox(ctx context.Context, id int64, token string, now time.Time) error
	DeferInbox(ctx context.Context, id int64, token string, availableAt, now time.Time) error
	MatchEcho(ctx context.Context, echo store.Echo) (bool, error)
}

type Options struct {
	LeaseDuration time.Duration
	PollInterval  time.Duration
	Version       string
}

type Option func(*Options)

func WithLeaseDuration(d time.Duration) Option {
	return func(o *Options) {
		if d > 0 {
			o.LeaseDuration = d
		}
	}
}

func WithPollInterval(d time.Duration) Option {
	return func(o *Options) {
		if d > 0 {
			o.PollInterval = d
		}
	}
}

func WithVersion(v string) Option {
	return func(o *Options) {
		if v != "" {
			o.Version = v
		}
	}
}
