package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/darkydtm/whatsgram/internal/domain"
	"github.com/go-telegram/bot"
)

const maxResponseBytes = 8 << 20

type boundedClient struct{ client *http.Client }

func (b boundedClient) Do(req *http.Request) (*http.Response, error) {
	response, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	response.Body = &boundedBody{ReadCloser: response.Body, remaining: maxResponseBytes}
	return response, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n != 0 {
			return 0, errors.New("response exceeds limit")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func classify(ctx context.Context, err error, mutation bool) error {
	if err == nil {
		return nil
	}
	var limited *bot.TooManyRequestsError
	if errors.As(err, &limited) {
		delay := time.Duration(limited.RetryAfter) * time.Second
		if delay < time.Second {
			delay = time.Second
		}
		return domain.AdapterError{Class: domain.RateLimited, Code: "rate_limited", RetryAfter: delay}
	}
	switch {
	case errors.Is(err, bot.ErrorForbidden), errors.Is(err, bot.ErrorUnauthorized):
		return adapterError(domain.Permission, "permission_denied")
	case errors.Is(err, bot.ErrorBadRequest), errors.Is(err, bot.ErrorNotFound):
		return adapterError(domain.Permanent, "invalid_target")
	case errors.Is(err, bot.ErrorConflict):
		return adapterError(domain.Conflict, "conflict")
	}
	if mutation {
		return adapterError(domain.Uncertain, "uncertain")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return adapterError(domain.Transient, "not_connected")
}
