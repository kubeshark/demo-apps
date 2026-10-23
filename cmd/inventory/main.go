package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/kubeshark/demo-apps/internal/flows/inventory"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	if err := run(); err != nil {
		slog.Error("inventory", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	svc, err := inventory.NewService(ctx, httpx.MustEnv("MONGODB_URI"))
	if err != nil {
		return err
	}
	addr := httpx.Env("LISTEN_ADDR", ":9090")
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := grpc.NewServer()
	svc.Register(srv)
	healthv1.RegisterHealthServer(srv, health.NewServer())
	go func() {
		<-ctx.Done()
		srv.GracefulStop()
	}()
	slog.Info("listening", "addr", addr)
	return srv.Serve(lis)
}
