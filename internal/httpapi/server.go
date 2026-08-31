package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/darky/whatsgram/internal/config"
	"github.com/darky/whatsgram/internal/provider"
	"github.com/darky/whatsgram/internal/store"
)

type Server struct {
	Config   config.Config
	Store    *store.Store
	WhatsApp provider.WhatsApp
	Telegram provider.Telegram
}

func (s Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /webhooks/whatsapp", s.verifyWhatsApp)
	mux.HandleFunc("POST /webhooks/whatsapp", s.whatsAppWebhook)
	mux.HandleFunc("POST /webhooks/telegram", s.telegramWebhook)
	mux.HandleFunc("GET /api/chats", s.listChats)
	mux.HandleFunc("GET /api/messages", s.listMessages)
	mux.HandleFunc("POST /api/commands", s.createCommand)
	mux.HandleFunc("GET /api/deliveries/{id}", s.getDelivery)
	return mux
}

func (s Server) listChats(w http.ResponseWriter, r *http.Request) {
	chats, err := s.Store.ListChats(r.Context())
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, chats)
}

func (s Server) listMessages(w http.ResponseWriter, r *http.Request) {
	chatID, err := strconv.ParseInt(r.URL.Query().Get("chat_id"), 10, 64)
	if err != nil || chatID <= 0 {
		http.Error(w, "chat_id is required", http.StatusBadRequest)
		return
	}
	messages, err := s.Store.ListMessages(r.Context(), chatID)
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, messages)
}

type commandRequest struct {
	ChatID  int64  `json:"chat_id"`
	Body    string `json:"body"`
	Command string `json:"command"`
}

func (s Server) createCommand(w http.ResponseWriter, r *http.Request) {
	var request commandRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&request); err != nil || request.ChatID <= 0 || strings.TrimSpace(request.Body) == "" {
		http.Error(w, "chat_id and body are required", http.StatusBadRequest)
		return
	}
	recipient, err := s.Store.ChatTarget(r.Context(), request.ChatID)
	if err != nil {
		http.Error(w, "chat not found", http.StatusNotFound)
		return
	}
	delivery := map[string]string{"to": recipient, "body": request.Body}
	if err := s.Store.CreateOutbox(r.Context(), "whatsapp", &request.ChatID, delivery); err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s Server) getDelivery(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid delivery id", http.StatusBadRequest)
		return
	}
	delivery, err := s.Store.GetDelivery(r.Context(), id)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, delivery)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (s Server) live(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DB.PingContext(r.Context()); err != nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s Server) verifyWhatsApp(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("hub.verify_token") != s.Config.WhatsAppVerifyToken {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(r.URL.Query().Get("hub.challenge")))
}

func (s Server) validSignature(body []byte, header string) bool {
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}

	provided, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}

	digest := hmac.New(sha256.New, []byte(s.Config.WhatsAppAppSecret))
	_, _ = digest.Write(body)
	return hmac.Equal(provided, digest.Sum(nil))
}

func (s Server) whatsAppWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !s.validSignature(body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !json.Valid(body) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	externalID := whatsappEventID(body)
	if err := s.Store.PutInbox(r.Context(), "whatsapp", externalID, body); err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func whatsappEventID(body []byte) string {
	var event struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Messages []struct{ ID string `json:"id"` } `json:"messages"`
					Statuses []struct{ ID string `json:"id"` } `json:"statuses"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if json.Unmarshal(body, &event) == nil {
		for _, entry := range event.Entry {
			for _, change := range entry.Changes {
				if len(change.Value.Messages) > 0 && change.Value.Messages[0].ID != "" {
					return change.Value.Messages[0].ID
				}
				if len(change.Value.Statuses) > 0 && change.Value.Statuses[0].ID != "" {
					return change.Value.Statuses[0].ID + ":status"
				}
			}
		}
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func (s Server) telegramWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != s.Config.TelegramWebhookSecret {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var update telegramUpdate
	if err := json.Unmarshal(body, &update); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if update.Message == nil || !s.authorized(update.Message) {
		w.WriteHeader(http.StatusOK)
		return
	}

	externalID := strconv.FormatInt(update.UpdateID, 10)
	if err := s.Store.PutInbox(r.Context(), "telegram", externalID, body); err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s Server) authorized(message *telegramMessage) bool {
	return message.Chat.ID == s.Config.TelegramGroupID &&
		s.Config.TelegramAllowedUserIDs[message.From.ID]
}

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

type telegramMessage struct {
	Chat struct {
		ID int64 `json:"id"`
	} `json:"chat"`
	From struct {
		ID int64 `json:"id"`
	} `json:"from"`
	MessageThreadID int64 `json:"message_thread_id"`
	Text            string `json:"text"`
	ReplyToMessage  *struct {
		MessageID int64 `json:"message_id"`
	} `json:"reply_to_message"`
}
