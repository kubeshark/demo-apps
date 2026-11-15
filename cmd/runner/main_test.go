package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
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

func TestRunnerReadinessRequiresEveryFlowAndResetsOnReload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var inventoryOK atomic.Bool
	var inventoryRuns atomic.Int64
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/flows/inventory/run" {
			inventoryRuns.Add(1)
			if !inventoryOK.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()
	configPath := filepath.Join(t.TempDir(), "runner.yaml")
	writeConfig := func(text string) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r := &runner{
		gatewayURL: gateway.URL,
		client:     gateway.Client(),
		configPath: configPath,
		cancels:    map[string]context.CancelFunc{},
	}
	status := func() int {
		rec := httptest.NewRecorder()
		r.readiness(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
		return rec.Code
	}
	await := func(predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				t.Fatal("runner condition did not become true")
			}
			time.Sleep(time.Millisecond)
		}
	}
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("initial readiness = %d", got)
	}
	writeConfig("flows:\n  catalog: {interval: 10ms}\n  inventory: {interval: 10ms}\n")
	r.reload(ctx)
	await(func() bool { return inventoryRuns.Load() >= 2 })
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("readiness before inventory success = %d", got)
	}
	inventoryOK.Store(true)
	await(func() bool { return status() == http.StatusOK })
	inventoryOK.Store(false)
	writeConfig("flows:\n  catalog: {interval: 10ms}\n  inventory: {interval: 20ms}\n")
	r.reload(ctx)
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("readiness after changed loop = %d", got)
	}
	inventoryOK.Store(true)
	await(func() bool { return status() == http.StatusOK })
	writeConfig("flows: invalid")
	r.reload(ctx)
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("readiness with invalid config = %d", got)
	}
	writeConfig("flows: {}")
	r.reload(ctx)
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("readiness without flows = %d", got)
	}
}
