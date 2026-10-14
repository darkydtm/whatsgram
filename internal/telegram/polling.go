package telegram

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/darkydtm/whatsgram/internal/store"
	"github.com/go-telegram/bot/models"
)

var (
	ErrBatchStoreRequired  = errors.New("Telegram batch checkpoint store required")
	ErrEpochReviewRequired = errors.New("Telegram cursor epoch requires operator review")
)

type BatchStore interface {
	AcceptTelegramBatch(context.Context, []domain.Event, int64, string) error
	Cursor(context.Context, string) (store.Cursor, error)
}

type PollOptions struct {
	LastContact time.Time
}

var allowedUpdates = []string{models.AllowedUpdateMessage, models.AllowedUpdateEditedMessage, models.AllowedUpdateMessageReaction, models.AllowedUpdateMessageReactionCount, models.AllowedUpdatePoll, models.AllowedUpdatePollAnswer, models.AllowedUpdateMyChatMember, models.AllowedUpdateChatMember}

var _ domain.TelegramPort = (*Client)(nil)
var _ BatchStore = (*store.Store)(nil)
var _ PartStore = (*store.Store)(nil)

func (c *Client) Run(ctx context.Context, _ domain.EventSink) error {
	c.mu.RLock()
	admission, options := c.admission, c.pollOptions
	c.mu.RUnlock()
	if admission == nil {
		return ErrBatchStoreRequired
	}
	return c.RunPolling(ctx, admission, options)
}

func (c *Client) BindPolling(admission BatchStore, options PollOptions) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.admission, c.pollOptions = admission, options
}

func (c *Client) RunPolling(ctx context.Context, admission BatchStore, options PollOptions) error {
	if admission == nil {
		return ErrBatchStoreRequired
	}
	c.mu.Lock()
	if c.polling {
		c.mu.Unlock()
		return adapterError(domain.Conflict, "conflict")
	}
	c.polling = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.polling = false; c.mu.Unlock() }()
	if err := ctx.Err(); err != nil {
		return err
	}
	cursor, err := admission.Cursor(ctx, "telegram")
	if errors.Is(err, store.ErrNoWork) {
		var epoch [16]byte
		if _, err := rand.Read(epoch[:]); err != nil {
			return adapterError(domain.Permanent, "storage")
		}
		cursor.Epoch = hex.EncodeToString(epoch[:])
		if err := admission.AcceptTelegramBatch(ctx, nil, 0, cursor.Epoch); err != nil {
			return admissionError(ctx, err)
		}
	} else if err != nil {
		return admissionError(ctx, err)
	}
	if cursor.NextOffset < 0 || cursor.Epoch == "" || len(cursor.Epoch) > 256 {
		return adapterError(domain.Conflict, "conflict")
	}
	if cursor.NextOffset > 0 && (options.LastContact.IsZero() || time.Since(options.LastContact) >= 7*24*time.Hour || options.LastContact.After(time.Now().Add(time.Minute))) {
		c.logger.Warn("Telegram cursor requires epoch review")
		return ErrEpochReviewRequired
	}
	if err := c.Initialize(ctx); err != nil {
		return err
	}
	lastContact := time.Now()
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Since(lastContact) >= 7*24*time.Hour {
			return ErrEpochReviewRequired
		}
		events, next, err := c.poll(ctx, cursor)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var classified domain.AdapterError
			if !errors.As(err, &classified) || (classified.Class != domain.Transient && classified.Class != domain.RateLimited) {
				return err
			}
			delay := retryDelay(attempt, classified.RetryAfter)
			if attempt < 6 {
				attempt++
			}
			if err := wait(ctx, delay); err != nil {
				return err
			}
			continue
		}
		lastContact, attempt = time.Now(), 0
		if len(events) > 0 {
			if err := admission.AcceptTelegramBatch(ctx, events, next, cursor.Epoch); err != nil {
				return admissionError(ctx, err)
			}
			cursor.NextOffset = next
		} else if err := wait(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}

func admissionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	switch {
	case errors.Is(err, store.ErrBusy):
		return adapterError(domain.Transient, "busy")
	case errors.Is(err, store.ErrSaturated):
		return adapterError(domain.Transient, "saturated")
	case errors.Is(err, store.ErrConflict):
		return adapterError(domain.Conflict, "conflict")
	default:
		return adapterError(domain.Permanent, "storage")
	}
}

func retryDelay(attempt int, minimum time.Duration) time.Duration {
	if attempt > 6 {
		attempt = 6
	}
	base := time.Duration(1<<attempt) * 250 * time.Millisecond
	if base > 15*time.Second {
		base = 15 * time.Second
	}
	if minimum > base {
		base = minimum
	}
	return base + time.Duration(mathrand.Int64N(int64(250*time.Millisecond)))
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) poll(ctx context.Context, cursor store.Cursor) ([]domain.Event, int64, error) {
	timeout := 20
	if c.http.Timeout < 25*time.Second {
		timeout = int(c.http.Timeout/time.Second) - 1
		if timeout < 0 {
			timeout = 0
		}
	}
	payload, _ := json.Marshal(struct {
		Offset  int64    `json:"offset"`
		Limit   int      `json:"limit"`
		Timeout int      `json:"timeout"`
		Allowed []string `json:"allowed_updates"`
	}{cursor.NextOffset, 100, timeout, allowedUpdates})
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.cfg.Endpoint+"/bot"+c.cfg.Token.Reveal()+"/getUpdates", bytes.NewReader(payload))
	if err != nil {
		return nil, cursor.NextOffset, adapterError(domain.Permanent, "invalid_target")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, cursor.NextOffset, classify(ctx, err, false)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(body) > maxResponseBytes {
		return nil, cursor.NextOffset, adapterError(domain.Transient, "not_connected")
	}
	var envelope struct {
		OK         bool            `json:"ok"`
		Result     json.RawMessage `json:"result"`
		ErrorCode  int             `json:"error_code"`
		Parameters struct {
			RetryAfter int64 `json:"retry_after"`
		} `json:"parameters"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil, cursor.NextOffset, adapterError(domain.Transient, "not_connected")
	}
	if !envelope.OK {
		switch envelope.ErrorCode {
		case 429:
			seconds := envelope.Parameters.RetryAfter
			if seconds < 1 {
				seconds = 1
			}
			if seconds > int64((1<<63-1)/int64(time.Second))-1 {
				return nil, cursor.NextOffset, adapterError(domain.Permanent, "invalid_target")
			}
			return nil, cursor.NextOffset, domain.AdapterError{Class: domain.RateLimited, Code: "rate_limited", RetryAfter: time.Duration(seconds) * time.Second}
		case 401, 403:
			return nil, cursor.NextOffset, adapterError(domain.Permission, "permission_denied")
		case 409:
			return nil, cursor.NextOffset, adapterError(domain.Conflict, "conflict")
		case 400, 404:
			return nil, cursor.NextOffset, adapterError(domain.Permanent, "invalid_target")
		default:
			return nil, cursor.NextOffset, adapterError(domain.Transient, "not_connected")
		}
	}
	var updates []json.RawMessage
	if len(envelope.Result) == 0 || envelope.Result[0] != '[' || json.Unmarshal(envelope.Result, &updates) != nil || len(updates) > 100 {
		return nil, cursor.NextOffset, adapterError(domain.Permanent, "invalid_target")
	}
	events := make([]domain.Event, 0, len(updates))
	next := cursor.NextOffset
	for _, raw := range updates {
		event, err := Normalize(c.cfg, raw, cursor.Epoch)
		if err != nil {
			return nil, cursor.NextOffset, err
		}
		id, _ := strconv.ParseInt(event.Key, 10, 64)
		if id < next {
			return nil, cursor.NextOffset, ErrEpochReviewRequired
		}
		if id == 1<<63-1 {
			return nil, cursor.NextOffset, adapterError(domain.Permanent, "invalid_target")
		}
		next = id + 1
		events = append(events, event)
	}
	return events, next, nil
}
