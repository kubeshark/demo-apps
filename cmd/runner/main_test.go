package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunnerGatewayProtocol(t *testing.T) {
	for _, tc := range []struct {
		name  string
		http2 bool
		proto int
	}{
		{name: "default HTTP1", proto: 1},
		{name: "prior knowledge HTTP2", http2: true, proto: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			type observation struct {
				external bool
				proto    int
				method   string
				path     string
			}
			requests := make(chan observation, 2)
			handler := func(external bool) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					select {
					case requests <- observation{external, r.ProtoMajor, r.Method, r.URL.Path}:
					case <-ctx.Done():
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
				})
			}
			gateway := httptest.NewUnstartedServer(handler(false))
			gateway.Config.Protocols = new(http.Protocols)
			gateway.Config.Protocols.SetHTTP1(true)
			gateway.Config.Protocols.SetUnencryptedHTTP2(true)
			gateway.Start()
			defer gateway.Close()
			external := httptest.NewTLSServer(handler(true))
			defer external.Close()
			r := &runner{
				gatewayURL:     gateway.URL,
				externalURL:    external.URL,
				client:         gatewayClient(tc.http2),
				externalClient: external.Client(),
			}
			defer r.client.CloseIdleConnections()
			defer r.externalClient.CloseIdleConnections()
			done := make(chan struct{})
			go func() {
				defer close(done)
				r.loop(ctx, "catalog", time.Millisecond)
			}()
			for i := range 2 {
				select {
				case got := <-requests:
					wantProto := tc.proto
					if i == 1 {
						wantProto = 1
					}
					if got.external != (i == 1) || got.proto != wantProto || got.method != http.MethodPost || got.path != "/api/flows/catalog/run" {
						t.Errorf("request %d = %+v, want external=%t HTTP/%d POST /api/flows/catalog/run", i, got, i == 1, wantProto)
					}
				case <-ctx.Done():
					t.Fatal("runner did not reach both gateway and external HTTPS endpoint")
				}
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("runner did not stop after context cancellation")
			}
		})
	}
}
