package whatsapp

import (
	"errors"
	"strconv"
	"strings"

	"github.com/darkydtm/whatsgram/internal/domain"
	"go.mau.fi/whatsmeow"
)

func sendError(err error) error {
	if errors.Is(err, whatsmeow.ErrNoPermissionInGroup) {
		return adapterError(domain.Permission, "permission_denied")
	}
	if errors.Is(err, whatsmeow.ErrRecipientADJID) || errors.Is(err, whatsmeow.ErrUnknownServer) || errors.Is(err, whatsmeow.ErrNotLoggedIn) {
		return adapterError(domain.Permanent, "invalid_target")
	}
	if errors.Is(err, whatsmeow.ErrServerReturnedError) {
		prefix := whatsmeow.ErrServerReturnedError.Error() + " "
		text := err.Error()
		if strings.HasPrefix(text, prefix) {
			suffix := strings.TrimPrefix(text, prefix)
			code, parseErr := strconv.Atoi(suffix)
			if parseErr == nil && strconv.Itoa(code) == suffix {
				switch {
				case code == 429:
					return adapterError(domain.RateLimited, "rate_limited")
				case code == 401 || code == 403:
					return adapterError(domain.Permission, "permission_denied")
				case code >= 500 && code <= 599:
					return adapterError(domain.Transient, "not_connected")
				case code >= 400 && code <= 499:
					return adapterError(domain.Permanent, "invalid_target")
				}
			}
		}
	}
	return adapterError(domain.Uncertain, "uncertain")
}
