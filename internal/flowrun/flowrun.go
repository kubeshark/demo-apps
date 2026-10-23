// Package flowrun is the model shared by the gateway, the runner and the UI:
// a flow is a named, narratable path through the services, a run is one
// execution of it in a chosen variant, and a step is one hop of that run.
package flowrun

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kubeshark/demo-apps/internal/markers"
)

type Variant string

const (
	OK    Variant = "ok"
	Error Variant = "error"
	Slow  Variant = "slow"
)

func ParseVariant(s string) (Variant, error) {
	switch Variant(s) {
	case "", OK:
		return OK, nil
	case Error, Slow:
		return Variant(s), nil
	}
	return "", fmt.Errorf("unknown variant %q", s)
}

type Step struct {
	Name     string        `json:"name"`
	Target   string        `json:"target"`
	Protocol string        `json:"protocol"`
	Detail   string        `json:"detail,omitempty"`
	Duration time.Duration `json:"duration"`
	Err      string        `json:"error,omitempty"`
}

type Result struct {
	Flow     string        `json:"flow"`
	Variant  Variant       `json:"variant"`
	Source   string        `json:"source"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration"`
	Steps    []Step        `json:"steps"`
	// Err is set when the run failed in an unexpected way. The error variant
	// succeeds when it produces the error it was designed to produce.
	Err string `json:"error,omitempty"`
}

// Flow is one narratable path. Run performs it once; it returns the steps it
// took even when one of them failed.
type Flow interface {
	Name() string
	Description() string
	Path() string
	Markers() *markers.Set
	Run(ctx context.Context, v Variant) ([]Step, error)
}

// Trace collects steps during a run.
type Trace struct {
	steps []Step
}

func (t *Trace) Step(name, target, protocol string, fn func() (string, error)) error {
	start := time.Now()
	detail, err := fn()
	s := Step{Name: name, Target: target, Protocol: protocol, Detail: detail, Duration: time.Since(start)}
	if err != nil {
		s.Err = err.Error()
	}
	t.steps = append(t.steps, s)
	return err
}

func (t *Trace) Steps() []Step { return t.steps }

// History keeps the most recent results per flow for the UI.
type History struct {
	mu    sync.Mutex
	limit int
	runs  map[string][]Result
}

func NewHistory(limit int) *History {
	return &History{limit: limit, runs: map[string][]Result{}}
}

func (h *History) Add(r Result) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rs := append([]Result{r}, h.runs[r.Flow]...)
	if len(rs) > h.limit {
		rs = rs[:h.limit]
	}
	h.runs[r.Flow] = rs
}

func (h *History) Recent(flow string) []Result {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Result(nil), h.runs[flow]...)
}
