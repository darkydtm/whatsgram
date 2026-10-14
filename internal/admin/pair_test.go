package admin

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPairingParsesOnlyQRorInternationalDigits(t *testing.T) {
	p, _ := NewPolicy(-100123, []int64{42}, "BridgeBot")
	qr := p.Decide(commandInput("/pair qr"))
	if qr.Kind != Command || qr.Command.Name != "pair" || qr.Command.Mode != "qr" || qr.Command.Argument.Reveal() != "" {
		t.Fatalf("QR intent: %v", qr)
	}
	phone := "15551234567"
	code := p.Decide(commandInput("/pair code " + phone))
	if code.Kind != Command || code.Command.Mode != "code" || code.Command.Argument.Reveal() != phone {
		t.Fatalf("code intent: %v", code)
	}
	for _, text := range []string{"/pair", "/pair QR", "/pair qr extra", "/pair code", "/pair code +15551234567", "/pair code 05551234567", "/pair code 123456", "/pair code 1234567890123456", "/pair code １２３４５６７", "/pair code 15551234567 extra"} {
		if got := p.Decide(commandInput(text)); got.Kind != Reject {
			t.Fatalf("invalid pairing: %v", got)
		}
	}
	for _, digits := range []string{"1234567", "123456789012345"} {
		if got := p.Decide(commandInput("/pair code " + digits)); got.Kind != Command {
			t.Fatalf("phone boundary: %v", got)
		}
	}
	assertPrivateFormatting(t, phone, code.Command.Argument, &code.Command.Argument, code.Command, &code.Command, code, &code)
	var zero PrivateValue
	if zero.Reveal() != "" {
		t.Fatal("zero private value")
	}
}

func assertPrivateFormatting(t *testing.T, private string, values ...any) {
	t.Helper()
	for _, value := range values {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%T", "%w", "%z", "%#z", "%+z"} {
			if output := fmt.Sprintf(format, value); strings.Contains(output, private) {
				t.Fatalf("private argument exposed by %s", format)
			}
		}
		output, err := json.Marshal(value)
		if err != nil || strings.Contains(string(output), private) {
			t.Fatal("private argument JSON exposure or error")
		}
	}
}
