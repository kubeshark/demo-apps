package graphql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/graphql-go/graphql"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/kubeshark/demo-apps/internal/httpx"
)

const (
	// Connections are recycled so the postgresql startup and authentication
	// exchange shows up periodically, not only once at startup.
	connLifetime = 2 * time.Minute
	// Books added by the mutation are kept this long, then pruned.
	addedTTL = time.Hour
)

type Service struct {
	db     *sql.DB
	schema graphql.Schema
}

// NewService connects to postgresql with the given DSN, which must carry
// sslmode=disable for the dissector to see the protocol.
func NewService(ctx context.Context, dsn string) (*Service, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxLifetime(connLifetime)
	if err := httpx.Retry(ctx, "postgresql", 3*time.Second, func() error { return db.PingContext(ctx) }); err != nil {
		return nil, err
	}
	s := &Service{db: db}
	if s.schema, err = s.buildSchema(); err != nil {
		return nil, err
	}
	return s, s.seed(ctx)
}

func (s *Service) seed(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS authors (id SERIAL PRIMARY KEY, name TEXT NOT NULL UNIQUE)`,
		`CREATE TABLE IF NOT EXISTS books (
			id SERIAL PRIMARY KEY, title TEXT NOT NULL UNIQUE,
			author_id INT NOT NULL REFERENCES authors(id), created TIMESTAMPTZ NOT NULL DEFAULT now())`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	for _, b := range Books() {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO authors (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`, b.Author); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO books (title, author_id) SELECT $1, id FROM authors WHERE name = $2
			 ON CONFLICT (title) DO NOTHING`, b.Title, b.Author); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) declared(author string) bool {
	for _, b := range Books() {
		if b.Author == author {
			return true
		}
	}
	return false
}

// lostTables is true when postgresql came back empty: it keeps nothing across
// restarts, so the first query after one fails with undefined_table.
func lostTables(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01"
}

func (s *Service) buildSchema() (graphql.Schema, error) {
	bookType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Book",
		Fields: graphql.Fields{
			"id":     &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
			"title":  &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
			"author": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		},
	})
	query := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"books": &graphql.Field{
				Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(bookType))),
				Args: graphql.FieldConfigArgument{
					"author":       &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
					"delaySeconds": &graphql.ArgumentConfig{Type: graphql.Int, DefaultValue: 0},
				},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					author, _ := p.Args["author"].(string)
					delay, _ := p.Args["delaySeconds"].(int)
					return s.books(p.Context, author, delay)
				},
			},
			"book": &graphql.Field{
				Type: bookType,
				Args: graphql.FieldConfigArgument{"id": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.Int)}},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					id, _ := p.Args["id"].(int)
					return s.book(p.Context, id)
				},
			},
		},
	})
	mutation := graphql.NewObject(graphql.ObjectConfig{
		Name: "Mutation",
		Fields: graphql.Fields{
			"addBook": &graphql.Field{
				Type: graphql.NewNonNull(bookType),
				Args: graphql.FieldConfigArgument{
					"title":  &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
					"author": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
				},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					title, _ := p.Args["title"].(string)
					author, _ := p.Args["author"].(string)
					return s.addBook(p.Context, title, author)
				},
			},
		},
	})
	return graphql.NewSchema(graphql.SchemaConfig{Query: query, Mutation: mutation})
}

func (s *Service) books(ctx context.Context, author string, delaySeconds int) ([]Book, error) {
	if delaySeconds > 0 {
		if _, err := s.db.ExecContext(ctx, `SELECT pg_sleep($1)`, delaySeconds); err != nil {
			return nil, err
		}
	}
	out, err := s.booksBy(ctx, author)
	if (lostTables(err) || (err == nil && len(out) == 0)) && s.declared(author) {
		// The server lost its data; seed again rather than answer nothing
		// for an author the flow declares.
		if err = s.seed(ctx); err == nil {
			out, err = s.booksBy(ctx, author)
		}
	}
	return out, err
}

func (s *Service) booksBy(ctx context.Context, author string) ([]Book, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT b.id, b.title, a.name FROM books b JOIN authors a ON a.id = b.author_id
		 WHERE a.name = $1 ORDER BY b.id`, author)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Book{}
	for rows.Next() {
		var b Book
		if err := rows.Scan(&b.ID, &b.Title, &b.Author); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Service) book(ctx context.Context, id int) (*Book, error) {
	var b Book
	err := s.db.QueryRowContext(ctx,
		`SELECT b.id, b.title, a.name FROM books b JOIN authors a ON a.id = b.author_id WHERE b.id = $1`, id).
		Scan(&b.ID, &b.Title, &b.Author)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (s *Service) addBook(ctx context.Context, title, author string) (*Book, error) {
	if !s.declared(author) {
		return nil, fmt.Errorf("unknown author %q", author)
	}
	b, err := s.insertBook(ctx, title, author)
	if lostTables(err) || errors.Is(err, sql.ErrNoRows) {
		// The server lost its data: no tables, or an authors table with
		// no row to attach the book to. Seed again and retry.
		if err = s.seed(ctx); err == nil {
			b, err = s.insertBook(ctx, title, author)
		}
	}
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx,
		`DELETE FROM books WHERE created < now() - $1::interval AND title LIKE $2`,
		addedTTL.String(), Markers.One("ADDED")+"%")
	return b, err
}

func (s *Service) insertBook(ctx context.Context, title, author string) (*Book, error) {
	b := Book{Title: title, Author: author}
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO books (title, author_id) SELECT $1, id FROM authors WHERE name = $2
		 ON CONFLICT (title) DO UPDATE SET created = now() RETURNING id`, title, author).Scan(&b.ID)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

type Request struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /graphql", s.serve)
	return mux
}

func (s *Service) serve(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	res := graphql.Do(graphql.Params{
		Schema:         s.schema,
		RequestString:  req.Query,
		OperationName:  req.OperationName,
		VariableValues: req.Variables,
		Context:        r.Context(),
	})
	// A request that fails before execution (a field that does not exist,
	// a missing variable) has no data: the GraphQL over HTTP convention
	// answers 400 with only the errors array.
	status := http.StatusOK
	if res.Data == nil && res.HasErrors() {
		status = http.StatusBadRequest
	}
	httpx.JSON(w, status, res)
}
