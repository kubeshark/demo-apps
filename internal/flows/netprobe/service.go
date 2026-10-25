package netprobe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"github.com/kubeshark/demo-apps/internal/flowrun"
	"github.com/kubeshark/demo-apps/internal/httpx"
)

const (
	echoes       = 3
	echoInterval = 200 * time.Millisecond
	slowEchoes   = 5
	slowInterval = 500 * time.Millisecond
	missingPath  = "/this-path-does-not-exist"
)

// Report is the probe service's answer: one step per lookup, ping and fetch,
// in the order they ran.
type Report struct {
	Steps []flowrun.Step `json:"steps"`
}

type ServiceConfig struct {
	// DNSAddr is host:port of the authoritative server. The host is resolved
	// through the cluster resolver on every dial, so that lookup is on the
	// wire too.
	DNSAddr     string
	Zone        string
	PingTargets []string
	EgressURLs  []string
}

type Service struct {
	cfg      ServiceConfig
	resolver *net.Resolver
	client   *http.Client
	n        atomic.Uint64
}

func NewService(cfg ServiceConfig) *Service {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &Service{
		cfg: cfg,
		resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, cfg.DNSAddr)
			},
		},
		// A fresh connection per request, so every run shows the TLS handshake and
		// the HTTP/2 preface the dissector needs, rather than frames mid-connection.
		client: &http.Client{
			Timeout:   10 * time.Second,
			Transport: &http.Transport{DisableKeepAlives: true, ForceAttemptHTTP2: true},
		},
	}
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /probe", s.probe)
	return mux
}

// probe runs every hop and answers 502 when one failed or when the variant
// is the error path, whose failures are designed.
func (s *Service) probe(w http.ResponseWriter, r *http.Request) {
	v, err := flowrun.ParseVariant(r.URL.Query().Get("variant"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	steps := s.run(r.Context(), v)
	status := http.StatusOK
	if v == flowrun.Error {
		status = http.StatusBadGateway
	}
	for _, st := range steps {
		if st.Err != "" {
			status = http.StatusBadGateway
		}
	}
	httpx.JSON(w, status, Report{Steps: steps})
}

func (s *Service) run(ctx context.Context, v flowrun.Variant) []flowrun.Step {
	t := &flowrun.Trace{}
	s.lookups(ctx, t, v)
	s.pings(ctx, t, v)
	s.egress(ctx, t, v)
	return t.Steps()
}

func (s *Service) fqdn(label string) string {
	return label + "." + s.cfg.Zone + "."
}

func (s *Service) lookups(ctx context.Context, t *flowrun.Trace, v flowrun.Variant) {
	hosts := Markers.Field("HOST")
	host := s.fqdn(hosts[int(s.n.Add(1)-1)%len(hosts)])
	host6 := s.fqdn(Markers.One("HOST6"))
	srv := Markers.One("SRV")
	txt := s.fqdn(Markers.One("TXT"))
	missing := s.fqdn(Markers.One("MISSING"))
	dns := s.cfg.DNSAddr

	_ = t.Step("dns A", dns, "dns", func() (string, error) {
		ips, err := s.resolver.LookupIP(ctx, "ip4", host)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s A %s", host, join(ips)), nil
	})
	_ = t.Step("dns AAAA", dns, "dns", func() (string, error) {
		ips, err := s.resolver.LookupIP(ctx, "ip6", host6)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s AAAA %s", host6, join(ips)), nil
	})
	_ = t.Step("dns SRV", dns, "dns", func() (string, error) {
		_, recs, err := s.resolver.LookupSRV(ctx, srv, "tcp", s.cfg.Zone+".")
		if err != nil {
			return "", err
		}
		if len(recs) == 0 {
			return "", fmt.Errorf("srv %s: no records", srv)
		}
		return fmt.Sprintf("_%s._tcp.%s SRV %s:%d", srv, s.cfg.Zone, recs[0].Target, recs[0].Port), nil
	})
	_ = t.Step("dns TXT", dns, "dns", func() (string, error) {
		texts, err := s.resolver.LookupTXT(ctx, txt)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s TXT %q", txt, strings.Join(texts, " ")), nil
	})
	_ = t.Step("dns NXDOMAIN", dns, "dns", func() (string, error) {
		ips, err := s.resolver.LookupIP(ctx, "ip4", missing)
		if err == nil {
			return "", fmt.Errorf("lookup %s: want NXDOMAIN, got %s", missing, join(ips))
		}
		var dnsErr *net.DNSError
		if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
			return "", fmt.Errorf("lookup %s: want NXDOMAIN: %w", missing, err)
		}
		if v == flowrun.Error {
			return "NXDOMAIN as designed: " + dnsErr.Error(), nil
		}
		return fmt.Sprintf("%s NXDOMAIN", missing), nil
	})
}

func join(ips []net.IP) string {
	out := make([]string, len(ips))
	for i, ip := range ips {
		out[i] = ip.String()
	}
	return strings.Join(out, ", ")
}

func (s *Service) pings(ctx context.Context, t *flowrun.Trace, v flowrun.Variant) {
	count, interval := echoes, echoInterval
	if v == flowrun.Slow {
		count, interval = slowEchoes, slowInterval
	}
	for _, target := range s.cfg.PingTargets {
		_ = t.Step("icmp", target, "icmp", func() (string, error) {
			p, err := probing.NewPinger(target)
			if err != nil {
				return "", err
			}
			p.Count = count
			p.Interval = interval
			p.Timeout = time.Duration(count)*interval + 2*time.Second
			// An ICMP datagram socket: real echo requests without a raw
			// socket, which a non-root container cannot open.
			p.SetPrivileged(false)
			if err := p.RunWithContext(ctx); err != nil {
				return "", err
			}
			st := p.Statistics()
			if st.PacketsRecv == 0 {
				return "", fmt.Errorf("ping %s (%s): no reply to %d echoes", target, st.IPAddr, st.PacketsSent)
			}
			return fmt.Sprintf("%s (%s): %d/%d replies, avg rtt %s", target, st.IPAddr, st.PacketsRecv, st.PacketsSent, st.AvgRtt.Round(time.Microsecond)), nil
		})
	}
}

func (s *Service) egress(ctx context.Context, t *flowrun.Trace, v flowrun.Variant) {
	if len(s.cfg.EgressURLs) == 0 {
		_ = t.Step("https egress", "", "https", func() (string, error) {
			return "skipped: no egress urls configured", nil
		})
		return
	}
	for _, u := range s.cfg.EgressURLs {
		_ = t.Step("https egress", u, "https", func() (string, error) {
			return s.get(ctx, u, http.StatusOK)
		})
	}
	if v == flowrun.Error {
		u := strings.TrimSuffix(s.cfg.EgressURLs[0], "/") + missingPath
		_ = t.Step("https egress", u, "https", func() (string, error) {
			return s.get(ctx, u, http.StatusNotFound)
		})
	}
}

func (s *Service) get(ctx context.Context, url string, want int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(ProbeHeader, Markers.One("PROBE"))
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != want {
		return "", fmt.Errorf("GET %s: status %d, want %d", url, resp.StatusCode, want)
	}
	detail := fmt.Sprintf("status %d, %d bytes, %s", resp.StatusCode, n, resp.Proto)
	if resp.TLS != nil {
		detail += " over " + tls.VersionName(resp.TLS.Version)
	}
	if want != http.StatusOK {
		return detail + " as designed", nil
	}
	return detail, nil
}
