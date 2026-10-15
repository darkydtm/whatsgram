package telegram

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/go-telegram/bot"
)

type rateLimiter struct {
	mu           sync.Mutex
	lastGlobal   time.Time
	lastChatSend time.Time
	blockedUntil time.Time
	minInterval  time.Duration
	chatInterval time.Duration
}

func newRateLimiter(localAPI bool) *rateLimiter {
	rl := &rateLimiter{}
	if !localAPI {
		rl.minInterval = 50 * time.Millisecond
		rl.chatInterval = 1000 * time.Millisecond
	}
	return rl
}

func (l *rateLimiter) setIntervals(minInterval, chatInterval time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.minInterval = minInterval
	l.chatInterval = chatInterval
}

func (l *rateLimiter) wait(ctx context.Context, isChatSend bool) error {
	for {
		l.mu.Lock()
		now := time.Now()
		var waitDur time.Duration

		if now.Before(l.blockedUntil) {
			waitDur = l.blockedUntil.Sub(now)
		} else if isChatSend && l.chatInterval > 0 && now.Sub(l.lastChatSend) < l.chatInterval {
			waitDur = l.chatInterval - now.Sub(l.lastChatSend)
		} else if l.minInterval > 0 && now.Sub(l.lastGlobal) < l.minInterval {
			waitDur = l.minInterval - now.Sub(l.lastGlobal)
		}

		if waitDur <= 0 {
			now = time.Now()
			l.lastGlobal = now
			if isChatSend {
				l.lastChatSend = now
			}
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waitDur):
		}
	}
}

func (l *rateLimiter) recordError(err error) {
	if err == nil {
		return
	}
	var limited *bot.TooManyRequestsError
	if errors.As(err, &limited) {
		delay := time.Duration(limited.RetryAfter) * time.Second
		if delay < time.Second {
			delay = time.Second
		}
		l.mu.Lock()
		blocked := time.Now().Add(delay)
		if blocked.After(l.blockedUntil) {
			l.blockedUntil = blocked
		}
		l.mu.Unlock()
	}
}
