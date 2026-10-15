package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

func TestRateLimiterPacing(t *testing.T) {
	rl := newRateLimiter(false)
	rl.setIntervals(30*time.Millisecond, 80*time.Millisecond)

	ctx := context.Background()

	// First call should not wait significantly
	start := time.Now()
	if err := rl.wait(ctx, true); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 20*time.Millisecond {
		t.Fatalf("first call took too long: %v", time.Since(start))
	}

	// Second chat send should wait for chatInterval (80ms)
	start = time.Now()
	if err := rl.wait(ctx, true); err != nil {
		t.Fatal(err)
	}
	dur := time.Since(start)
	if dur < 60*time.Millisecond {
		t.Fatalf("expected at least 60ms wait for chat send, got %v", dur)
	}

	// Non-chat send should only wait for minInterval (30ms)
	start = time.Now()
	if err := rl.wait(ctx, false); err != nil {
		t.Fatal(err)
	}
	dur = time.Since(start)
	if dur < 20*time.Millisecond {
		t.Fatalf("expected at least 20ms wait for minInterval, got %v", dur)
	}
}

func TestRateLimiterBackoffOn429(t *testing.T) {
	rl := newRateLimiter(true) // zero default intervals

	ctx := context.Background()
	// Trigger 429 TooManyRequests with 1 second RetryAfter
	err429 := &bot.TooManyRequestsError{
		Message:    "Too Many Requests",
		RetryAfter: 1,
	}
	rl.recordError(err429)

	start := time.Now()
	if err := rl.wait(ctx, false); err != nil {
		t.Fatal(err)
	}
	dur := time.Since(start)
	if dur < 900*time.Millisecond {
		t.Fatalf("expected at least ~1s wait after 429, got %v", dur)
	}
}

func TestRateLimiterContextCancellation(t *testing.T) {
	rl := newRateLimiter(false)
	rl.setIntervals(5*time.Second, 5*time.Second)

	_ = rl.wait(context.Background(), true)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := rl.wait(ctx, true)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
}
