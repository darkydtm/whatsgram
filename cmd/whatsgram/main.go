package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/darkydtm/whatsgram/internal/admin"
	"github.com/darkydtm/whatsgram/internal/config"
	"github.com/darkydtm/whatsgram/internal/coordinator"
	"github.com/darkydtm/whatsgram/internal/media"
	"github.com/darkydtm/whatsgram/internal/store"
	"github.com/darkydtm/whatsgram/internal/telegram"
	"github.com/darkydtm/whatsgram/internal/whatsapp"
)

func parseLogLevel(levelStr string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(levelStr)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func setupLogger(level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			key := strings.ToLower(a.Key)
			if strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password") {
				return slog.String(a.Key, "[redacted]")
			}
			return a
		},
	}
	handler := slog.NewJSONHandler(os.Stdout, opts)
	return slog.New(handler)
}

func main() {
	var (
		configPath string
		logLevel   string
	)

	flag.StringVar(&configPath, "config", "", "path to YAML configuration file")
	flag.StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")
	flag.Parse()

	if envLevel := os.Getenv("LOG_LEVEL"); envLevel != "" && logLevel == "info" {
		logLevel = envLevel
	}

	logger := setupLogger(parseLogLevel(logLevel))
	slog.SetDefault(logger)

	logger.Info("starting whatsgram bridge")

	if configPath == "" {
		if envConfig := os.Getenv("WHATSGRAM_CONFIG"); envConfig != "" {
			configPath = envConfig
		} else if _, err := os.Stat("config.yaml"); err == nil {
			configPath = "config.yaml"
		}
	}

	cfg, err := config.Load(configPath, nil)
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	str, err := store.Open(ctx, cfg.Database.Path, cfg.Database.BusyTimeout)
	if err != nil {
		logger.Error("failed to open database store", "error", err)
		os.Exit(1)
	}
	defer str.Close()

	ffmpeg := media.NewFFmpeg(cfg.Media.FFmpeg, cfg.Media.Timeout)
	_, err = media.NewPipeline(cfg.Media.TempDir, ffmpeg)
	if err != nil {
		logger.Error("failed to initialize media pipeline", "error", err)
		os.Exit(1)
	}

	tgClient, err := telegram.New(cfg.Telegram, nil, logger.With("component", "telegram"))
	if err != nil {
		logger.Error("failed to initialize telegram client", "error", err)
		os.Exit(1)
	}
	tgClient.BindParts(str)
	tgClient.BindPolling(str, telegram.PollOptions{LastContact: time.Now()})

	initCtx, initCancel := context.WithTimeout(ctx, cfg.Runtime.OperationTimeout)
	defer initCancel()

	if err := tgClient.Initialize(initCtx); err != nil {
		logger.Error("failed to initialize telegram bot session", "error", err)
		os.Exit(1)
	}

	botUsername := tgClient.BotUsername()
	if botUsername == "" {
		botUsername = "WhatsgramBot"
	}

	policy, err := admin.NewPolicy(cfg.Telegram.ChatID, cfg.Telegram.AllowedUserIDs, botUsername)
	if err != nil {
		logger.Error("failed to initialize admin policy", "error", err)
		os.Exit(1)
	}

	deviceStore, err := str.SessionStore().GetFirstDevice(ctx)
	if err != nil {
		logger.Error("failed to load whatsapp device session", "error", err)
		os.Exit(1)
	}
	if deviceStore == nil {
		deviceStore = str.SessionStore().NewDevice()
	}

	waOpts := whatsapp.Options{
		Logger:             logger.With("component", "whatsapp"),
		ReconnectBase:      cfg.Runtime.RetryBase,
		ReconnectMax:       cfg.Runtime.RetryMax,
		MaxConnectAttempts: cfg.Runtime.MaxAttempts,
	}

	waClient, err := whatsapp.New(ctx, deviceStore, waOpts)
	if err != nil {
		logger.Error("failed to initialize whatsapp client", "error", err)
		os.Exit(1)
	}

	coord, err := coordinator.New(
		cfg,
		str,
		tgClient,
		waClient,
		waClient,
		policy,
		coordinator.WithLeaseDuration(cfg.Runtime.OperationTimeout),
	)
	if err != nil {
		logger.Error("failed to initialize coordinator", "error", err)
		os.Exit(1)
	}

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("starting telegram runner")
		if err := tgClient.Run(ctx, str.Accept); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("telegram runner stopped with error", "error", err)
			stop()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("starting coordinator runner")
		if err := coord.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("coordinator runner stopped with error", "error", err)
			stop()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		logger.Info("starting whatsapp runner")
		for {
			if ctx.Err() != nil {
				return
			}
			if waClient.IsPaired() {
				logger.Info("whatsapp device is paired, running connection loop")
				if err := waClient.Run(ctx, str.Accept); err != nil && !errors.Is(err, context.Canceled) {
					logger.Error("whatsapp runner stopped with error", "error", err)
					stop()
				}
				return
			}
			logger.Info("whatsapp is unpaired; send /pair in telegram meta-group to pair")
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received, terminating runners...", "timeout", cfg.Runtime.ShutdownTimeout)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Runtime.ShutdownTimeout)
	defer shutdownCancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Info("all runners terminated cleanly")
	case <-shutdownCtx.Done():
		logger.Warn("shutdown deadline exceeded, forcing termination")
	}

	logger.Info("whatsgram bridge stopped")
}
