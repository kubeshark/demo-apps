package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

type flowFilter struct {
	Variant   string `json:"variant"`
	Title     string `json:"title"`
	KFL       string `json:"kfl"`
	Redaction bool   `json:"redaction"`
}

type filterResult struct {
	flowFilter
	flow string
	hits int
	err  error
}

// runFilters checks the Kubeshark filters the gateway shows on each flow page:
// it subscribes once per filter, runs every enabled flow in every variant, and
// fails when a filter matched nothing or the hub rejected it. Filters that
// only match under redaction are checked only with -redaction.
func runFilters(ctx context.Context, cfg config) int {
	flows, err := enabledFlows(ctx, cfg.gateway)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
		return 2
	}
	var results []*filterResult
	for _, flow := range flows {
		fs, err := flowFilters(ctx, cfg.gateway, flow)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gateway:", err)
			return 2
		}
		for _, f := range fs {
			if f.Redaction && !cfg.redaction {
				continue
			}
			results = append(results, &filterResult{flowFilter: f, flow: flow})
		}
	}
	if len(results) == 0 {
		fmt.Fprintln(os.Stderr, "no filters to check")
		return 2
	}

	streamCtx, stopStreams := context.WithCancel(ctx)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, r := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := stream(streamCtx, cfg.hub, r.KFL, func([]byte) {
				mu.Lock()
				r.hits++
				mu.Unlock()
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				mu.Lock()
				r.err = err
				mu.Unlock()
			}
		}()
	}
	select {
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
	}
	runs, failed := trigger(ctx, cfg.gateway, flows, cfg.rounds)
	fmt.Printf("ran %d flow runs (%d reported failure) across %d flows, %d filters\n\n", runs, failed, len(flows), len(results))
	select {
	case <-time.After(cfg.grace):
	case <-ctx.Done():
	}
	stopStreams()
	wg.Wait()

	bad := 0
	fmt.Printf("%-8s %5s  %-10s %-6s %s\n", "RESULT", "HITS", "FLOW", "RUN", "FILTER")
	for _, r := range results {
		result := "ok"
		switch {
		case r.err != nil:
			result, bad = "ERROR", bad+1
		case r.hits == 0:
			result, bad = "NOMATCH", bad+1
		}
		fmt.Printf("%-8s %5d  %-10s %-6s %s: %s\n", result, r.hits, r.flow, r.Variant, r.Title, r.KFL)
		if r.err != nil {
			fmt.Printf("         %v\n", r.err)
		}
	}
	fmt.Println()
	if bad > 0 || failed > 0 {
		fmt.Printf("FAIL: %d filters matched nothing or were rejected, %d failed flow runs\n", bad, failed)
		return 1
	}
	fmt.Println("PASS: every filter matched what its flow produced")
	return 0
}

func flowFilters(ctx context.Context, gateway, flow string) ([]flowFilter, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gateway+"/api/flows/"+url.PathEscape(flow), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/flows/%s: status %d", flow, resp.StatusCode)
	}
	var d struct {
		Filters []flowFilter `json:"filters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	return d.Filters, nil
}
