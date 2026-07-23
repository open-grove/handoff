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

	"github.com/open-grove/handoff/internal/card"
	"github.com/open-grove/handoff/internal/opengroveauth"
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

	var compactor card.Compactor
	agentPlanBaseURL := envOr("ARK_AGENT_PLAN_BASE_URL", "https://ark.cn-beijing.volces.com/api/plan")
	agentPlanAPIKey := strings.TrimSpace(os.Getenv("ARK_AGENT_PLAN_API_KEY"))
	agentPlanModel := envOr("ARK_AGENT_PLAN_MODEL", "kimi-k3")
	if agentPlanAPIKey != "" {
		compactor = card.AgentPlanCompactor{BaseURL: agentPlanBaseURL, APIKey: agentPlanAPIKey, Model: agentPlanModel}
	} else {
		logger.Info("optional Agent Plan server generation disabled; normal current-Agent publishing remains available")
	}

	api := &server.API{
		Store:     store,
		Compactor: compactor,
		Token:     apiToken,
		VerifyOpenGroveUser: func(ctx context.Context, token string) (bool, error) {
			return opengroveauth.VerifyAccessToken(ctx, envOr("OPENGROVE_WW_BASE_URL", opengroveauth.DefaultWWBaseURL), token, nil)
		},
		PublicURL:  strings.TrimSpace(os.Getenv("HANDOFF_PUBLIC_URL")),
		DefaultTTL: durationEnv("HANDOFF_DEFAULT_TTL", 7*24*time.Hour),
		MaxTTL:     durationEnv("HANDOFF_MAX_TTL", 30*24*time.Hour),
		Logger:     logger,
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
	go server.RunCleanup(ctx, store, logger)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("handoffd started", "address", listenAddress, "data_dir", dataDir, "model_configured", compactor != nil)
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

func durationEnv(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
