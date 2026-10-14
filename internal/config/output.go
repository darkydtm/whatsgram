package config

import (
	"fmt"
	"io"
)

func (Config) String() string                    { return "Config[redacted]" }
func (Config) GoString() string                  { return "Config[redacted]" }
func (Config) Format(state fmt.State, verb rune) { io.WriteString(state, "Config[redacted]") }
func (Config) MarshalJSON() ([]byte, error)      { return []byte(`{"redacted":true}`), nil }

func (Telegram) String() string                    { return "Telegram[redacted]" }
func (Telegram) GoString() string                  { return "Telegram[redacted]" }
func (Telegram) Format(state fmt.State, verb rune) { io.WriteString(state, "Telegram[redacted]") }
func (Telegram) MarshalJSON() ([]byte, error)      { return []byte(`{"redacted":true}`), nil }
