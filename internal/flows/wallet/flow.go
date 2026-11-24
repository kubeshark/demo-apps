package wallet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kubeshark/demo-apps/internal/flowrun"
	"github.com/kubeshark/demo-apps/internal/markers"
)

// Flow is the gateway's side of the path. Step details and errors never
// include a request or response body: the gateway API returns them, and a
// secret echoed there would read as a redaction leak.
type Flow struct {
	BaseURL string
	Client  *http.Client
}

func NewFlow(baseURL string) *Flow {
	return &Flow{BaseURL: baseURL, Client: &http.Client{Timeout: 30 * time.Second}}
}

func init() {
	flowrun.Register(flowrun.Registration{
		Name:   Name,
		EnvVar: "WALLET_URL",
		New:    func(addr string) (flowrun.Flow, error) { return NewFlow(addr), nil },
	})
}

func (f *Flow) Name() string { return Name }
func (f *Flow) Description() string {
	return "Card wallet sign-in, profile and payments carrying credentials, tokens and card numbers, for sensitive-data redaction"
}
func (f *Flow) Path() string { return "gateway -> wallet (http) -> redis" }
func (f *Flow) Markers() *markers.Set {
	return Markers
}

func (f *Flow) Run(ctx context.Context, v flowrun.Variant) ([]flowrun.Step, error) {
	t := &flowrun.Trace{}
	var err error
	switch v {
	case flowrun.OK:
		err = t.Step("sign in", f.BaseURL, "http", func() (string, error) { return f.signIn(ctx) })
		if err == nil {
			err = t.Step("profile", f.BaseURL, "http", func() (string, error) {
				return f.profile(ctx, Markers.One("APIKEY"), http.StatusOK)
			})
		}
		if err == nil {
			err = t.Step("card payment", f.BaseURL, "http", func() (string, error) { return f.pay(ctx) })
		}
		if err == nil {
			err = t.Step("form payment", f.BaseURL, "http", func() (string, error) { return f.payForm(ctx) })
		}
	case flowrun.Slow:
		err = t.Step("statement under budget", f.BaseURL, "http", func() (string, error) {
			return f.upload(ctx, Statement("UNDER", StatementUnder))
		})
		if err == nil {
			err = t.Step("statement over budget", f.BaseURL, "http", func() (string, error) {
				return f.upload(ctx, Statement("OVER", StatementOver))
			})
		}
	case flowrun.Error:
		err = t.Step("revoked api key", f.BaseURL, "http", func() (string, error) {
			return f.profile(ctx, Markers.One("REVOKEDKEY"), http.StatusUnauthorized)
		})
	}
	return t.Steps(), err
}

func (f *Flow) signIn(ctx context.Context) (string, error) {
	body, _ := json.Marshal(SignIn{Username: Markers.One("USER"), Password: Markers.One("PASSWORD")})
	req, err := f.request(ctx, http.MethodPost, "/session", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	var s Session
	resp, err := f.do(req, http.StatusOK, &s)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("signed in as %s, %d cookie(s), %s token for %ds", s.User, len(resp.Cookies()), s.TokenType, s.ExpiresIn), nil
}

func (f *Flow) profile(ctx context.Context, apiKey string, want int) (string, error) {
	q := url.Values{"token": {Markers.One("QUERYTOKEN")}, "view": {Markers.One("VIEW")}}
	req, err := f.request(ctx, http.MethodGet, "/profile?"+q.Encode(), "", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+Markers.One("BEARER"))
	req.Header.Set("X-API-Key", apiKey)
	req.Header.Set("X-Auth-Token", Markers.One("XAUTHTOKEN"))
	req.Header.Set("X-Client-Assertion", Assertion())
	req.AddCookie(&http.Cookie{Name: SessionCookie, Value: Markers.One("SESSION")})
	req.AddCookie(&http.Cookie{Name: ThemeCookie, Value: Markers.One("COOKIETHEME")})
	if want != http.StatusOK {
		if _, err := f.do(req, want, nil); err != nil {
			return "", err
		}
		return fmt.Sprintf("status %d as designed: api key revoked", want), nil
	}
	var p Profile
	if _, err := f.do(req, want, &p); err != nil {
		return "", err
	}
	return "profile of " + p.DisplayName, nil
}

func (f *Flow) pay(ctx context.Context) (string, error) {
	req, err := f.request(ctx, http.MethodPost, "/payments", "application/json", bytes.NewReader(PaymentBody()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Proxy-Authorization", "Basic "+Markers.One("PROXYAUTH"))
	req.Header.Set("X-Request-Id", Markers.One("REQUESTID"))
	var r Receipt
	if _, err := f.do(req, http.StatusOK, &r); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s, card ending %s", r.Status, r.CardLast4), nil
}

func (f *Flow) payForm(ctx context.Context) (string, error) {
	form := url.Values{
		"password":    {Markers.One("FORMPASSWORD")},
		"card_number": {Markers.One("FORMCARD")},
		"description": {Markers.One("FORMDESCRIPTION")},
	}
	req, err := f.request(ctx, http.MethodPost, "/payments/form", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	var r Receipt
	if _, err := f.do(req, http.StatusOK, &r); err != nil {
		return "", err
	}
	return r.Status + " by form", nil
}

func (f *Flow) upload(ctx context.Context, body []byte) (string, error) {
	req, err := f.request(ctx, http.MethodPost, "/statements", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	var r struct {
		Bytes int64 `json:"accepted_bytes"`
	}
	if _, err := f.do(req, http.StatusOK, &r); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d bytes accepted", r.Bytes), nil
}

func (f *Flow) request(ctx context.Context, method, path, contentType string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, f.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set(UserHeader, Markers.One("USER"))
	return req, nil
}

// do sends req, checks the status and decodes a json answer into out.
func (f *Flow) do(req *http.Request, want int, out any) (*http.Response, error) {
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != want {
		return nil, fmt.Errorf("%s %s: status %d, want %d", req.Method, req.URL.Path, resp.StatusCode, want)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
		}
	}
	return resp, nil
}
