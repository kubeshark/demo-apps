package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/kubeshark/demo-apps/internal/flows/netprobe"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

func main() {
	svc := netprobe.NewService(netprobe.ServiceConfig{
		DNSAddr:     httpx.MustEnv("DNS_ADDR"),
		Zone:        httpx.Env("DNS_ZONE", "demo.local"),
		PingTargets: list(httpx.MustEnv("PING_TARGETS")),
		EgressURLs:  list(os.Getenv("EGRESS_URLS")),
	})
	if err := httpx.Serve(httpx.Env("LISTEN_ADDR", ":8080"), svc.Handler()); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

func list(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
