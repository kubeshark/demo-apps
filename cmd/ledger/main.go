package main

import (
	"context"
	"log/slog"
	"net/url"
	"os"

	"github.com/kubeshark/demo-apps/internal/flows/ledger"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	ctx := context.Background()
	// encrypt=disable keeps TDS in plaintext so the bytes are visible on the wire.
	dsn := url.URL{
		Scheme:   "sqlserver",
		User:     url.UserPassword(httpx.Env("MSSQL_USER", "sa"), httpx.MustEnv("MSSQL_PASSWORD")),
		Host:     httpx.Env("MSSQL_ADDR", "mssql:1433"),
		RawQuery: "encrypt=disable",
	}
	svc, err := ledger.NewService(ctx, dsn.String())
	if err != nil {
		slog.Error("startup", "err", err)
		os.Exit(1)
	}
	if err := httpx.Serve(httpx.Env("LISTEN_ADDR", ":8080"), svc.Handler()); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
