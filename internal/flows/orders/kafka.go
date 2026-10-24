package orders

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kversion"
	"github.com/twmb/franz-go/pkg/sasl/plain"
)

// Connections are recycled so the authentication exchange of every server
// shows up periodically, not only once at startup.
const connLifetime = 2 * time.Minute

type KafkaConfig struct {
	Brokers  []string
	User     string
	Password string
	// Pinned caps the client's API versions at what the dissector decodes:
	// ApiVersions v3, Metadata v11, Fetch v12 and Produce v9 are the kversion
	// 3.0.0 set, the newest whose Metadata stays at v11.
	Pinned bool
}

func (c KafkaConfig) opts(extra ...kgo.Opt) []kgo.Opt {
	opts := []kgo.Opt{
		kgo.SeedBrokers(c.Brokers...),
		kgo.SASL(plain.Auth{User: c.User, Pass: c.Password}.AsMechanism()),
		kgo.ClientID("demo-apps"),
	}
	if c.Pinned {
		opts = append(opts, kgo.MaxVersions(kversion.V3_0_0()))
	}
	return append(opts, extra...)
}

func (c KafkaConfig) ensureTopic(ctx context.Context) error {
	cl, err := kgo.NewClient(c.opts()...)
	if err != nil {
		return err
	}
	defer cl.Close()
	if err := cl.Ping(ctx); err != nil {
		return err
	}
	_, err = kadm.NewClient(cl).CreateTopic(ctx, 1, 1, nil, Topic)
	if errors.Is(err, kerr.TopicAlreadyExists) {
		return nil
	}
	return err
}

// producer keeps one kafka client and replaces it every connLifetime, which
// is how the SASL exchange recurs on a producer that is otherwise never idle.
type producer struct {
	cfg    KafkaConfig
	mu     sync.Mutex
	client *kgo.Client
	opened time.Time
}

func (p *producer) get() (*kgo.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil && time.Since(p.opened) < connLifetime {
		return p.client, nil
	}
	if p.client != nil {
		p.client.Close()
		p.client = nil
	}
	// A broker that restarted has lost the topic (it keeps no data); asking for
	// auto-creation in Metadata brings it back instead of blocking until timeout.
	cl, err := kgo.NewClient(p.cfg.opts(kgo.DefaultProduceTopic(Topic), kgo.AllowAutoTopicCreation())...)
	if err != nil {
		return nil, err
	}
	p.client, p.opened = cl, time.Now()
	return cl, nil
}

func (p *producer) produce(ctx context.Context, key string, value []byte) error {
	cl, err := p.get()
	if err != nil {
		return err
	}
	return cl.ProduceSync(ctx, &kgo.Record{Key: []byte(key), Value: value}).FirstErr()
}

func (p *producer) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.client != nil {
		p.client.Close()
		p.client = nil
	}
}
