package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/kubeshark/demo-apps/internal/flows/orders"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	ctx := context.Background()
	pgURL := fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=disable",
		url.QueryEscape(httpx.Env("POSTGRES_USER", "demo")), url.QueryEscape(httpx.MustEnv("POSTGRES_PASSWORD")),
		httpx.Env("POSTGRES_ADDR", "postgresql:5432"), httpx.Env("POSTGRES_DB", "demo"))
	svc, err := orders.NewService(ctx, orders.ServiceConfig{
		PostgresURL: pgURL,
		Kafka: orders.KafkaConfig{
			Brokers:  strings.Split(httpx.Env("KAFKA_BROKERS", "kafka:9092"), ","),
			User:     httpx.Env("KAFKA_USER", "demo"),
			Password: httpx.MustEnv("KAFKA_PASSWORD"),
			Pinned:   httpx.Env("KAFKA_PIN_VERSIONS", "true") == "true",
		},
		NotifierURL: httpx.Env("NOTIFIER_URL", "http://notifier:8080"),
	})
	if err != nil {
		slog.Error("startup", "err", err)
		os.Exit(1)
	}
	err = httpx.Serve(httpx.Env("LISTEN_ADDR", ":8080"), svc.Handler())
	svc.Close()
	if err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
