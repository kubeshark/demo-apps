package netprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kubeshark/demo-apps/internal/flowrun"
	"github.com/kubeshark/demo-apps/internal/markers"
)

// Flow is the gateway's side of the path: one HTTP/1.1 call to the netprobe
// service, whose report lists the dns, icmp and https hops it made.
type Flow struct {
	BaseURL string
	Client  *http.Client
}

func NewFlow(baseURL string) *Flow {
	return &Flow{BaseURL: baseURL, Client: &http.Client{Timeout: 25 * time.Second}}
}

func init() {
	flowrun.Register(flowrun.Registration{
		Name:   Name,
		EnvVar: "NETPROBE_URL",
		New:    func(addr string) (flowrun.Flow, error) { return NewFlow(addr), nil },
	})
}

func (f *Flow) Name() string { return Name }
func (f *Flow) Description() string {
	return "DNS lookups in a private zone, ICMP echoes and HTTPS egress, reported by the netprobe service"
}
func (f *Flow) Path() string {
	return "gateway -> netprobe (http) -> coredns (dns), icmp, https egress"
}
func (f *Flow) Markers() *markers.Set {
	return Markers
}

func (f *Flow) Run(ctx context.Context, v flowrun.Variant) ([]flowrun.Step, error) {
	want := http.StatusOK
	if v == flowrun.Error {
		want = http.StatusBadGateway
	}
	t := &flowrun.Trace{}
	var report Report
	err := t.Step("probe", f.BaseURL, "http", func() (string, error) {
		url := f.BaseURL + "/probe?variant=" + string(v)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set(ProbeHeader, Markers.One("PROBE"))
		resp, err := f.Client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if err := json.Unmarshal(body, &report); err != nil {
			return "", fmt.Errorf("POST %s: status %d: %w", url, resp.StatusCode, err)
		}
		if resp.StatusCode != want {
			return "", fmt.Errorf("POST %s: status %d, want %d", url, resp.StatusCode, want)
		}
		if want != http.StatusOK {
			return fmt.Sprintf("status %d as designed, %d probes", resp.StatusCode, len(report.Steps)), nil
		}
		return fmt.Sprintf("status %d, %d probes", resp.StatusCode, len(report.Steps)), nil
	})
	steps := append(t.Steps(), report.Steps...)
	for _, s := range report.Steps {
		if err == nil && s.Err != "" {
			err = fmt.Errorf("%s %s: %s", s.Name, s.Target, s.Err)
		}
	}
	return steps, err
}
