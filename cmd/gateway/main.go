package main

import (
	"context"
	"embed"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/kubeshark/demo-apps/internal/flowrun"
	_ "github.com/kubeshark/demo-apps/internal/flows/all"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

//go:embed ui/*
var ui embed.FS

const runTimeout = 30 * time.Second

type gateway struct {
	flows   map[string]flowrun.Flow
	history *flowrun.History
}

func main() {
	g := &gateway{
		flows:   map[string]flowrun.Flow{},
		history: flowrun.NewHistory(10),
	}
	// A flow is constructed when the address of its first hop is configured;
	// the chart sets exactly the addresses of the enabled flows.
	for _, r := range flowrun.Registered() {
		addr := os.Getenv(r.EnvVar)
		if addr == "" {
			continue
		}
		f, err := r.New(addr)
		if err != nil {
			slog.Error("flow client", "flow", r.Name, "err", err)
			os.Exit(1)
		}
		g.add(f)
	}
	if len(g.flows) == 0 {
		slog.Error("no flow configured")
		os.Exit(2)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, ui, "ui/index.html") })
	mux.Handle("GET /ui/", http.FileServerFS(ui))
	mux.HandleFunc("GET /api/flows", g.list)
	mux.HandleFunc("GET /api/flows/{name}", g.detail)
	mux.HandleFunc("POST /api/flows/{name}/run", g.run)
	tl := httpx.TLSListener{CertFile: os.Getenv("TLS_CERT_FILE"), KeyFile: os.Getenv("TLS_KEY_FILE")}
	if tl.CertFile != "" {
		tl.Addr = httpx.Env("TLS_LISTEN_ADDR", ":8443")
	}
	if err := httpx.ServeTLS(httpx.Env("LISTEN_ADDR", ":8080"), tl, mux); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

func (g *gateway) add(f flowrun.Flow) {
	g.flows[f.Name()] = f
	slog.Info("flow registered", "flow", f.Name(), "path", f.Path())
}

type flowView struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Path        string           `json:"path"`
	Markers     int              `json:"markers"`
	Recent      []flowrun.Result `json:"recent"`
}

func (g *gateway) view(f flowrun.Flow) flowView {
	return flowView{
		Name: f.Name(), Description: f.Description(), Path: f.Path(),
		Markers: len(f.Markers().Markers), Recent: g.history.Recent(f.Name()),
	}
}

func (g *gateway) views() []flowView {
	var out []flowView
	for _, f := range g.flows {
		out = append(out, g.view(f))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (g *gateway) list(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, g.views())
}

// declared describes what a flow sends without the values themselves: the
// gateway's own responses are captured too, and a sensitive value listed here
// would show up in clear next to the traffic that redacts it.
type declared struct {
	Kind   string `json:"kind"`
	Expect string `json:"expect"`
	What   string `json:"what"`
}

type flowDetail struct {
	flowView
	Declared []declared `json:"declared"`
}

func (g *gateway) detail(w http.ResponseWriter, r *http.Request) {
	f, ok := g.flows[r.PathValue("name")]
	if !ok {
		httpx.Error(w, http.StatusNotFound, "no such flow")
		return
	}
	d := flowDetail{flowView: g.view(f)}
	for _, m := range f.Markers().Markers {
		d.Declared = append(d.Declared, declared{Kind: string(m.Kind), Expect: string(m.Expect), What: m.What})
	}
	httpx.JSON(w, http.StatusOK, d)
}

func (g *gateway) run(w http.ResponseWriter, r *http.Request) {
	f, ok := g.flows[r.PathValue("name")]
	if !ok {
		httpx.Error(w, http.StatusNotFound, "no such flow")
		return
	}
	v, err := flowrun.ParseVariant(r.URL.Query().Get("variant"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "ui"
	}
	ctx, cancel := context.WithTimeout(r.Context(), runTimeout)
	defer cancel()
	start := time.Now()
	steps, runErr := f.Run(ctx, v)
	res := flowrun.Result{Flow: f.Name(), Variant: v, Source: source, Started: start, Duration: time.Since(start), Steps: steps}
	if runErr != nil {
		res.Err = runErr.Error()
	}
	g.history.Add(res)
	slog.Info("flow run", "flow", f.Name(), "variant", v, "source", source, "duration", res.Duration, "err", res.Err)
	status := http.StatusOK
	if runErr != nil {
		status = http.StatusBadGateway
	}
	httpx.JSON(w, status, res)
}
