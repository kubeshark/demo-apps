// Package markers reads a flow's declared payload content: the values a flow
// puts on the wire and whether each must survive capture ("keep") or must not
// ("vanish"). The same file is read by the service that sends the value and by
// any checker that asserts on what was captured, so neither can drift.
package markers

import (
	"bufio"
	"fmt"
	"strings"
)

type Kind string

const (
	Keep   Kind = "keep"
	Vanish Kind = "vanish"
)

type Marker struct {
	Kind  Kind
	Value string
	What  string
}

// Field is the segment after the flow name in KSKEEP-<FLOW>-<FIELD>-<REST>.
func (m Marker) Field() string {
	parts := strings.SplitN(m.Value, "-", 4)
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

type Set struct {
	Flow    string
	Markers []Marker
}

// Parse reads kind<TAB>value<TAB>what lines. Blank lines and lines starting
// with # are ignored. Every value must be unique within the set.
func Parse(flow, text string) (*Set, error) {
	s := &Set{Flow: flow}
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	line := 0
	for sc.Scan() {
		line++
		raw := sc.Text()
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		cols := strings.Split(raw, "\t")
		if len(cols) != 3 {
			return nil, fmt.Errorf("markers %s line %d: want 3 tab-separated columns, got %d", flow, line, len(cols))
		}
		kind := Kind(cols[0])
		if kind != Keep && kind != Vanish {
			return nil, fmt.Errorf("markers %s line %d: unknown kind %q", flow, line, cols[0])
		}
		if cols[1] == "" || cols[2] == "" {
			return nil, fmt.Errorf("markers %s line %d: empty value or description", flow, line)
		}
		if seen[cols[1]] {
			return nil, fmt.Errorf("markers %s line %d: duplicate value %q", flow, line, cols[1])
		}
		seen[cols[1]] = true
		s.Markers = append(s.Markers, Marker{Kind: kind, Value: cols[1], What: cols[2]})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(s.Markers) == 0 {
		return nil, fmt.Errorf("markers %s: no entries", flow)
	}
	return s, nil
}

func MustParse(flow, text string) *Set {
	s, err := Parse(flow, text)
	if err != nil {
		panic(err)
	}
	return s
}

// Field returns every value whose field segment equals name, in file order.
func (s *Set) Field(name string) []string {
	var out []string
	for _, m := range s.Markers {
		if m.Field() == name {
			out = append(out, m.Value)
		}
	}
	return out
}

// One returns the single value with the given field, and panics when the file
// declares zero or several: that is a programming error in the flow, not input.
func (s *Set) One(name string) string {
	v := s.Field(name)
	if len(v) != 1 {
		panic(fmt.Sprintf("markers %s: field %s has %d values, want 1", s.Flow, name, len(v)))
	}
	return v[0]
}
