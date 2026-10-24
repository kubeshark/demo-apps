package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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

	servers := []*grpc.Server{grpc.NewServer()}
	listeners := []string{httpx.Env("LISTEN_ADDR", ":9090")}
	if cert := os.Getenv("TLS_CERT_FILE"); cert != "" {
		creds, err := credentials.NewServerTLSFromFile(cert, os.Getenv("TLS_KEY_FILE"))
		if err != nil {
			return err
		}
		servers = append(servers, grpc.NewServer(grpc.Creds(creds)))
		listeners = append(listeners, httpx.Env("TLS_LISTEN_ADDR", ":9443"))
	}

	errc := make(chan error, len(servers))
	for i, srv := range servers {
		svc.Register(srv)
		healthv1.RegisterHealthServer(srv, health.NewServer())
		lis, err := net.Listen("tcp", listeners[i])
		if err != nil {
			return err
		}
		slog.Info("listening", "addr", listeners[i], "tls", i > 0)
		go func() { errc <- srv.Serve(lis) }()
	}
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	for _, srv := range servers {
		srv.GracefulStop()
	}
	return nil
}
