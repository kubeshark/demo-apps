package ledger

import (
	"bytes"
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

const amountCents = 1250

// Flow is the gateway's side of the path: it calls the ledger service over
// HTTP/1.1 and reports each hop.
type Flow struct {
	BaseURL string
	Client  *http.Client
	n       atomic.Uint64
}

func NewFlow(baseURL string) *Flow {
	return &Flow{BaseURL: baseURL, Client: &http.Client{Timeout: 15 * time.Second}}
}

func init() {
	flowrun.Register(flowrun.Registration{
		Name:   Name,
		EnvVar: "LEDGER_URL",
		New:    func(addr string) (flowrun.Flow, error) { return NewFlow(addr), nil },
	})
}

func (f *Flow) Name() string { return Name }
func (f *Flow) Description() string {
	return "Double-entry posting and balance lookup in Microsoft SQL Server"
}
func (f *Flow) Path() string { return "gateway -> ledger (http) -> mssql" }
func (f *Flow) Markers() *markers.Set {
	return Markers
}

func (f *Flow) Run(ctx context.Context, v flowrun.Variant) ([]flowrun.Step, error) {
	n := int(f.n.Add(1) - 1)
	accounts := Markers.Field("ACCOUNT")
	p := Posting{
		From:        accounts[n%len(accounts)],
		To:          accounts[(n+1)%len(accounts)],
		AmountCents: amountCents,
		Memo:        Markers.One("MEMO"),
	}
	t := &flowrun.Trace{}
	var err error
	switch v {
	case flowrun.OK:
		err = t.Step("post pair", f.BaseURL, "http", func() (string, error) {
			return f.post(ctx, p, http.StatusCreated)
		})
		if err == nil {
			err = t.Step("balance", f.BaseURL, "http", func() (string, error) {
				return f.balance(ctx, p.To, "")
			})
		}
	case flowrun.Slow:
		err = t.Step("slow balance", f.BaseURL, "http", func() (string, error) {
			return f.balance(ctx, p.To, "slow=1")
		})
	case flowrun.Error:
		p.AmountCents = 0
		err = t.Step("zero amount", f.BaseURL, "http", func() (string, error) {
			return f.post(ctx, p, http.StatusInternalServerError)
		})
	}
	return t.Steps(), err
}

func (f *Flow) post(ctx context.Context, p Posting, want int) (string, error) {
	body, _ := json.Marshal(p)
	resp, err := f.do(ctx, http.MethodPost, f.BaseURL+"/entries", body)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != want {
		return "", fmt.Errorf("POST /entries: status %d, want %d: %s", resp.StatusCode, want, out)
	}
	if want != http.StatusCreated {
		return fmt.Sprintf("status %d as designed: %s", resp.StatusCode, out), nil
	}
	return fmt.Sprintf("%d cents from %s to %s", p.AmountCents, p.From, p.To), nil
}

func (f *Flow) balance(ctx context.Context, account, query string) (string, error) {
	url := f.BaseURL + "/entries/" + account
	if query != "" {
		url += "?" + query
	}
	resp, err := f.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: status %d: %s", url, resp.StatusCode, out)
	}
	var b Balance
	if err := json.Unmarshal(out, &b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: %d cents over %d entries", b.Account, b.BalanceCents, b.Entries), nil
}

func (f *Flow) do(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set(UserHeader, Markers.One("USER"))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return f.Client.Do(req)
}
