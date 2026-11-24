package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/kubeshark/demo-apps/internal/flows/wallet"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	svc, err := wallet.NewService(context.Background(), wallet.ServiceConfig{
		RedisAddr:     httpx.Env("REDIS_ADDR", "redis:6379"),
		RedisPassword: httpx.MustEnv("REDIS_PASSWORD"),
	})
	if err != nil {
		slog.Error("startup", "err", err)
		os.Exit(1)
	}
	if err := httpx.Serve(httpx.Env("LISTEN_ADDR", ":8080"), svc.Handler()); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
