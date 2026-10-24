package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kubeshark/demo-apps/internal/flows/telco"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	if err := run(); err != nil {
		slog.Error("telco-server", "err", err)
		os.Exit(1)
	}
}

// The Diameter listener on TCP is the one every environment has; the SCTP
// listeners need the kernel's sctp module on the node, so their failure is
// logged and the process keeps serving TCP.
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	srv := telco.NewServer()
	diameterTCP := httpx.Env("DIAMETER_TCP_ADDR", ":3868")
	diameterSCTP := httpx.Env("DIAMETER_SCTP_ADDR", ":3868")
	echo := httpx.Env("SCTP_ECHO_ADDR", ":3869")

	fatal := make(chan error, 1)
	go func() { fatal <- srv.ServeDiameter("tcp", diameterTCP) }()
	go func() {
		if err := srv.ServeDiameter("sctp", diameterSCTP); err != nil {
			slog.Error("diameter over sctp unavailable", "addr", diameterSCTP, "err", err)
		}
	}()
	go func() {
		if err := srv.ServeEcho(ctx, echo); err != nil {
			slog.Error("sctp echo unavailable", "addr", echo, "err", err)
		}
	}()
	select {
	case err := <-fatal:
		return err
	case <-ctx.Done():
		return nil
	}
}
