package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/kubeshark/demo-apps/internal/flowrun"
	"github.com/kubeshark/demo-apps/internal/markers"
)

// Flow is the gateway's side of the path: it calls the catalog service over
// HTTP/1.1 and reports each hop.
type Flow struct {
	BaseURL string
	Client  *http.Client
	n       atomic.Uint64
}

func NewFlow(baseURL string) *Flow {
	return &Flow{BaseURL: baseURL, Client: &http.Client{Timeout: 15 * time.Second}}
}

func (f *Flow) Name() string { return Name }
func (f *Flow) Description() string {
	return "Product lookup served from the redis cache, falling back to mysql"
}
func (f *Flow) Path() string { return "gateway -> catalog (http) -> redis -> mysql" }
func (f *Flow) Markers() *markers.Set {
	return Markers
}

func (f *Flow) Run(ctx context.Context, v flowrun.Variant) ([]flowrun.Step, error) {
	skus := Markers.Field("SKU")
	sku := skus[int(f.n.Add(1)-1)%len(skus)]
	t := &flowrun.Trace{}
	var err error
	switch v {
	case flowrun.OK:
		err = t.Step("refresh", f.BaseURL, "http", func() (string, error) {
			return f.get(ctx, sku, "refresh=1", http.StatusOK)
		})
		if err == nil {
			err = t.Step("cached", f.BaseURL, "http", func() (string, error) {
				return f.get(ctx, sku, "", http.StatusOK)
			})
		}
	case flowrun.Slow:
		err = t.Step("slow query", f.BaseURL, "http", func() (string, error) {
			return f.get(ctx, sku, "slow=1&refresh=1", http.StatusOK)
		})
	case flowrun.Error:
		err = t.Step("missing table", f.BaseURL, "http", func() (string, error) {
			return f.get(ctx, sku, "fail=table", http.StatusInternalServerError)
		})
	}
	return t.Steps(), err
}

func (f *Flow) get(ctx context.Context, sku, query string, want int) (string, error) {
	url := f.BaseURL + "/products/" + sku
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(UserHeader, Markers.One("USER"))
	resp, err := f.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != want {
		return "", fmt.Errorf("GET %s: status %d, want %d: %s", url, resp.StatusCode, want, body)
	}
	if want != http.StatusOK {
		return fmt.Sprintf("status %d as designed: %s", resp.StatusCode, body), nil
	}
	var p Product
	if err := json.Unmarshal(body, &p); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s from %s", p.Name, p.Source), nil
}
