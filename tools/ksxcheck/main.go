// ksxcheck asserts that every declared marker of the enabled flows reaches an
// entry the Kubeshark hub streams. It subscribes to the hub's UI stream for the
// namespace, runs every enabled flow in every variant through the gateway, and
// then compares what it saw with the markers files:
//
//	keep   + stream (default)  must be seen, else MISSING
//	keep   + wire              reported only: sent, but no dissector streams it
//	vanish                     must not be seen, else LEAK
//
// It fails as well when the stream carried no entry or no expected marker at
// all, since a blind checker would otherwise report everything as clean.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubeshark/demo-apps/internal/markers"
)

func main() {
	hub := flag.String("hub", "http://127.0.0.1:18899", "Kubeshark hub URL")
	gateway := flag.String("gateway", "http://127.0.0.1:18080", "demo-apps gateway URL")
	namespace := flag.String("namespace", "demo-apps", "namespace the flows run in")
	glob := flag.String("markers", "deploy/chart/files/markers/*.tsv", "markers files; <name>.tsv belongs to flow <name>, clients.tsv to the TLS clients")
	// Flows rotate through their entities run by run, and some carriers (an
	// HTTP request body, a client-side TLS session) are captured on only part
	// of the runs, so each marker needs several chances; the TLS clients run on
	// their own interval and need the stream to stay open across a few of them.
	rounds := flag.Int("rounds", 6, "times each enabled flow runs in each variant")
	clients := flag.Bool("clients", true, "check clients.tsv (the chart's TLS clients are enabled)")
	grace := flag.Duration("grace", 45*time.Second, "how long to keep streaming after the last run")
	timeout := flag.Duration("timeout", 10*time.Minute, "overall deadline")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	code := run(ctx, config{*hub, *gateway, *namespace, *glob, *rounds, *clients, *grace})
	cancel()
	os.Exit(code)
}

type config struct {
	hub, gateway, namespace, glob string
	rounds                        int
	clients                       bool
	grace                         time.Duration
}

type tracked struct {
	markers.Marker
	set string
}

func run(ctx context.Context, cfg config) int {
	flows, err := enabledFlows(ctx, cfg.gateway)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
		return 2
	}
	sets, err := loadMarkers(cfg.glob, flows, cfg.clients)
	if err != nil {
		fmt.Fprintln(os.Stderr, "markers:", err)
		return 2
	}

	var all []tracked
	for _, s := range sets {
		for _, m := range s.Markers {
			all = append(all, tracked{m, s.Flow})
		}
	}
	values := make([]string, len(all))
	for i, m := range all {
		values[i] = m.Value
	}

	streamCtx, stopStream := context.WithCancel(ctx)
	defer stopStream()
	counter := newCounter(values)
	streamErr := make(chan error, 1)
	go func() { streamErr <- stream(streamCtx, cfg.hub, cfg.namespace, counter) }()

	// Give the subscription a moment to attach before generating traffic.
	select {
	case <-time.After(3 * time.Second):
	case err := <-streamErr:
		fmt.Fprintln(os.Stderr, "hub stream:", err)
		return 2
	}
	runs, failed := trigger(ctx, cfg.gateway, flows, cfg.rounds)
	fmt.Printf("ran %d flow runs (%d reported failure) across %d flows\n", runs, failed, len(flows))

	select {
	case <-time.After(cfg.grace):
	case <-ctx.Done():
	}
	stopStream()
	if err := <-streamErr; err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "hub stream:", err)
		return 2
	}
	return report(all, counter, failed)
}

func enabledFlows(ctx context.Context, gateway string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gateway+"/api/flows", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var views []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&views); err != nil {
		return nil, err
	}
	var names []string
	for _, v := range views {
		names = append(names, v.Name)
	}
	sort.Strings(names)
	return names, nil
}

func loadMarkers(glob string, flows []string, clients bool) ([]*markers.Set, error) {
	files, err := filepath.Glob(glob)
	if err != nil {
		return nil, err
	}
	enabled := map[string]bool{}
	for _, f := range flows {
		enabled[f] = true
	}
	var sets []*markers.Set
	found := map[string]bool{}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".tsv")
		if !enabled[name] && (name != "clients" || !clients) {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		s, err := markers.Parse(name, string(b))
		if err != nil {
			return nil, err
		}
		sets = append(sets, s)
		found[name] = true
	}
	for _, f := range flows {
		if !found[f] {
			return nil, fmt.Errorf("flow %s is enabled but has no markers file matching %s", f, glob)
		}
	}
	return sets, nil
}

// trigger runs every flow in every variant, flows in parallel, and returns how
// many runs were made and how many the gateway reported as failed.
func trigger(ctx context.Context, gateway string, flows []string, rounds int) (int, int) {
	var mu sync.Mutex
	runs, failed := 0, 0
	var wg sync.WaitGroup
	for _, flow := range flows {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				for _, v := range []string{"ok", "slow", "error"} {
					ok := runFlow(ctx, gateway, flow, v)
					mu.Lock()
					runs++
					if !ok {
						failed++
						fmt.Printf("run failed: %s/%s\n", flow, v)
					}
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return runs, failed
}

func runFlow(ctx context.Context, gateway, flow, variant string) bool {
	url := fmt.Sprintf("%s/api/flows/%s/run?variant=%s&source=ksxcheck", gateway, flow, variant)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// counter counts, per marker, the streamed entries that contain it.
type counter struct {
	mu      sync.Mutex
	values  []string
	hits    map[string]int
	entries int
}

func newCounter(values []string) *counter {
	return &counter{values: values, hits: map[string]int{}}
}

// Entries arrive as protobuf JSON, where bytes fields (headers, bodies, URLs,
// query text) are base64. Every base64-looking token is decoded and searched
// along with the JSON itself.
var b64 = regexp.MustCompile(`[A-Za-z0-9+/]{8,}={0,2}`)

func (c *counter) add(entry []byte) {
	var hay bytes.Buffer
	hay.Write(entry)
	for _, tok := range b64.FindAll(entry, -1) {
		if dec, err := base64.StdEncoding.DecodeString(string(tok)); err == nil {
			hay.WriteByte('\n')
			hay.Write(dec)
		}
	}
	s := hay.String()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries++
	for _, v := range c.values {
		if strings.Contains(s, v) {
			c.hits[v]++
		}
	}
}

// stream subscribes to the hub's UIEventService over the Connect protocol with
// the JSON codec, so no generated client is needed.
func stream(ctx context.Context, hub, namespace string, c *counter) error {
	msg, _ := json.Marshal(map[string]string{"filter": fmt.Sprintf("%q in namespaces", namespace)})
	var body bytes.Buffer
	body.WriteByte(0)
	_ = binary.Write(&body, binary.BigEndian, uint32(len(msg)))
	body.Write(msg)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hub+"/capture.UIEventService/RegisterClient", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("RegisterClient: status %d: %s", resp.StatusCode, b)
	}

	r := bufio.NewReaderSize(resp.Body, 1<<20)
	for {
		var head [5]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		frame := make([]byte, binary.BigEndian.Uint32(head[1:]))
		if _, err := io.ReadFull(r, frame); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if head[0]&0x02 != 0 {
			var end struct {
				Error *struct{ Code, Message string } `json:"error"`
			}
			if json.Unmarshal(frame, &end) == nil && end.Error != nil {
				return fmt.Errorf("stream ended: %s: %s", end.Error.Code, end.Error.Message)
			}
			return errors.New("stream ended by the hub")
		}
		var ev struct {
			Entry   json.RawMessage `json:"entry"`
			Entries struct {
				Entries []json.RawMessage `json:"entries"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(frame, &ev); err != nil {
			continue
		}
		if len(ev.Entry) > 0 {
			c.add(ev.Entry)
		}
		for _, e := range ev.Entries.Entries {
			c.add(e)
		}
	}
}

func report(all []tracked, c *counter, failedRuns int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Printf("streamed %d entries\n\n%-8s %5s  %-10s %-40s %s\n", c.entries, "RESULT", "HITS", "SET", "MARKER", "WHAT")
	bad, seenExpected := 0, 0
	for _, m := range all {
		n := c.hits[m.Value]
		var result string
		switch {
		case m.Kind == markers.Vanish && n > 0:
			result, bad = "LEAK", bad+1
		case m.Kind == markers.Vanish:
			result = "clean"
		case m.Expect == markers.Wire:
			result = "wire"
		case n > 0:
			result, seenExpected = "ok", seenExpected+1
		default:
			result, bad = "MISSING", bad+1
		}
		fmt.Printf("%-8s %5d  %-10s %-40s %s\n", result, n, m.set, m.Value, m.What)
	}
	fmt.Println()
	if c.entries == 0 || seenExpected == 0 {
		fmt.Println("FAIL: the stream carried no expected marker; the check is blind (wrong hub, namespace, or dissection off)")
		return 1
	}
	if bad > 0 || failedRuns > 0 {
		fmt.Printf("FAIL: %d marker problems, %d failed flow runs\n", bad, failedRuns)
		return 1
	}
	fmt.Println("PASS: every stream marker seen, no leaks; wire markers are sent but not streamed by design")
	return 0
}
