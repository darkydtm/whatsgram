package config

import (
	"fmt"
	"io"
)

type Secret struct {
	reveal func() string
}

func NewSecret(value string) Secret {
	if value == "" {
		return Secret{}
	}
	return Secret{reveal: func() string { return value }}
}

func (s Secret) Reveal() string {
	if s.reveal == nil {
		return ""
	}
	return s.reveal()
}

func (Secret) String() string                    { return "Secret[redacted]" }
func (Secret) GoString() string                  { return "Secret[redacted]" }
func (Secret) Format(state fmt.State, verb rune) { io.WriteString(state, "Secret[redacted]") }
func (Secret) MarshalJSON() ([]byte, error)      { return []byte(`{"redacted":true}`), nil }
