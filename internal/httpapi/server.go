package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
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
	return mux
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

	digest := sha256.Sum256(body)
	externalID := hex.EncodeToString(digest[:])
	if err := s.Store.PutInbox(r.Context(), "whatsapp", externalID, body); err != nil {
		http.Error(w, "storage error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
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
