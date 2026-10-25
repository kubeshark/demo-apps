package main

import (
	"log/slog"
	"os"

	"github.com/kubeshark/demo-apps/internal/flows/telco"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	svc := telco.NewService(telco.ServiceConfig{
		DiameterAddr: httpx.Env("DIAMETER_ADDR", "telco-server:3868"),
		EchoAddr:     httpx.Env("SCTP_ECHO_ADDR", "telco-server:3869"),
	})
	if err := httpx.Serve(httpx.Env("LISTEN_ADDR", ":8080"), svc.Handler()); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
