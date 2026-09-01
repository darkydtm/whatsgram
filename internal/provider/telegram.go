package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
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

const telegramRequestInterval = 3200 * time.Millisecond

type File struct {
	ID   string `json:"file_id"`
	Path string `json:"file_path"`
}

type Media struct {
	Kind     string
	Mimetype string
	Filename string
	Caption  string
	Data     []byte
}

var telegramUploads = map[string]struct{ method, field string }{
	"image":      {"sendPhoto", "photo"},
	"video":      {"sendVideo", "video"},
	"animation":  {"sendAnimation", "animation"},
	"video_note": {"sendVideoNote", "video_note"},
	"audio":      {"sendAudio", "audio"},
	"voice":      {"sendVoice", "voice"},
	"document":   {"sendDocument", "document"},
	"sticker":    {"sendSticker", "sticker"},
}

const TelegramUploadLimit = 50 << 20

// SupportsCaption reports whether Telegram accepts a caption for the media kind.
func SupportsCaption(kind string) bool {
	return kind != "sticker" && kind != "video_note"
}

func (t Telegram) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return t.post(ctx, method, "application/json", body)
}

func (t Telegram) post(ctx context.Context, method, contentType string, body []byte) (json.RawMessage, error) {
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
	telegramRateLimit.next = time.Now().Add(telegramRequestInterval)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+t.Token+"/"+method,
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", contentType)

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

func (t Telegram) SetWebhook(ctx context.Context, url, secret string) error {
	_, err := t.call(ctx, "setWebhook", map[string]any{
		"url":             url,
		"secret_token":    secret,
		"allowed_updates": []string{"message", "edited_message"},
	})
	return err
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

func (t Telegram) SendMedia(ctx context.Context, groupID, threadID int64, media Media) (int64, error) {
	upload, ok := telegramUploads[media.Kind]
	if !ok {
		return 0, fmt.Errorf("unsupported Telegram media kind %q", media.Kind)
	}
	if len(media.Data) > TelegramUploadLimit {
		return 0, fmt.Errorf("media exceeds Telegram upload limit: %d bytes", len(media.Data))
	}
	body := &bytes.Buffer{}
	form := multipart.NewWriter(body)
	fields := map[string]string{"chat_id": strconv.FormatInt(groupID, 10)}
	if threadID != 0 {
		fields["message_thread_id"] = strconv.FormatInt(threadID, 10)
	}
	if media.Caption != "" && SupportsCaption(media.Kind) {
		fields["caption"] = media.Caption
	}
	for name, value := range fields {
		if err := form.WriteField(name, value); err != nil {
			return 0, err
		}
	}
	part, err := form.CreateFormFile(upload.field, mediaFilename(media))
	if err != nil {
		return 0, err
	}
	if _, err := part.Write(media.Data); err != nil {
		return 0, err
	}
	if err := form.Close(); err != nil {
		return 0, err
	}
	result, err := t.post(ctx, upload.method, form.FormDataContentType(), body.Bytes())
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

func mediaFilename(media Media) string {
	if media.Filename != "" {
		return media.Filename
	}
	return "file" + defaultExtensions[media.Kind]
}

var defaultExtensions = map[string]string{
	"image": ".jpg", "sticker": ".webp", "video": ".mp4", "animation": ".mp4",
	"video_note": ".mp4", "audio": ".mp3", "voice": ".ogg", "document": ".bin",
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
