package domain

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
)

type PairingSecret struct {
	reveal func() string
}

func NewPairingSecret(value string) PairingSecret {
	return PairingSecret{reveal: func() string { return value }}
}

func (s PairingSecret) Reveal() string {
	if s.reveal == nil {
		return ""
	}
	return s.reveal()
}

func (PairingSecret) Format(s fmt.State, _ rune)   { io.WriteString(s, "[redacted]") }
func (PairingSecret) LogValue() slog.Value         { return slog.StringValue("[redacted]") }
func (PairingSecret) MarshalJSON() ([]byte, error) { return json.Marshal("[redacted]") }
func (Event) MarshalJSON() ([]byte, error)         { return json.Marshal("Event[redacted]") }
func (TextContent) MarshalJSON() ([]byte, error)   { return json.Marshal("TextContent[redacted]") }
func (Delivery) MarshalJSON() ([]byte, error)      { return json.Marshal("Delivery[redacted]") }
func (SendTextRequest) MarshalJSON() ([]byte, error) {
	return json.Marshal("SendTextRequest[redacted]")
}
func (PairingNotice) MarshalJSON() ([]byte, error)  { return json.Marshal("PairingNotice[redacted]") }
func (e AdapterError) MarshalJSON() ([]byte, error) { return json.Marshal(e.Error()) }
