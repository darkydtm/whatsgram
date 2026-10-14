package admin

import (
	"fmt"
	"io"
)

type PrivateValue struct {
	reveal func() string
}

func privateValue(value string) PrivateValue {
	if value == "" {
		return PrivateValue{}
	}
	return PrivateValue{reveal: func() string { return value }}
}

func (v PrivateValue) Reveal() string {
	if v.reveal == nil {
		return ""
	}
	return v.reveal()
}

func (PrivateValue) String() string   { return "PrivateValue[redacted]" }
func (PrivateValue) GoString() string { return "PrivateValue[redacted]" }
func (PrivateValue) Format(state fmt.State, verb rune) {
	io.WriteString(state, "PrivateValue[redacted]")
}
func (PrivateValue) MarshalJSON() ([]byte, error) { return []byte(`{"redacted":true}`), nil }
