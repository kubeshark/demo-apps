package telco

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/avp"
	"github.com/fiorix/go-diameter/v4/diam/datatype"
	"github.com/fiorix/go-diameter/v4/diam/sm"
	"github.com/fiorix/go-diameter/v4/diam/sm/smparser"
	"github.com/ishidawataru/sctp"
)

const (
	ccInitialRequest = 1
	ccUpdateRequest  = 2
	grantedSeconds   = 600
	// Re-authorization takes a rating round trip the server simulates; this
	// is the slow path.
	updateDelay = 2 * time.Second
)

// Server is the Diameter peer and the SCTP echo the telco service talks to.
type Server struct {
	mux *sm.StateMachine
}

func NewServer() *Server {
	s := &Server{}
	s.mux = sm.New(&sm.Settings{
		OriginHost:       datatype.DiameterIdentity(ServerHost),
		OriginRealm:      datatype.DiameterIdentity(Realm),
		VendorID:         0,
		ProductName:      "demo-apps telco-server",
		FirmwareRevision: 1,
		OnCER:            s.authenticatePeer,
	})
	s.mux.HandleFunc("CCR", s.creditControl)
	go func() {
		for report := range s.mux.ErrorReports() {
			slog.Warn("diameter", "err", report.Error, "peer", report.Conn.RemoteAddr())
		}
	}()
	return s
}

// authenticatePeer rejects a CER from outside the realm with
// DIAMETER_UNKNOWN_PEER before the state machine completes the handshake.
func (s *Server) authenticatePeer(c diam.Conn, m *diam.Message) {
	cer := new(smparser.CER)
	if _, err := cer.Parse(m, smparser.Server); err != nil {
		return
	}
	if string(cer.OriginRealm) == Realm {
		return
	}
	a := m.Answer(diam.UnknownPeer)
	add(a, avp.OriginHost, avp.Mbit, datatype.DiameterIdentity(ServerHost))
	add(a, avp.OriginRealm, avp.Mbit, datatype.DiameterIdentity(Realm))
	add(a, avp.ErrorMessage, 0, datatype.UTF8String("peer realm "+string(cer.OriginRealm)+" is not "+Realm))
	if _, err := a.WriteTo(c); err != nil {
		slog.Warn("diameter CEA", "err", err)
	}
	slog.Warn("diameter peer rejected", "host", cer.OriginHost, "realm", cer.OriginRealm, "addr", c.RemoteAddr())
	c.Close()
}

type ccr struct {
	SessionID        string `avp:"Session-Id"`
	ServiceContextID string `avp:"Service-Context-Id"`
	RequestType      int32  `avp:"CC-Request-Type"`
	RequestNumber    uint32 `avp:"CC-Request-Number"`
	UserName         string `avp:"User-Name"`
}

// creditControl answers a CCR: 2001 with a granted unit for the known
// service context, 5012 for any other.
func (s *Server) creditControl(c diam.Conn, m *diam.Message) {
	var req ccr
	if err := m.Unmarshal(&req); err != nil {
		slog.Warn("diameter CCR", "err", err)
		return
	}
	code := uint32(diam.Success)
	if req.ServiceContextID != Markers.One("CONTEXT") {
		code = diam.UnableToComply
	}
	if code == diam.Success && req.RequestType == ccUpdateRequest {
		time.Sleep(updateDelay)
	}
	a := m.Answer(code)
	add(a, avp.SessionID, avp.Mbit, datatype.UTF8String(req.SessionID))
	add(a, avp.OriginHost, avp.Mbit, datatype.DiameterIdentity(ServerHost))
	add(a, avp.OriginRealm, avp.Mbit, datatype.DiameterIdentity(Realm))
	add(a, avp.AuthApplicationID, avp.Mbit, datatype.Unsigned32(CreditControlApp))
	add(a, avp.CCRequestType, avp.Mbit, datatype.Enumerated(req.RequestType))
	add(a, avp.CCRequestNumber, avp.Mbit, datatype.Unsigned32(req.RequestNumber))
	add(a, avp.UserName, avp.Mbit, datatype.UTF8String(req.UserName))
	if code == diam.Success {
		add(a, avp.GrantedServiceUnit, avp.Mbit, &diam.GroupedAVP{AVP: []*diam.AVP{
			diam.NewAVP(avp.CCTime, avp.Mbit, 0, datatype.Unsigned32(grantedSeconds)),
		}})
	} else {
		add(a, avp.ErrorMessage, 0, datatype.UTF8String("unknown Service-Context-Id "+req.ServiceContextID))
	}
	if _, err := a.WriteTo(c); err != nil {
		slog.Warn("diameter CCA", "err", err)
		return
	}
	slog.Info("diameter CCA", "transport", c.RemoteAddr().Network(), "user", req.UserName, "context", req.ServiceContextID, "result", code)
}

// ServeDiameter accepts Diameter peers on the named network until the
// listener fails.
func (s *Server) ServeDiameter(network, addr string) error {
	slog.Info("diameter listening", "network", network, "addr", addr)
	return diam.ListenAndServeNetwork(network, addr, s.mux, nil)
}

// ServeEcho answers every SCTP message with the same bytes.
func (s *Server) ServeEcho(ctx context.Context, addr string) error {
	laddr, err := sctp.ResolveSCTPAddr("sctp", addr)
	if err != nil {
		return err
	}
	ln, err := sctp.ListenSCTP("sctp", laddr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	slog.Info("sctp echo listening", "addr", addr)
	for {
		conn, err := ln.AcceptSCTP()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go echo(conn)
	}
}

func echo(conn *sctp.SCTPConn) {
	defer func() { _ = conn.Close() }()
	buf := make([]byte, 64<<10)
	for {
		n, info, err := conn.SCTPRead(buf)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				slog.Debug("sctp echo read", "err", err)
			}
			return
		}
		if n == 0 {
			continue
		}
		reply := &sctp.SndRcvInfo{}
		if info != nil {
			reply.Stream, reply.PPID = info.Stream, info.PPID
		}
		if _, err := conn.SCTPWrite(buf[:n], reply); err != nil {
			slog.Warn("sctp echo write", "err", err)
			return
		}
	}
}
