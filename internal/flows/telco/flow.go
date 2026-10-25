package telco

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/fiorix/go-diameter/v4/diam"

	"github.com/kubeshark/demo-apps/internal/flowrun"
	"github.com/kubeshark/demo-apps/internal/markers"
)

// Flow is the gateway's side of the path: it asks the telco service over
// HTTP/1.1 to charge a subscriber on each transport and to echo over SCTP.
type Flow struct {
	BaseURL string
	Client  *http.Client
	n       atomic.Uint64
}

func NewFlow(baseURL string) *Flow {
	return &Flow{BaseURL: baseURL, Client: &http.Client{Timeout: 20 * time.Second}}
}

func init() {
	flowrun.Register(flowrun.Registration{
		Name:   Name,
		EnvVar: "TELCO_URL",
		New:    func(addr string) (flowrun.Flow, error) { return NewFlow(addr), nil },
	})
}

func (f *Flow) Name() string { return Name }
func (f *Flow) Description() string {
	return "Credit control for a subscriber over Diameter on TCP and on SCTP, then a plain SCTP echo"
}
func (f *Flow) Path() string {
	return "gateway -> telco (http) -> telco-server (diameter over tcp, diameter over sctp, sctp echo)"
}
func (f *Flow) Markers() *markers.Set {
	return Markers
}

func (f *Flow) Run(ctx context.Context, v flowrun.Variant) ([]flowrun.Step, error) {
	subscribers := Markers.Field("SUBSCRIBER")
	req := ChargeRequest{
		Subscriber: subscribers[int(f.n.Add(1)-1)%len(subscribers)],
		Context:    Markers.One("CONTEXT"),
	}
	query, want, wantCode := "", http.StatusOK, uint32(diam.Success)
	switch v {
	case flowrun.Slow:
		query = "slow=1"
	case flowrun.Error:
		req.Context = Markers.One("BADCONTEXT")
		want, wantCode = http.StatusBadGateway, diam.UnableToComply
	}
	t := &flowrun.Trace{}
	var err error
	for _, transport := range []string{"tcp", "sctp"} {
		name := "credit control over " + transport
		if err = t.Step(name, f.BaseURL, "http", func() (string, error) {
			return f.charge(ctx, transport, req, query, want, wantCode)
		}); err != nil {
			return t.Steps(), err
		}
	}
	err = t.Step("sctp echo", f.BaseURL, "http", func() (string, error) {
		return f.echo(ctx, Markers.One("ECHO"))
	})
	return t.Steps(), err
}

func (f *Flow) charge(ctx context.Context, transport string, req ChargeRequest, query string, want int, wantCode uint32) (string, error) {
	url := f.BaseURL + "/charge/" + transport
	if query != "" {
		url += "?" + query
	}
	var res ChargeResult
	status, err := f.post(ctx, url, req, &res)
	if err != nil {
		return "", err
	}
	if status != want || res.ResultCode != wantCode {
		return "", fmt.Errorf("POST %s: status %d result %d, want %d and %d: %s", url, status, res.ResultCode, want, wantCode, res.Detail)
	}
	detail := fmt.Sprintf("%s; %s for %s", res.Watchdog, res.Detail, req.Subscriber)
	if want != http.StatusOK {
		return fmt.Sprintf("Result-Code %d as designed: %s", res.ResultCode, detail), nil
	}
	return detail, nil
}

func (f *Flow) echo(ctx context.Context, payload string) (string, error) {
	var res EchoResult
	status, err := f.post(ctx, f.BaseURL+"/echo", EchoRequest{Payload: payload}, &res)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("POST /echo: status %d", status)
	}
	return fmt.Sprintf("%d bytes echoed: %s", res.Bytes, res.Echoed), nil
}

// post sends v as JSON and decodes the answer into out; a JSON error body
// becomes the returned error.
func (f *Flow) post(ctx context.Context, url string, v, out any) (int, error) {
	body, _ := json.Marshal(v)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var failure struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &failure) == nil && failure.Error != "" {
		return resp.StatusCode, fmt.Errorf("POST %s: status %d: %s", url, resp.StatusCode, failure.Error)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, fmt.Errorf("POST %s: status %d: %w", url, resp.StatusCode, err)
	}
	return resp.StatusCode, nil
}
