package orders

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/kubeshark/demo-apps/internal/httpx"
)

const (
	ConsumerGroup = "notifier"
	recentLimit   = 20
)

type NotifierConfig struct {
	Kafka   KafkaConfig
	AMQPURL string
}

// Notifier consumes order events from kafka, relays each one through the
// rabbitmq exchange, and consumes its own queue so the message is delivered
// and acknowledged on the wire too.
type Notifier struct {
	cfg    NotifierConfig
	events chan Event
	mu     sync.Mutex
	recent []Notification
}

func NewNotifier(ctx context.Context, cfg NotifierConfig) (*Notifier, error) {
	if err := httpx.Retry(ctx, "kafka", 3*time.Second, func() error { return cfg.Kafka.ensureTopic(ctx) }); err != nil {
		return nil, err
	}
	if err := httpx.Retry(ctx, "rabbitmq", 3*time.Second, func() error {
		conn, err := amqp.Dial(cfg.AMQPURL)
		if err != nil {
			return err
		}
		return conn.Close()
	}); err != nil {
		return nil, err
	}
	return &Notifier{cfg: cfg, events: make(chan Event, 64)}, nil
}

func (n *Notifier) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /notifications/recent", func(w http.ResponseWriter, _ *http.Request) {
		n.mu.Lock()
		out := append([]Notification(nil), n.recent...)
		n.mu.Unlock()
		httpx.JSON(w, http.StatusOK, out)
	})
	return mux
}

// Run consumes until ctx ends. Each rabbitmq connection lives connLifetime,
// then is replaced, so the AMQP handshake recurs.
func (n *Notifier) Run(ctx context.Context) error {
	cl, err := kgo.NewClient(n.cfg.Kafka.opts(
		kgo.ConsumerGroup(ConsumerGroup),
		kgo.ConsumeTopics(Topic),
	)...)
	if err != nil {
		return err
	}
	defer cl.Close()
	go n.consumeKafka(ctx, cl)
	for ctx.Err() == nil {
		if err := n.amqpSession(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("rabbitmq session", "err", err)
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
		}
	}
	return nil
}

func (n *Notifier) consumeKafka(ctx context.Context, cl *kgo.Client) {
	for ctx.Err() == nil {
		fetches := cl.PollFetches(ctx)
		fetches.EachError(func(topic string, partition int32, err error) {
			if ctx.Err() == nil {
				slog.Warn("kafka fetch", "topic", topic, "partition", partition, "err", err)
			}
		})
		fetches.EachRecord(func(r *kgo.Record) {
			var ev Event
			if err := json.Unmarshal(r.Value, &ev); err != nil {
				slog.Warn("kafka record", "key", string(r.Key), "err", err)
				return
			}
			slog.Info("kafka consumed", "key", string(r.Key), "order", ev.Order.ID)
			select {
			case n.events <- ev:
			case <-ctx.Done():
			}
		})
	}
}

func (n *Notifier) amqpSession(ctx context.Context) error {
	conn, err := amqp.Dial(n.cfg.AMQPURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer func() { _ = ch.Close() }()
	if err := ch.ExchangeDeclare(Exchange, "direct", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err := ch.QueueDeclare(Queue, true, false, false, false, nil); err != nil {
		return err
	}
	if err := ch.QueueBind(Queue, RoutingKey, Exchange, false, nil); err != nil {
		return err
	}
	deliveries, err := ch.Consume(Queue, ConsumerGroup, false, false, false, false, nil)
	if err != nil {
		return err
	}
	closed := conn.NotifyClose(make(chan *amqp.Error, 1))
	session, cancel := context.WithTimeout(ctx, connLifetime)
	defer cancel()
	for {
		select {
		case <-session.Done():
			return nil
		case cerr := <-closed:
			if cerr == nil {
				return errors.New("connection closed")
			}
			return cerr
		case ev := <-n.events:
			body, _ := json.Marshal(ev)
			err := ch.PublishWithContext(session, Exchange, RoutingKey, false, false, amqp.Publishing{
				ContentType:  "application/json",
				MessageId:    ev.Order.ID,
				Type:         ev.Type,
				DeliveryMode: amqp.Persistent,
				Timestamp:    time.Now(),
				Body:         body,
			})
			if err != nil {
				return err
			}
			slog.Info("amqp published", "order", ev.Order.ID, "exchange", Exchange, "key", RoutingKey)
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("deliveries closed")
			}
			var ev Event
			if err := json.Unmarshal(d.Body, &ev); err != nil {
				slog.Warn("amqp delivery", "message_id", d.MessageId, "err", err)
				_ = d.Nack(false, false)
				continue
			}
			if err := d.Ack(false); err != nil {
				return err
			}
			n.record(Notification{OrderID: ev.Order.ID, Customer: ev.Order.Customer, SKU: ev.Order.SKU, Note: ev.Order.Note})
			slog.Info("amqp delivered", "order", ev.Order.ID, "customer", ev.Order.Customer, "sku", ev.Order.SKU)
		}
	}
}

func (n *Notifier) record(x Notification) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.recent = append([]Notification{x}, n.recent...)
	if len(n.recent) > recentLimit {
		n.recent = n.recent[:recentLimit]
	}
}
