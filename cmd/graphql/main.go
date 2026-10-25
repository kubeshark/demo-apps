package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"

	"github.com/kubeshark/demo-apps/internal/flows/graphql"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	ctx := context.Background()
	// sslmode=disable: the dissector decodes the plaintext protocol only.
	dsn := fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable",
		url.PathEscape(httpx.Env("POSTGRES_USER", "demo")), url.PathEscape(httpx.MustEnv("POSTGRES_PASSWORD")),
		httpx.Env("POSTGRES_ADDR", "postgresql:5432"), httpx.Env("POSTGRES_DATABASE", "demo"))
	svc, err := graphql.NewService(ctx, dsn)
	if err != nil {
		slog.Error("startup", "err", err)
		os.Exit(1)
	}
	if err := httpx.Serve(httpx.Env("LISTEN_ADDR", ":8080"), svc.Handler()); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
