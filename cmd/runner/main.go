// The runner drives every enabled flow on a timer through the gateway's HTTP
// API, so periodic traffic takes exactly the path a click in the UI takes.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kubeshark/demo-apps/internal/flowrun"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

const configPoll = 5 * time.Second

type config struct {
	ErrorEvery int                   `yaml:"errorEvery"`
	SlowEvery  int                   `yaml:"slowEvery"`
	Flows      map[string]flowConfig `yaml:"flows"`
}

type flowConfig struct {
	Interval time.Duration `yaml:"interval"`
}

func loadConfig(path string) (config, error) {
	var c config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, err
	}
	for name, f := range c.Flows {
		if f.Interval <= 0 {
			return c, fmt.Errorf("flow %s: interval must be positive", name)
		}
	}
	return c, nil
}

type runner struct {
	gatewayURL     string
	externalURL    string
	client         *http.Client
	externalClient *http.Client
	configPath     string

	mu      sync.Mutex
	cfg     config
	cancels map[string]context.CancelFunc
	ready   map[string]bool
	loaded  bool
}

func gatewayClient(http2 bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(!http2)
	transport.Protocols.SetUnencryptedHTTP2(http2)
	return &http.Client{Transport: transport, Timeout: 60 * time.Second}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	r := &runner{
		gatewayURL:     httpx.Env("GATEWAY_URL", "http://gateway:8080"),
		externalURL:    os.Getenv("EXTERNAL_URL"),
		client:         gatewayClient(os.Getenv("GATEWAY_HTTP2") == "true"),
		externalClient: &http.Client{Timeout: 60 * time.Second},
		configPath:     httpx.Env("RUNNER_CONFIG", "/etc/demo-apps/runner.yaml"),
		cancels:        map[string]context.CancelFunc{},
	}
	defer r.client.CloseIdleConnections()
	defer r.externalClient.CloseIdleConnections()
	health := &http.Server{
		Addr:              ":8081",
		Handler:           http.HandlerFunc(r.readiness),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := health.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("readiness", "err", err)
			stop()
		}
	}()
	defer func() {
		if err := health.Close(); err != nil {
			slog.Error("readiness shutdown", "err", err)
		}
	}()
	r.reload(ctx)
	t := time.NewTicker(configPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.reload(ctx)
		}
	}
}

// reload re-reads the config and restarts only the loops whose interval
// changed, so an edit to the ConfigMap takes effect without a restart.
func (r *runner) reload(ctx context.Context) {
	cfg, err := loadConfig(r.configPath)
	if err != nil {
		r.mu.Lock()
		r.loaded = false
		r.mu.Unlock()
		slog.Error("config", "path", r.configPath, "err", err)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ready == nil {
		r.ready = make(map[string]bool)
	}
	for name, cancel := range r.cancels {
		if nc, ok := cfg.Flows[name]; !ok || nc.Interval != r.cfg.Flows[name].Interval {
			cancel()
			delete(r.cancels, name)
			delete(r.ready, name)
		}
	}
	for name, fc := range cfg.Flows {
		if _, running := r.cancels[name]; running {
			continue
		}
		fctx, cancel := context.WithCancel(ctx)
		r.cancels[name] = cancel
		slog.Info("loop", "flow", name, "interval", fc.Interval)
		go r.loop(fctx, name, fc.Interval)
	}
	r.cfg = cfg
	r.loaded = true
}

func (r *runner) readiness(w http.ResponseWriter, _ *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.loaded || len(r.cancels) == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	for name := range r.cancels {
		if !r.ready[name] {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (r *runner) variant(n int) flowrun.Variant {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.cfg.SlowEvery > 0 && n%r.cfg.SlowEvery == 0:
		return flowrun.Slow
	case r.cfg.ErrorEvery > 0 && n%r.cfg.ErrorEvery == 0:
		return flowrun.Error
	}
	return flowrun.OK
}

func (r *runner) loop(ctx context.Context, flow string, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for n := 1; ; n++ {
		base, source, client := r.gatewayURL, "runner", r.client
		if r.externalURL != "" && n%2 == 0 {
			base, source, client = r.externalURL, "runner-external", r.externalClient
		}
		v := r.variant(n)
		url := fmt.Sprintf("%s/api/flows/%s/run?variant=%s&source=%s", base, flow, v, source)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			slog.Error("request", "flow", flow, "err", err)
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			slog.Warn("run", "flow", flow, "variant", v, "err", err)
		} else {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || readErr != nil {
				slog.Warn("run", "flow", flow, "variant", v, "status", resp.StatusCode, "body", string(body))
			} else {
				slog.Info("run", "flow", flow, "variant", v, "via", source)
				r.mu.Lock()
				if r.ready != nil && ctx.Err() == nil {
					r.ready[flow] = true
				}
				r.mu.Unlock()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
