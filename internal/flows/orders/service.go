package orders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kubeshark/demo-apps/internal/httpx"
)

const (
	slowSQL         = "SELECT pg_sleep(2)"
	notifyWait      = 5 * time.Second
	notifyPoll      = 250 * time.Millisecond
	checkViolation  = "23514"
	fkViolation     = "23503"
	undefinedTable  = "42P01"
	undefinedColumn = "42703"
)

type Service struct {
	db          *pgxpool.Pool
	kafka       *producer
	notifierURL string
	http        *http.Client
}

type ServiceConfig struct {
	PostgresURL string
	Kafka       KafkaConfig
	NotifierURL string
}

func NewService(ctx context.Context, cfg ServiceConfig) (*Service, error) {
	pc, err := pgxpool.ParseConfig(cfg.PostgresURL)
	if err != nil {
		return nil, err
	}
	pc.MaxConns = 4
	pc.MaxConnLifetime = connLifetime
	db, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	if err := httpx.Retry(ctx, "postgresql", 3*time.Second, func() error { return db.Ping(ctx) }); err != nil {
		return nil, err
	}
	if err := httpx.Retry(ctx, "kafka", 3*time.Second, func() error { return cfg.Kafka.ensureTopic(ctx) }); err != nil {
		return nil, err
	}
	s := &Service{
		db:          db,
		kafka:       &producer{cfg: cfg.Kafka},
		notifierURL: cfg.NotifierURL,
		http:        &http.Client{Timeout: 5 * time.Second},
	}
	return s, s.seed(ctx)
}

func (s *Service) Close() {
	s.kafka.close()
	s.db.Close()
}

// seed creates the schema and the products the flow declares. Statements
// without arguments go out on the simple protocol, the inserts on the
// extended one.
func (s *Service) seed(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS products (sku TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS orders (
		id TEXT PRIMARY KEY,
		customer TEXT NOT NULL,
		sku TEXT NOT NULL REFERENCES products (sku),
		quantity INT NOT NULL CHECK (quantity > 0),
		note TEXT NOT NULL,
		created TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	for _, sku := range Markers.Field("SKU") {
		if _, err := s.db.Exec(ctx, `INSERT INTO products (sku) VALUES ($1) ON CONFLICT DO NOTHING`, sku); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /orders", s.create)
	mux.HandleFunc("GET /orders/{id}", s.get)
	mux.HandleFunc("GET /orders/{id}/notification", s.notification)
	return mux
}

func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var o Order
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&o); err != nil {
		httpx.Error(w, http.StatusBadRequest, "body: "+err.Error())
		return
	}
	if o.ID == "" || o.Customer == "" || o.SKU == "" {
		httpx.Error(w, http.StatusBadRequest, "id, customer and sku are required")
		return
	}
	if r.URL.Query().Get("slow") == "1" {
		if _, err := s.db.Exec(ctx, slowSQL); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	err := s.insert(ctx, o)
	if lostData(err) && s.declared(o.SKU) {
		// The server lost its data (it keeps none across restarts); seed again
		// rather than reject an order for a product the flow declares.
		if err = s.seed(ctx); err == nil {
			err = s.insert(ctx, o)
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == checkViolation || pgErr.Code == fkViolation) {
		httpx.Error(w, http.StatusUnprocessableEntity, fmt.Sprintf("postgresql %s: %s", pgErr.Code, pgErr.Message))
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	value, _ := json.Marshal(Event{Type: RoutingKey, Order: o})
	if err := s.kafka.produce(ctx, o.ID, value); err != nil {
		httpx.Error(w, http.StatusBadGateway, "kafka: "+err.Error())
		return
	}
	httpx.JSON(w, http.StatusCreated, o)
}

func (s *Service) insert(ctx context.Context, o Order) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO orders (id, customer, sku, quantity, note) VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (id) DO UPDATE SET customer = EXCLUDED.customer, sku = EXCLUDED.sku,
		 quantity = EXCLUDED.quantity, note = EXCLUDED.note`,
		o.ID, o.Customer, o.SKU, o.Quantity, o.Note)
	return err
}

// get reads the order back on the simple protocol, so one run of the flow
// carries both a QUERY and an EXECUTE for the dissector.
func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	var o Order
	err := s.db.QueryRow(r.Context(),
		`SELECT id, customer, sku, quantity, note FROM orders WHERE id = $1`, pgx.QueryExecModeSimpleProtocol, r.PathValue("id")).
		Scan(&o.ID, &o.Customer, &o.SKU, &o.Quantity, &o.Note)
	if errors.Is(err, pgx.ErrNoRows) || lostData(err) {
		httpx.Error(w, http.StatusNotFound, "no order "+r.PathValue("id"))
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}

// notification polls the notifier until it has relayed the order's event
// through rabbitmq, which is the proof the event went end to end.
func (s *Service) notification(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), notifyWait)
	defer cancel()
	id := r.PathValue("id")
	for {
		n, err := s.recent(ctx)
		if err != nil && ctx.Err() == nil {
			httpx.Error(w, http.StatusBadGateway, "notifier: "+err.Error())
			return
		}
		for _, x := range n {
			if x.OrderID == id {
				httpx.JSON(w, http.StatusOK, x)
				return
			}
		}
		select {
		case <-ctx.Done():
			httpx.Error(w, http.StatusGatewayTimeout, fmt.Sprintf("notifier did not relay %s within %s", id, notifyWait))
			return
		case <-time.After(notifyPoll):
		}
	}
}

func (s *Service) recent(ctx context.Context) ([]Notification, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.notifierURL+"/notifications/recent", nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out []Notification
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func (s *Service) declared(sku string) bool {
	for _, v := range Markers.Field("SKU") {
		if v == sku {
			return true
		}
	}
	return false
}

// lostData is true for the shapes a fresh postgresql takes: no table, no
// column, or no product row for the order to reference.
func lostData(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case undefinedTable, undefinedColumn, fkViolation:
		return true
	}
	return false
}
