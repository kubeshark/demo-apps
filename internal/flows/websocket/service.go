package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/coder/websocket"
)

const (
	eventCount    = 5
	eventInterval = 200 * time.Millisecond
	sessionIdle   = 30 * time.Second
)

// Service is the live side: it accepts HTTP/1.1 upgrades on /ws and pushes
// events for the channels the markers declare.
type Service struct {
	channels []string
}

func NewService() *Service {
	return &Service{channels: Markers.Field("CHANNEL")}
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /ws", s.ws)
	return mux
}

func (s *Service) ws(w http.ResponseWriter, r *http.Request) {
	// Compression stays off so every frame is readable on the wire.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		slog.Warn("upgrade", "err", err)
		return
	}
	defer func() { _ = c.CloseNow() }()
	ctx, cancel := context.WithTimeout(r.Context(), sessionIdle)
	defer cancel()

	if err := s.session(ctx, c); err != nil {
		slog.Info("session ended", "user", r.Header.Get(UserHeader), "err", err)
	}
}

func (s *Service) session(ctx context.Context, c *websocket.Conn) error {
	msg, err := readMessage(ctx, c)
	if err != nil {
		return err
	}
	if msg.Type != "subscribe" {
		return c.Close(websocket.StatusProtocolError, "expected subscribe, got "+msg.Type)
	}
	if !slices.Contains(s.channels, msg.Channel) {
		// The error path: the client asked for a channel this service does
		// not serve, and the session ends with a policy violation close.
		return c.Close(websocket.StatusPolicyViolation, Markers.One("REASON")+": "+msg.Channel)
	}

	if msg.DelayMs > 0 {
		select {
		case <-time.After(time.Duration(msg.DelayMs) * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	payload := Markers.One("PAYLOAD")
	for seq := 1; seq <= eventCount; seq++ {
		if seq > 1 {
			select {
			case <-time.After(eventInterval):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		body, err := json.Marshal(Event{Channel: msg.Channel, Seq: seq, Payload: payload})
		if err != nil {
			return err
		}
		if err := c.Write(ctx, websocket.MessageText, body); err != nil {
			return err
		}
	}

	for {
		msg, err := readMessage(ctx, c)
		if err != nil {
			return err
		}
		switch msg.Type {
		case "ping":
			if err := c.Write(ctx, websocket.MessageText, []byte("pong")); err != nil {
				return err
			}
		case "unsubscribe":
			return c.Close(websocket.StatusNormalClosure, "unsubscribed "+msg.Channel)
		default:
			return c.Close(websocket.StatusProtocolError, "unexpected "+msg.Type)
		}
	}
}

// readMessage reads one text frame: the bare word ping, or a JSON Message.
func readMessage(ctx context.Context, c *websocket.Conn) (Message, error) {
	typ, data, err := c.Read(ctx)
	if err != nil {
		return Message{}, err
	}
	if typ != websocket.MessageText {
		return Message{}, errors.New("binary frame")
	}
	if string(data) == "ping" {
		return Message{Type: "ping"}, nil
	}
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return Message{}, fmt.Errorf("decode %q: %w", data, err)
	}
	return msg, nil
}
