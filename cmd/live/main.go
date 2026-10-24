package main

import (
	"log/slog"
	"os"

	"github.com/kubeshark/demo-apps/internal/flows/websocket"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	if err := httpx.Serve(httpx.Env("LISTEN_ADDR", ":8080"), websocket.NewService().Handler()); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
