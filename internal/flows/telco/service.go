package telco

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"github.com/fiorix/go-diameter/v4/diam/dict"
	"github.com/fiorix/go-diameter/v4/diam/sm"
	"github.com/fiorix/go-diameter/v4/diam/sm/smpeer"
	"github.com/ishidawataru/sctp"

	"github.com/kubeshark/demo-apps/internal/httpx"
)

const (
	// Peer connections are recycled so the capabilities exchange shows up
	// periodically, not only once at startup.
	connLifetime  = 2 * time.Minute
	answerTimeout = 10 * time.Second
	echoTimeout   = 5 * time.Second
)

type ChargeRequest struct {
	Subscriber string `json:"subscriber"`
	Context    string `json:"context"`
}

type ChargeResult struct {
	Transport  string `json:"transport"`
	Watchdog   string `json:"watchdog"`
	SessionID  string `json:"session_id"`
	ResultCode uint32 `json:"result_code"`
	Detail     string `json:"detail"`
}

type EchoRequest struct {
	Payload string `json:"payload"`
}

type EchoResult struct {
	Echoed string `json:"echoed"`
	Bytes  int    `json:"bytes"`
}

type ServiceConfig struct {
	DiameterAddr string
	EchoAddr     string
}

// Service keeps one Diameter peer connection per transport, each opened with
// a CER/CEA handshake, and answers HTTP by exchanging Diameter messages on
// the requested transport.
type Service struct {
	cfg     ServiceConfig
	client  *sm.Client
	peers   map[string]*peer
	pending sync.Map
	n       atomic.Uint64
}

type peer struct {
	network string
	mu      sync.Mutex
	conn    diam.Conn
	born    time.Time
}

func NewService(cfg ServiceConfig) *Service {
	s := &Service{cfg: cfg, peers: map[string]*peer{"tcp": {network: "tcp"}, "sctp": {network: "sctp"}}}
	mux := sm.New(&sm.Settings{
		OriginHost:       datatype.DiameterIdentity(ClientHost),
		OriginRealm:      datatype.DiameterIdentity(Realm),
		VendorID:         0,
		ProductName:      "demo-apps telco",
		OriginStateID:    datatype.Unsigned32(time.Now().Unix()),
		FirmwareRevision: 1,
	})
	mux.HandleFunc("CCA", s.answered)
	mux.HandleFunc("DWA", s.answered)
	s.client = &sm.Client{
		Dict:               dict.Default,
		Handler:            mux,
		MaxRetransmits:     2,
		RetransmitInterval: 2 * time.Second,
		AuthApplicationID: []*diam.AVP{
			diam.NewAVP(avp.AuthApplicationID, avp.Mbit, 0, datatype.Unsigned32(CreditControlApp)),
		},
	}
	go func() {
		for report := range mux.ErrorReports() {
			slog.Warn("diameter", "err", report.Error)
		}
	}()
	return s
}

// answered hands an answer to the request waiting on its Hop-by-Hop id.
func (s *Service) answered(_ diam.Conn, m *diam.Message) {
	if ch, ok := s.pending.LoadAndDelete(m.Header.HopByHopID); ok {
		ch.(chan *diam.Message) <- m
	}
}

// conn returns the peer connection for the network, dialing and handshaking
// a new one when none is open or the current one has reached its lifetime.
func (s *Service) conn(network string) (diam.Conn, error) {
	p, ok := s.peers[network]
	if !ok {
		return nil, fmt.Errorf("unknown transport %q", network)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		closed := false
		select {
		case <-p.conn.(diam.CloseNotifier).CloseNotify():
			closed = true
		default:
		}
		if closed || time.Since(p.born) > connLifetime {
			p.conn.Close()
			p.conn = nil
		}
	}
	if p.conn == nil {
		c, err := s.client.DialNetwork(network, s.cfg.DiameterAddr)
		if err != nil {
			return nil, fmt.Errorf("%s dial %s: %w", network, s.cfg.DiameterAddr, err)
		}
		p.conn, p.born = c, time.Now()
	}
	return p.conn, nil
}

func (s *Service) exchange(ctx context.Context, c diam.Conn, m *diam.Message) (*diam.Message, error) {
	ch := make(chan *diam.Message, 1)
	s.pending.Store(m.Header.HopByHopID, ch)
	defer s.pending.Delete(m.Header.HopByHopID)
	if _, err := m.WriteTo(c); err != nil {
		return nil, err
	}
	select {
	case a := <-ch:
		return a, nil
	case <-time.After(answerTimeout):
		return nil, errors.New("no answer within " + answerTimeout.String())
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /charge/{transport}", s.charge)
	mux.HandleFunc("POST /echo", s.echo)
	return mux
}

type cca struct {
	ResultCode   uint32 `avp:"Result-Code"`
	OriginHost   string `avp:"Origin-Host"`
	ErrorMessage string `avp:"Error-Message"`
	Granted      struct {
		Time uint32 `avp:"CC-Time"`
	} `avp:"Granted-Service-Unit"`
}

// charge runs a Device-Watchdog and a Credit-Control exchange on the
// transport in the path. A CCA other than 2001 is answered 502 with the
// Result-Code in the body.
func (s *Service) charge(w http.ResponseWriter, r *http.Request) {
	network := r.PathValue("transport")
	var req ChargeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Subscriber == "" || req.Context == "" {
		httpx.Error(w, http.StatusBadRequest, "subscriber and context required")
		return
	}
	c, err := s.conn(network)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	meta, ok := smpeer.FromContext(c.Context())
	if !ok {
		httpx.Error(w, http.StatusBadGateway, "peer metadata missing after handshake")
		return
	}
	res := ChargeResult{Transport: network}

	dwr := diam.NewRequest(diam.DeviceWatchdog, 0, dict.Default)
	add(dwr, avp.OriginHost, avp.Mbit, datatype.DiameterIdentity(ClientHost))
	add(dwr, avp.OriginRealm, avp.Mbit, datatype.DiameterIdentity(Realm))
	dwa, err := s.exchange(r.Context(), c, dwr)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "DWR over "+network+": "+err.Error())
		return
	}
	var watchdog struct {
		ResultCode uint32 `avp:"Result-Code"`
		OriginHost string `avp:"Origin-Host"`
	}
	if err := dwa.Unmarshal(&watchdog); err != nil {
		httpx.Error(w, http.StatusBadGateway, "DWA: "+err.Error())
		return
	}
	res.Watchdog = fmt.Sprintf("DWA %d from %s", watchdog.ResultCode, watchdog.OriginHost)

	n := s.n.Add(1)
	requestType := ccInitialRequest
	if r.URL.Query().Get("slow") == "1" {
		requestType = ccUpdateRequest
	}
	res.SessionID = fmt.Sprintf("%s;%d;%d;%s", ClientHost, time.Now().Unix(), n, Markers.One("SESSION"))
	ccr := diam.NewRequest(diam.CreditControl, CreditControlApp, dict.Default)
	add(ccr, avp.SessionID, avp.Mbit, datatype.UTF8String(res.SessionID))
	add(ccr, avp.OriginHost, avp.Mbit, datatype.DiameterIdentity(ClientHost))
	add(ccr, avp.OriginRealm, avp.Mbit, datatype.DiameterIdentity(Realm))
	add(ccr, avp.DestinationRealm, avp.Mbit, meta.OriginRealm)
	add(ccr, avp.DestinationHost, avp.Mbit, meta.OriginHost)
	add(ccr, avp.AuthApplicationID, avp.Mbit, datatype.Unsigned32(CreditControlApp))
	add(ccr, avp.ServiceContextID, avp.Mbit, datatype.UTF8String(req.Context))
	add(ccr, avp.CCRequestType, avp.Mbit, datatype.Enumerated(requestType))
	add(ccr, avp.CCRequestNumber, avp.Mbit, datatype.Unsigned32(n))
	add(ccr, avp.UserName, avp.Mbit, datatype.UTF8String(req.Subscriber))
	add(ccr, avp.RequestedServiceUnit, avp.Mbit, &diam.GroupedAVP{AVP: []*diam.AVP{
		diam.NewAVP(avp.CCTime, avp.Mbit, 0, datatype.Unsigned32(grantedSeconds)),
	}})
	answer, err := s.exchange(r.Context(), c, ccr)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "CCR over "+network+": "+err.Error())
		return
	}
	var a cca
	if err := answer.Unmarshal(&a); err != nil {
		httpx.Error(w, http.StatusBadGateway, "CCA: "+err.Error())
		return
	}
	res.ResultCode = a.ResultCode
	status := http.StatusOK
	switch {
	case a.ResultCode == diam.Success && requestType == ccUpdateRequest:
		res.Detail = fmt.Sprintf("CCA %d from %s, %ds granted after the server's %s re-authorization delay", a.ResultCode, a.OriginHost, a.Granted.Time, updateDelay)
	case a.ResultCode == diam.Success:
		res.Detail = fmt.Sprintf("CCA %d from %s, %ds granted", a.ResultCode, a.OriginHost, a.Granted.Time)
	default:
		status = http.StatusBadGateway
		res.Detail = fmt.Sprintf("CCA %d from %s: %s", a.ResultCode, a.OriginHost, a.ErrorMessage)
	}
	httpx.JSON(w, status, res)
}

// echo sends the payload over a fresh SCTP association and reads it back.
func (s *Service) echo(w http.ResponseWriter, r *http.Request) {
	var req EchoRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Payload == "" {
		httpx.Error(w, http.StatusBadRequest, "payload required")
		return
	}
	raddr, err := sctp.ResolveSCTPAddr("sctp", s.cfg.EchoAddr)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "sctp resolve: "+err.Error())
		return
	}
	conn, err := sctp.DialSCTP("sctp", nil, raddr)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "sctp dial "+s.cfg.EchoAddr+": "+err.Error())
		return
	}
	defer func() { _ = conn.Close() }()
	if err := readTimeout(conn, echoTimeout); err != nil {
		httpx.Error(w, http.StatusBadGateway, "sctp setsockopt: "+err.Error())
		return
	}
	if _, err := conn.SCTPWrite([]byte(req.Payload), &sctp.SndRcvInfo{}); err != nil {
		httpx.Error(w, http.StatusBadGateway, "sctp write: "+err.Error())
		return
	}
	buf := make([]byte, 64<<10)
	n, _, err := conn.SCTPRead(buf)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "sctp read: "+err.Error())
		return
	}
	if !bytes.Equal(buf[:n], []byte(req.Payload)) {
		httpx.Error(w, http.StatusBadGateway, fmt.Sprintf("sctp echo mismatch: got %q", buf[:n]))
		return
	}
	httpx.JSON(w, http.StatusOK, EchoResult{Echoed: string(buf[:n]), Bytes: n})
}

// readTimeout bounds blocking reads on the association; the library has
// no deadline support, so the socket option does it.
func readTimeout(conn *sctp.SCTPConn, timeout time.Duration) error {
	rc, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	tv := syscall.NsecToTimeval(timeout.Nanoseconds())
	var sockErr error
	if err := rc.Control(func(fd uintptr) {
		sockErr = syscall.SetsockoptTimeval(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)
	}); err != nil {
		return err
	}
	return sockErr
}
