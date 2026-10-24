package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/kubeshark/demo-apps/internal/httpx"
)

const (
	slowDelay    = "00:00:02"
	openingCents = 100000
	// Connections are recycled so the TDS login exchange shows up
	// periodically, not only once at startup.
	connLifetime = 2 * time.Minute

	database = "ledger"
	table    = database + ".dbo.ledger_entries"
	// Server error number for an invalid object name, what a fresh server
	// answers before the table exists.
	errNoTable = 208
)

type Posting struct {
	From        string `json:"from"`
	To          string `json:"to"`
	AmountCents int64  `json:"amount_cents"`
	Memo        string `json:"memo"`
}

type Balance struct {
	Account      string `json:"account"`
	Entries      int    `json:"entries"`
	BalanceCents int64  `json:"balance_cents"`
}

type Service struct {
	db *sql.DB
}

func NewService(ctx context.Context, dsn string) (*Service, error) {
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxLifetime(connLifetime)
	if err := httpx.Retry(ctx, "mssql", 5*time.Second, func() error { return db.PingContext(ctx) }); err != nil {
		return nil, err
	}
	s := &Service{db: db}
	return s, s.seed(ctx)
}

func (s *Service) seed(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `IF DB_ID(N'`+database+`') IS NULL CREATE DATABASE `+database); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `IF OBJECT_ID(N'`+table+`') IS NULL CREATE TABLE `+table+` (
		id BIGINT IDENTITY(1,1) PRIMARY KEY,
		account NVARCHAR(64) NOT NULL,
		memo NVARCHAR(128) NOT NULL,
		amount_cents BIGINT NOT NULL,
		posted_at DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME(),
		CONSTRAINT [`+Markers.One("CHECK")+`] CHECK (amount_cents <> 0))`); err != nil {
		return err
	}
	opening := Markers.One("OPENING")
	for _, account := range Markers.Field("ACCOUNT") {
		if _, err := s.db.ExecContext(ctx,
			`IF NOT EXISTS (SELECT 1 FROM `+table+` WHERE account = @p1 AND memo = @p2)
			 INSERT INTO `+table+` (account, memo, amount_cents) VALUES (@p1, @p2, @p3)`,
			account, opening, openingCents); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /entries", s.post)
	mux.HandleFunc("GET /entries/{account}", s.balance)
	return mux
}

// post writes a debit and a credit of the same amount in one transaction.
// A zero amount is the error path: the server rejects it with its check
// constraint and this hop answers 500.
func (s *Service) post(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var p Posting
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.declared(p.From) || !s.declared(p.To) {
		httpx.Error(w, http.StatusNotFound, fmt.Sprintf("no account %s or %s", p.From, p.To))
		return
	}
	err := s.transfer(ctx, p)
	if lostData(err) {
		if err = s.seed(ctx); err == nil {
			err = s.transfer(ctx, p)
		}
	}
	var merr mssql.Error
	if errors.As(err, &merr) {
		httpx.Error(w, http.StatusInternalServerError, fmt.Sprintf("mssql error %d: %s", merr.Number, merr.Message))
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, http.StatusCreated, p)
}

func (s *Service) transfer(ctx context.Context, p Posting) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	const insert = `INSERT INTO ` + table + ` (account, memo, amount_cents) VALUES (@p1, @p2, @p3)`
	if _, err := tx.ExecContext(ctx, insert, p.From, p.Memo, -p.AmountCents); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, insert, p.To, p.Memo, p.AmountCents); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) balance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	account := r.PathValue("account")
	if r.URL.Query().Get("slow") == "1" {
		if _, err := s.db.ExecContext(ctx, `WAITFOR DELAY '`+slowDelay+`'`); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	b, err := s.sum(ctx, account)
	if (lostData(err) || (err == nil && b.Entries == 0)) && s.declared(account) {
		// The server lost its data (it keeps none across restarts); seed
		// again rather than answer 404 for an account the flow declares.
		if err = s.seed(ctx); err == nil {
			b, err = s.sum(ctx, account)
		}
	}
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if b.Entries == 0 {
		httpx.Error(w, http.StatusNotFound, "no account "+account)
		return
	}
	httpx.JSON(w, http.StatusOK, b)
}

func (s *Service) sum(ctx context.Context, account string) (Balance, error) {
	b := Balance{Account: account}
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(amount_cents), 0) FROM `+table+` WHERE account = @p1`, account).
		Scan(&b.Entries, &b.BalanceCents)
	return b, err
}

func (s *Service) declared(account string) bool {
	for _, a := range Markers.Field("ACCOUNT") {
		if a == account {
			return true
		}
	}
	return false
}

// lostData is true for the shape a fresh server takes: no table at all.
func lostData(err error) bool {
	var merr mssql.Error
	return errors.As(err, &merr) && merr.Number == errNoTable
}
