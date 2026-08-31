package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Telegram struct {
	Token  string
	Client *http.Client
}

func (t Telegram) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+t.Token+"/"+method,
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := t.Client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("telegram status %d", response.StatusCode)
	}

	var result struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram: %s", result.Description)
	}
	return result.Result, nil
}

func (t Telegram) CreateTopic(ctx context.Context, groupID int64, name string) (int64, error) {
	result, err := t.call(ctx, "createForumTopic", map[string]any{
		"chat_id": groupID,
		"name":    name,
	})
	if err != nil {
		return 0, err
	}

	var topic struct {
		MessageThreadID int64 `json:"message_thread_id"`
	}
	if err := json.Unmarshal(result, &topic); err != nil {
		return 0, err
	}
	return topic.MessageThreadID, nil
}

func (t Telegram) SendText(ctx context.Context, groupID, threadID int64, text string) (int64, error) {
	result, err := t.call(ctx, "sendMessage", map[string]any{
		"chat_id":           groupID,
		"message_thread_id": threadID,
		"text":              text,
	})
	if err != nil {
		return 0, err
	}

	var message struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(result, &message); err != nil {
		return 0, err
	}
	return message.MessageID, nil
}
