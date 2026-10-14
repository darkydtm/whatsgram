package config

import (
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
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

func (s *Secret) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return errors.New("invalid secret YAML value")
	}
	*s = NewSecret(node.Value)
	return nil
}
