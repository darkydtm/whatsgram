package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
)

type WhatsApp struct {
	Token   string
	PhoneID string
	Version string
	Client  *http.Client
}

func (w WhatsApp) send(ctx context.Context, payload any) error {
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

func (w WhatsApp) SendText(ctx context.Context, recipient, body string) error {
	return w.send(ctx, map[string]any{
		"messaging_product": "whatsapp",
		"to":                recipient,
		"type":              "text",
		"text":              map[string]string{"body": body},
	})
}

func (w WhatsApp) SendMedia(ctx context.Context, recipient, mediaType, mediaID, caption string) error {
	if !supportedMediaType(mediaType) {
		return fmt.Errorf("unsupported WhatsApp media type %q", mediaType)
	}
	return w.send(ctx, map[string]any{
		"messaging_product": "whatsapp",
		"to":                recipient,
		"type":              mediaType,
		mediaType: map[string]string{
			"id":      mediaID,
			"caption": caption,
		},
	})
}

func (w WhatsApp) UploadMedia(ctx context.Context, mediaType string, content io.Reader) (string, error) {
	if !supportedMediaType(mediaType) {
		return "", fmt.Errorf("unsupported WhatsApp media type %q", mediaType)
	}
	file, err := os.CreateTemp("", "whatsgram-media-*")
	if err != nil {
		return "", err
	}
	name := file.Name()
	defer os.Remove(name)
	defer file.Close()
	if _, err := io.Copy(file, content); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "media")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	_ = writer.WriteField("messaging_product", "whatsapp")
	_ = writer.WriteField("type", mediaType)
	if err := writer.Close(); err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("https://graph.facebook.com/%s/%s/media", w.Version, w.PhoneID), &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+w.Token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := w.Client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("whatsapp upload status %d", response.StatusCode)
	}
	var result struct { ID string `json:"id"` }
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.ID == "" {
		return "", fmt.Errorf("whatsapp upload returned no media id")
	}
	return result.ID, nil
}


func supportedMediaType(mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "image", "document", "video", "audio" :
		return true
	default:
		return false
	}
}
