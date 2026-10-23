package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"

	"github.com/kubeshark/demo-apps/internal/httpx"
)

const (
	cacheTTL  = 60 * time.Second
	slowQuery = 2 * time.Second
)

type Product struct {
	SKU        string `json:"sku"`
	Name       string `json:"name"`
	PriceCents int    `json:"price_cents"`
	Source     string `json:"source,omitempty"`
}

// Products pairs SKU and NAME markers by their trailing segment.
func Products() []Product {
	names := map[string]string{}
	for _, n := range Markers.Field("NAME") {
		names[suffix(n)] = n
	}
	var out []Product
	for i, sku := range Markers.Field("SKU") {
		name, ok := names[suffix(sku)]
		if !ok {
			panic("catalog markers: SKU without matching NAME: " + sku)
		}
		out = append(out, Product{SKU: sku, Name: name, PriceCents: 1000 + 250*i})
	}
	return out
}

func suffix(v string) string {
	return v[strings.LastIndex(v, "-")+1:]
}

type Service struct {
	db    *sql.DB
	cache *redis.Client
}

type ServiceConfig struct {
	MySQLDSN      string
	RedisAddr     string
	RedisPassword string
}

func NewService(ctx context.Context, cfg ServiceConfig) (*Service, error) {
	db, err := sql.Open("mysql", cfg.MySQLDSN)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := httpx.Retry(ctx, "mysql", 3*time.Second, func() error { return db.PingContext(ctx) }); err != nil {
		return nil, err
	}
	cache := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		// RESP2 is the wire format the dissector decodes.
		Protocol: 2,
	})
	if err := httpx.Retry(ctx, "redis", 3*time.Second, func() error { return cache.Ping(ctx).Err() }); err != nil {
		return nil, err
	}
	s := &Service{db: db, cache: cache}
	return s, s.seed(ctx)
}

func (s *Service) seed(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS products (
		sku VARCHAR(64) PRIMARY KEY, name VARCHAR(128) NOT NULL, price_cents INT NOT NULL)`); err != nil {
		return err
	}
	for _, p := range Products() {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO products (sku, name, price_cents) VALUES (?, ?, ?)
			 ON DUPLICATE KEY UPDATE name = VALUES(name), price_cents = VALUES(price_cents)`,
			p.SKU, p.Name, p.PriceCents); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /products", s.list)
	mux.HandleFunc("GET /products/{sku}", s.get)
	return mux
}

func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT sku, name, price_cents FROM products ORDER BY sku`)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.SKU, &p.Name, &p.PriceCents); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		p.Source = "mysql"
		out = append(out, p)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sku := r.PathValue("sku")
	q := r.URL.Query()
	key := "product:" + sku

	if q.Get("fail") == "table" {
		// The error path: a query against a table that does not exist, so
		// mysql answers with an error frame and this hop answers 500.
		table := Markers.One("TABLE")
		_, err := s.db.ExecContext(ctx, "SELECT sku FROM `"+table+"` WHERE sku = ?", sku)
		var merr *mysql.MySQLError
		if errors.As(err, &merr) {
			httpx.Error(w, http.StatusInternalServerError, fmt.Sprintf("mysql error %d: %s", merr.Number, merr.Message))
			return
		}
		httpx.Error(w, http.StatusInternalServerError, fmt.Sprintf("expected a mysql error, got %v", err))
		return
	}

	if q.Get("slow") == "1" {
		if _, err := s.db.ExecContext(ctx, "SELECT SLEEP(?)", slowQuery.Seconds()); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if q.Get("refresh") == "1" {
		if err := s.cache.Del(ctx, key).Err(); err != nil {
			httpx.Error(w, http.StatusBadGateway, "redis: "+err.Error())
			return
		}
	} else {
		cached, err := s.cache.Get(ctx, key).Result()
		switch {
		case err == nil:
			var p Product
			if json.Unmarshal([]byte(cached), &p) == nil {
				p.Source = "redis"
				httpx.JSON(w, http.StatusOK, p)
				return
			}
		case !errors.Is(err, redis.Nil):
			httpx.Error(w, http.StatusBadGateway, "redis: "+err.Error())
			return
		}
	}

	var p Product
	err := s.db.QueryRowContext(ctx, `SELECT sku, name, price_cents FROM products WHERE sku = ?`, sku).
		Scan(&p.SKU, &p.Name, &p.PriceCents)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.Error(w, http.StatusNotFound, "no product "+sku)
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	body, _ := json.Marshal(p)
	if err := s.cache.Set(ctx, key, body, cacheTTL).Err(); err != nil {
		httpx.Error(w, http.StatusBadGateway, "redis: "+err.Error())
		return
	}
	p.Source = "mysql"
	httpx.JSON(w, http.StatusOK, p)
}
