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

type File struct {
	ID   string `json:"file_id"`
	Path string `json:"file_path"`
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

func (t Telegram) SendMedia(ctx context.Context, groupID, threadID int64, mediaType, fileID, caption string) (int64, error) {
	result, err := t.call(ctx, "send"+mediaType, map[string]any{
		"chat_id":           groupID,
		"message_thread_id": threadID,
		mediaType:            fileID,
		"caption":            caption,
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

func (t Telegram) GetFile(ctx context.Context, fileID string) (File, error) {
	result, err := t.call(ctx, "getFile", map[string]string{"file_id": fileID})
	if err != nil {
		return File{}, err
	}
	var file File
	if err := json.Unmarshal(result, &file); err != nil {
		return File{}, err
	}
	return file, nil
}

func (t Telegram) DownloadFile(ctx context.Context, path string) (io.ReadCloser, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.telegram.org/file/bot"+t.Token+"/"+path, nil)
	if err != nil {
		return nil, err
	}
	response, err := t.Client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= http.StatusMultipleChoices {
		response.Body.Close()
		return nil, fmt.Errorf("telegram file status %d", response.StatusCode)
	}
	return response.Body, nil
}
