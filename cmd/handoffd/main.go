package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/open-grove/handoff/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	listenAddress := envOr("HANDOFF_LISTEN_ADDR", ":7391")
	dataDir := envOr("HANDOFF_DATA_DIR", "./data")
	apiToken := strings.TrimSpace(os.Getenv("HANDOFF_API_TOKEN"))
	store, err := server.NewStore(dataDir)
	if err != nil {
		logger.Error("initialize store", "error", err)
		os.Exit(1)
	}

	api := &server.API{
		Store:     store,
		Token:     apiToken,
		PublicURL: strings.TrimSpace(os.Getenv("HANDOFF_PUBLIC_URL")),
		Logger:    logger,
	}
	httpServer := &http.Server{
		Addr:              listenAddress,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    32 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("handoffd started", "address", listenAddress, "data_dir", dataDir)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve", "error", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
