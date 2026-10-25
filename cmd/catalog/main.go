package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/kubeshark/demo-apps/internal/flows/catalog"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	ctx := context.Background()
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true",
		httpx.Env("MYSQL_USER", "catalog"), httpx.MustEnv("MYSQL_PASSWORD"),
		httpx.Env("MYSQL_ADDR", "mysql:3306"), httpx.Env("MYSQL_DATABASE", "catalog"))
	svc, err := catalog.NewService(ctx, catalog.ServiceConfig{
		MySQLDSN:      dsn,
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
