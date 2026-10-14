package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestNestedJSONBoundaryRedactsPrivateValues(t *testing.T) {
	canary := "SYNTHETIC-PRIVATE-BOUNDARY"
	values := []any{PairingNotice{Secret: NewPairingSecret(canary)}, Event{Text: &TextContent{Text: canary}, Raw: []byte(canary)}, TextContent{Text: canary}, Delivery{Text: &TextContent{Text: canary}}, SendTextRequest{Content: TextContent{Text: canary}}, AdapterError{Code: canary, Cause: errors.New(canary)}}
	for _, value := range values {
		for _, nested := range []any{value, []any{value}, map[string]any{"value": value}, struct{ Value any }{value}} {
			encoded, err := json.Marshal(nested)
			if err != nil || bytes.Contains(encoded, []byte(canary)) {
				t.Errorf("private JSON boundary failed for %T, error=%v", nested, err)
			}
			var out bytes.Buffer
			slog.New(slog.NewJSONHandler(&out, nil)).Info("boundary", "value", nested)
			if bytes.Contains(out.Bytes(), []byte(canary)) {
				t.Errorf("nested structured log leaked %T", nested)
			}
		}
	}
}

func TestPairingDiagnosticsCannotExposeSecret(t *testing.T) {
	canary := "SYNTHETIC-PAIRING-BOUNDARY"
	p := PairingNotice{Secret: NewPairingSecret(canary)}
	copy := p.Secret
	var zero PairingSecret
	if copy.Reveal() != canary || zero.Reveal() != "" {
		t.Fatal("pairing secret lost exact bytes or unsafe zero value")
	}
	for _, value := range []any{p, &p, []PairingNotice{p}, []*PairingNotice{&p}, []any{p}, map[string]any{"value": p}, struct{ Value any }{&p}} {
		for _, verb := range []string{"v", "+v", "#v", "w", "p"} {
			format := "%" + verb
			for _, output := range []string{fmt.Sprintf(format, value), fmt.Errorf(format, value).Error()} {
				if strings.Contains(output, canary) {
					t.Errorf("pairing diagnostic leaked %T verb=%s", value, verb)
				}
			}
		}
	}
}

func TestMalformedMessageDiagnosticIsOutsideJSONBoundary(t *testing.T) {
	canary := "SYNTHETIC-RAW-TEXT-BOUNDARY"
	format := "%" + "w"
	if !strings.Contains(fmt.Sprintf(format, TextContent{Text: canary}), canary) {
		t.Fatal("raw-field diagnostic characterization changed; reconsider the boundary")
	}
}
