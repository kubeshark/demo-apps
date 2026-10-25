package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/coder/websocket"

	"github.com/kubeshark/demo-apps/internal/flowrun"
	"github.com/kubeshark/demo-apps/internal/markers"
)

const slowDelayMs = 2000

// Flow is the gateway's side of the path: one WebSocket session per run
// against the live service.
type Flow struct {
	BaseURL string
	n       atomic.Uint64
}

func NewFlow(baseURL string) *Flow {
	return &Flow{BaseURL: baseURL}
}

func init() {
	flowrun.Register(flowrun.Registration{
		Name:   Name,
		EnvVar: "LIVE_URL",
		New:    func(addr string) (flowrun.Flow, error) { return NewFlow(addr), nil },
	})
}

func (f *Flow) Name() string { return Name }
func (f *Flow) Description() string {
	return "A WebSocket session: subscribe, receive server pushes, ping, unsubscribe"
}
func (f *Flow) Path() string { return "gateway -> live (websocket)" }
func (f *Flow) Markers() *markers.Set {
	return Markers
}

func (f *Flow) Run(ctx context.Context, v flowrun.Variant) ([]flowrun.Step, error) {
	channels := Markers.Field("CHANNEL")
	channel := channels[int(f.n.Add(1)-1)%len(channels)]
	sub := Message{Type: "subscribe", Channel: channel}
	switch v {
	case flowrun.Slow:
		sub.DelayMs = slowDelayMs
	case flowrun.Error:
		sub.Channel = Markers.One("MISSING")
	}
	url := f.BaseURL + "/ws"
	t := &flowrun.Trace{}

	var c *websocket.Conn
	err := t.Step("upgrade", url, "ws", func() (string, error) {
		header := http.Header{}
		header.Set(UserHeader, Markers.One("USER"))
		conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
			HTTPHeader:      header,
			CompressionMode: websocket.CompressionDisabled,
		})
		if err != nil {
			return "", err
		}
		c = conn
		return fmt.Sprintf("%s, protocols switched", resp.Status), nil
	})
	if err != nil {
		return t.Steps(), err
	}
	defer func() { _ = c.CloseNow() }()

	err = t.Step("subscribe", url, "ws", func() (string, error) {
		return "subscribed to " + sub.Channel, writeJSON(ctx, c, sub)
	})
	if err != nil {
		return t.Steps(), err
	}

	if v == flowrun.Error {
		err = t.Step("close", url, "ws", func() (string, error) {
			_, data, err := c.Read(ctx)
			if err == nil {
				return "", fmt.Errorf("expected a close, got frame %q", data)
			}
			var ce websocket.CloseError
			if !errors.As(err, &ce) || ce.Code != websocket.StatusPolicyViolation {
				return "", fmt.Errorf("expected close %d, got %w", websocket.StatusPolicyViolation, err)
			}
			return fmt.Sprintf("closed %d as designed: %s", ce.Code, ce.Reason), nil
		})
		return t.Steps(), err
	}

	var last Event
	err = t.Step("events", url, "ws", func() (string, error) {
		for seq := 1; ; seq++ {
			_, data, err := c.Read(ctx)
			if err != nil {
				return "", fmt.Errorf("event %d: %w", seq, err)
			}
			if err := json.Unmarshal(data, &last); err != nil {
				return "", fmt.Errorf("event %d: %w", seq, err)
			}
			if last.Seq != seq || last.Channel != channel {
				return "", fmt.Errorf("event %d: got %+v", seq, last)
			}
			if seq == eventCount {
				return fmt.Sprintf("%d events on %s, payload %s", seq, channel, last.Payload), nil
			}
		}
	})
	if err != nil {
		return t.Steps(), err
	}

	err = t.Step("close", url, "ws", func() (string, error) {
		if err := c.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
			return "", err
		}
		_, data, err := c.Read(ctx)
		if err != nil {
			return "", fmt.Errorf("pong: %w", err)
		}
		if string(data) != "pong" {
			return "", fmt.Errorf("expected pong, got %q", data)
		}
		if err := writeJSON(ctx, c, Message{Type: "unsubscribe", Channel: channel}); err != nil {
			return "", err
		}
		_, data, err = c.Read(ctx)
		if err == nil {
			return "", fmt.Errorf("expected a close, got frame %q", data)
		}
		var ce websocket.CloseError
		if !errors.As(err, &ce) || ce.Code != websocket.StatusNormalClosure {
			return "", fmt.Errorf("expected close %d, got %w", websocket.StatusNormalClosure, err)
		}
		return fmt.Sprintf("pong received, closed %d: %s", ce.Code, ce.Reason), nil
	})
	return t.Steps(), err
}

func writeJSON(ctx context.Context, c *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, data)
}
