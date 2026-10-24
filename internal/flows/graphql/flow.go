package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kubeshark/demo-apps/internal/flowrun"
	"github.com/kubeshark/demo-apps/internal/markers"
)

const slowSeconds = 2

const (
	booksQuery = `query BooksByAuthor($author: String!, $delaySeconds: Int) {
  books(author: $author, delaySeconds: $delaySeconds) { id title author }
}`
	bookQuery = `query BookById($id: Int!) {
  book(id: $id) { id title author }
}`
	addBookMutation = `mutation AddBook($title: String!, $author: String!) {
  addBook(title: $title, author: $author) { id title author }
}`
	// The error path selects a field the Book type does not have: a valid
	// document that fails validation, so the server answers with errors.
	missingFieldQuery = `query BooksByAuthor($author: String!) {
  books(author: $author) { id title missing }
}`
)

// Flow is the gateway's side of the path: GraphQL documents posted as JSON
// over HTTP/1.1 to the graphql service.
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
		EnvVar: "GRAPHQL_URL",
		New:    func(addr string) (flowrun.Flow, error) { return NewFlow(addr), nil },
	})
}

func (f *Flow) Name() string { return Name }
func (f *Flow) Description() string {
	return "Book lookup and insertion through a GraphQL API backed by postgresql"
}
func (f *Flow) Path() string { return "gateway -> graphql (http, graphql) -> postgresql" }
func (f *Flow) Markers() *markers.Set {
	return Markers
}

type response struct {
	Data struct {
		Books   []Book `json:"books"`
		Book    *Book  `json:"book"`
		AddBook *Book  `json:"addBook"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func (f *Flow) Run(ctx context.Context, v flowrun.Variant) ([]flowrun.Step, error) {
	n := f.n.Add(1)
	books := Books()
	author := books[int(n-1)%len(books)].Author
	t := &flowrun.Trace{}

	if v == flowrun.Error {
		err := t.Step("query", f.BaseURL, "http", func() (string, error) {
			resp, err := f.post(ctx, Request{
				Query: missingFieldQuery, OperationName: "BooksByAuthor",
				Variables: map[string]any{"author": author},
			}, http.StatusBadRequest)
			if err != nil {
				return "", err
			}
			if len(resp.Errors) == 0 {
				return "", fmt.Errorf("expected a graphql errors array")
			}
			return fmt.Sprintf("status 400 as designed: %s", resp.Errors[0].Message), nil
		})
		return t.Steps(), err
	}

	vars := map[string]any{"author": author}
	if v == flowrun.Slow {
		vars["delaySeconds"] = slowSeconds
	}
	err := t.Step("query", f.BaseURL, "http", func() (string, error) {
		resp, err := f.post(ctx, Request{Query: booksQuery, OperationName: "BooksByAuthor", Variables: vars}, http.StatusOK)
		if err != nil {
			return "", err
		}
		titles := make([]string, 0, len(resp.Data.Books))
		for _, b := range resp.Data.Books {
			titles = append(titles, b.Title)
		}
		return fmt.Sprintf("%d books by %s: %s", len(titles), author, strings.Join(titles, ", ")), nil
	})
	if err != nil {
		return t.Steps(), err
	}

	var added Book
	err = t.Step("mutation", f.BaseURL, "http", func() (string, error) {
		title := fmt.Sprintf("%s%d", Markers.One("ADDED"), n)
		resp, err := f.post(ctx, Request{
			Query: addBookMutation, OperationName: "AddBook",
			Variables: map[string]any{"title": title, "author": author},
		}, http.StatusOK)
		if err != nil {
			return "", err
		}
		if resp.Data.AddBook == nil {
			return "", fmt.Errorf("addBook returned no book")
		}
		added = *resp.Data.AddBook
		return fmt.Sprintf("added %s (id %d) by %s", added.Title, added.ID, added.Author), nil
	})
	if err != nil {
		return t.Steps(), err
	}

	err = t.Step("query by id", f.BaseURL, "http", func() (string, error) {
		resp, err := f.post(ctx, Request{
			Query: bookQuery, OperationName: "BookById", Variables: map[string]any{"id": added.ID},
		}, http.StatusOK)
		if err != nil {
			return "", err
		}
		if resp.Data.Book == nil {
			return "", fmt.Errorf("book %d not found after addBook", added.ID)
		}
		return fmt.Sprintf("book %d is %s", resp.Data.Book.ID, resp.Data.Book.Title), nil
	})
	return t.Steps(), err
}

func (f *Flow) post(ctx context.Context, gql Request, want int) (*response, error) {
	body, err := json.Marshal(gql)
	if err != nil {
		return nil, err
	}
	url := f.BaseURL + "/graphql"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(UserHeader, Markers.One("USER"))
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != want {
		return nil, fmt.Errorf("POST %s %s: status %d, want %d: %s", url, gql.OperationName, resp.StatusCode, want, raw)
	}
	var out response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if want == http.StatusOK && len(out.Errors) > 0 {
		return nil, fmt.Errorf("%s: %s", gql.OperationName, out.Errors[0].Message)
	}
	return &out, nil
}
