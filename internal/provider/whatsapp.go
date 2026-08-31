package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type WhatsApp struct {
	Token   string
	PhoneID string
	Version string
	Client  *http.Client
}

func (w WhatsApp) SendText(ctx context.Context, recipient, body string) error {
	payload := map[string]any{
		"messaging_product": "whatsapp",
		"to":                recipient,
		"type":              "text",
		"text":              map[string]string{"body": body},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("https://graph.facebook.com/%s/%s/messages", w.Version, w.PhoneID),
		bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+w.Token)
	request.Header.Set("Content-Type", "application/json")

	response, err := w.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("whatsapp status %d: %s", response.StatusCode, detail)
	}
	return nil
}
