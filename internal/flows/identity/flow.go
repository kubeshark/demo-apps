package identity

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

// Flow is the gateway's side of the path: it logs a user in through the
// identity service over HTTP/1.1 and reports what LDAP and RADIUS answered.
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
		EnvVar: "IDENTITY_URL",
		New:    func(addr string) (flowrun.Flow, error) { return NewFlow(addr), nil },
	})
}

func (f *Flow) Name() string { return Name }
func (f *Flow) Description() string {
	return "User login checked against OpenLDAP, then authenticated and accounted by FreeRADIUS"
}
func (f *Flow) Path() string {
	return "gateway -> identity (http) -> openldap (ldap) + freeradius (radius)"
}
func (f *Flow) Markers() *markers.Set {
	return Markers
}

func (f *Flow) Run(ctx context.Context, v flowrun.Variant) ([]flowrun.Step, error) {
	accounts := Accounts()
	a := accounts[int(f.n.Add(1)-1)%len(accounts)]
	t := &flowrun.Trace{}
	var err error
	switch v {
	case flowrun.OK:
		err = t.Step("login", f.BaseURL, "http", func() (string, error) {
			return f.login(ctx, a, "", http.StatusOK)
		})
	case flowrun.Slow:
		err = t.Step("slow login", f.BaseURL, "http", func() (string, error) {
			return f.login(ctx, a, "slow=1", http.StatusOK)
		})
	case flowrun.Error:
		a.Password = Markers.One("BADPASSWORD")
		err = t.Step("wrong password", f.BaseURL, "http", func() (string, error) {
			return f.login(ctx, a, "", http.StatusUnauthorized)
		})
	}
	return t.Steps(), err
}

func (f *Flow) login(ctx context.Context, a Account, query string, want int) (string, error) {
	url := f.BaseURL + "/login"
	if query != "" {
		url += "?" + query
	}
	body, _ := json.Marshal(LoginRequest{Username: a.User, Password: a.Password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != want {
		return "", fmt.Errorf("POST %s: status %d, want %d: %s", url, resp.StatusCode, want, raw)
	}
	var res LoginResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", err
	}
	detail := fmt.Sprintf("ldap: %s", res.LDAP.Bind)
	if res.LDAP.Search != "" {
		detail += ", " + res.LDAP.Search + ", " + res.LDAP.Modify
	}
	detail += "; radius: " + res.Radius.Access
	if res.Radius.Accounting != "" {
		detail += ", " + res.Radius.Accounting
	}
	if query == "slow=1" {
		detail += " (2s pause in identity between the ldap and radius hops)"
	}
	if want != http.StatusOK {
		return fmt.Sprintf("status %d as designed: %s", resp.StatusCode, detail), nil
	}
	return detail, nil
}
