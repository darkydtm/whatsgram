package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type Telegram struct {
	Token  string
	Client *http.Client
}

var telegramRateLimit = struct {
	sync.Mutex
	next time.Time
}{}

type File struct {
	ID   string `json:"file_id"`
	Path string `json:"file_path"`
}

func (t Telegram) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	telegramRateLimit.Lock()
	defer telegramRateLimit.Unlock()
	if wait := time.Until(telegramRateLimit.next); wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	telegramRateLimit.next = time.Now().Add(1100 * time.Millisecond)

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
	var result struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		if response.StatusCode >= http.StatusMultipleChoices {
			return nil, fmt.Errorf("telegram status %d", response.StatusCode)
		}
		return nil, err
	}
	if response.StatusCode >= http.StatusMultipleChoices && result.OK {
		return nil, fmt.Errorf("telegram status %d", response.StatusCode)
	}
	if !result.OK {
		if result.Parameters.RetryAfter > 0 {
			telegramRateLimit.next = time.Now().Add(time.Duration(result.Parameters.RetryAfter) * time.Second)
			return nil, fmt.Errorf("telegram: %s (retry after %ds)", result.Description, result.Parameters.RetryAfter)
		}
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
		mediaType:           fileID,
		"caption":           caption,
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

func (t Telegram) EditText(ctx context.Context, groupID, messageID int64, text string) error {
	_, err := t.call(ctx, "editMessageText", map[string]any{
		"chat_id": groupID, "message_id": messageID, "text": text,
	})
	return err
}

func (t Telegram) DeleteMessage(ctx context.Context, groupID, messageID int64) error {
	_, err := t.call(ctx, "deleteMessage", map[string]any{
		"chat_id": groupID, "message_id": messageID,
	})
	return err
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
