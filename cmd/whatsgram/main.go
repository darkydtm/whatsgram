package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/darky/whatsgram/internal/config"
	"github.com/darky/whatsgram/internal/httpapi"
	"github.com/darky/whatsgram/internal/provider"
	"github.com/darky/whatsgram/internal/store"
	"github.com/darky/whatsgram/internal/worker"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		log.Fatal(err)
	}

	client := http.DefaultClient
	whatsapp := provider.WhatsApp{
		Token:   cfg.WhatsAppAccessToken,
		PhoneID: cfg.WhatsAppPhoneNumberID,
		Version: cfg.WhatsAppAPIVersion,
		Client:  client,
	}
	telegram := provider.Telegram{Token: cfg.TelegramBotToken, Client: client}

	go (worker.Worker{
		Store:    st,
		WhatsApp: whatsapp,
		Telegram: telegram,
		GroupID:  cfg.TelegramGroupID,
	}).Run(ctx)

	server := &http.Server{Addr: cfg.HTTPAddr, Handler: (httpapi.Server{
		Config:   cfg,
		Store:    st,
		WhatsApp: whatsapp,
		Telegram: telegram,
	}).Routes()}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("listening on %s", cfg.HTTPAddr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Println(err)
	}
}
