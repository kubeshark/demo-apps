package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/kubeshark/demo-apps/internal/flows/orders"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	if err := run(); err != nil {
		slog.Error("notifier", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	amqpURL := fmt.Sprintf("amqp://%s:%s@%s/",
		url.QueryEscape(httpx.Env("RABBITMQ_USER", "demo")), url.QueryEscape(httpx.MustEnv("RABBITMQ_PASSWORD")),
		httpx.Env("RABBITMQ_ADDR", "rabbitmq:5672"))
	n, err := orders.NewNotifier(ctx, orders.NotifierConfig{
		Kafka: orders.KafkaConfig{
			Brokers:  strings.Split(httpx.Env("KAFKA_BROKERS", "kafka:9092"), ","),
			User:     httpx.Env("KAFKA_USER", "demo"),
			Password: httpx.MustEnv("KAFKA_PASSWORD"),
			Pinned:   httpx.Env("KAFKA_PIN_VERSIONS", "true") == "true",
		},
		AMQPURL: amqpURL,
	})
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() { errc <- n.Run(ctx) }()
	go func() { errc <- httpx.Serve(httpx.Env("LISTEN_ADDR", ":8080"), n.Handler()) }()
	err = <-errc
	stop()
	return err
}
