package orders

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

// Flow is the gateway's side of the path: it places an order with the orders
// service over HTTP/1.1, reads it back, and waits for the notifier to have
// relayed its event.
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
		EnvVar: "ORDERS_URL",
		New:    func(addr string) (flowrun.Flow, error) { return NewFlow(addr), nil },
	})
}

func (f *Flow) Name() string { return Name }
func (f *Flow) Description() string {
	return "Order placement stored in postgresql, published to kafka and relayed through rabbitmq"
}
func (f *Flow) Path() string {
	return "gateway -> orders (http) -> postgresql -> kafka -> notifier -> rabbitmq (amqp)"
}
func (f *Flow) Markers() *markers.Set {
	return Markers
}

func (f *Flow) Run(ctx context.Context, v flowrun.Variant) ([]flowrun.Step, error) {
	n := f.n.Add(1)
	skus := Markers.Field("SKU")
	o := Order{
		ID:       fmt.Sprintf("%s%d", Markers.One("ORDER"), n),
		Customer: Markers.One("CUSTOMER"),
		SKU:      skus[int(n-1)%len(skus)],
		Quantity: 1 + int(n%3),
		Note:     Markers.One("NOTE"),
	}
	t := &flowrun.Trace{}

	if v == flowrun.Error {
		o.Quantity = 0
		err := t.Step("place order", f.BaseURL, "http", func() (string, error) {
			body, err := f.call(ctx, http.MethodPost, "/orders", o, http.StatusUnprocessableEntity)
			if err != nil {
				return "", err
			}
			return "422 as designed: " + string(body), nil
		})
		return t.Steps(), err
	}

	path := "/orders"
	if v == flowrun.Slow {
		path += "?slow=1"
	}
	err := t.Step("place order", f.BaseURL, "http", func() (string, error) {
		_, err := f.call(ctx, http.MethodPost, path, o, http.StatusCreated)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s: %d x %s for %s", o.ID, o.Quantity, o.SKU, o.Customer), nil
	})
	if err != nil {
		return t.Steps(), err
	}
	err = t.Step("read back", f.BaseURL, "http", func() (string, error) {
		body, err := f.call(ctx, http.MethodGet, "/orders/"+o.ID, nil, http.StatusOK)
		if err != nil {
			return "", err
		}
		var got Order
		if err := json.Unmarshal(body, &got); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s stored with note %s", got.ID, got.Note), nil
	})
	if err != nil {
		return t.Steps(), err
	}
	err = t.Step("notified", f.BaseURL, "http", func() (string, error) {
		body, err := f.call(ctx, http.MethodGet, "/orders/"+o.ID+"/notification", nil, http.StatusOK)
		if err != nil {
			return "", err
		}
		var got Notification
		if err := json.Unmarshal(body, &got); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s relayed through kafka and rabbitmq to %s", got.OrderID, got.Customer), nil
	})
	return t.Steps(), err
}

func (f *Flow) call(ctx context.Context, method, path string, in any, want int) ([]byte, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, f.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != want {
		return nil, fmt.Errorf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, out)
	}
	return out, nil
}
