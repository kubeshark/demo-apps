package identity

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-ldap/ldap/v3"
	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2866"
	"layeh.com/radius/rfc2869"

	"github.com/kubeshark/demo-apps/internal/httpx"
)

const (
	slowDelay = 2 * time.Second
	// Connections are recycled so the authentication exchange of every
	// server shows up periodically, not only once at startup.
	connLifetime  = 2 * time.Minute
	radiusTimeout = 5 * time.Second
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResult struct {
	Username string `json:"username"`
	LDAP     struct {
		Bind   string `json:"bind"`
		Search string `json:"search,omitempty"`
		Modify string `json:"modify,omitempty"`
	} `json:"ldap"`
	Radius struct {
		Access     string `json:"access"`
		Accounting string `json:"accounting,omitempty"`
	} `json:"radius"`
}

type ServiceConfig struct {
	LDAPAddr       string
	BaseDN         string
	AdminPassword  string
	RadiusAuthAddr string
	RadiusAcctAddr string
	RadiusSecret   string
}

// Service keeps one LDAP connection, re-bound per login, and talks RADIUS
// per request over UDP.
type Service struct {
	cfg      ServiceConfig
	radius   *radius.Client
	sessions atomic.Uint64

	mu   sync.Mutex
	conn *ldap.Conn
	born time.Time
}

func NewService(ctx context.Context, cfg ServiceConfig) (*Service, error) {
	s := &Service{cfg: cfg, radius: &radius.Client{Retry: time.Second, MaxPacketErrors: 10}}
	err := httpx.Retry(ctx, "ldap", 3*time.Second, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		c, err := s.ldap()
		if err != nil {
			return err
		}
		return s.seed(c)
	})
	return s, err
}

func (s *Service) peopleDN() string { return "ou=people," + s.cfg.BaseDN }
func (s *Service) adminDN() string  { return "cn=admin," + s.cfg.BaseDN }
func (s *Service) userDN(user string) string {
	return "uid=" + ldap.EscapeDN(user) + "," + s.peopleDN()
}

// ldap returns the shared connection, dialing a new one when none is open or
// the current one has reached its lifetime. The caller holds s.mu.
func (s *Service) ldap() (*ldap.Conn, error) {
	if s.conn != nil && (s.conn.IsClosing() || time.Since(s.born) > connLifetime) {
		s.drop()
	}
	if s.conn == nil {
		c, err := ldap.DialURL("ldap://"+s.cfg.LDAPAddr, ldap.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}))
		if err != nil {
			return nil, err
		}
		c.SetTimeout(10 * time.Second)
		s.conn, s.born = c, time.Now()
	}
	return s.conn, nil
}

func (s *Service) drop() {
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

// seed binds as admin and creates the people subtree and every declared
// account, resetting the password of accounts that already exist.
func (s *Service) seed(c *ldap.Conn) error {
	if err := c.Bind(s.adminDN(), s.cfg.AdminPassword); err != nil {
		return fmt.Errorf("admin bind: %w", err)
	}
	ou := ldap.NewAddRequest(s.peopleDN(), nil)
	ou.Attribute("objectClass", []string{"organizationalUnit"})
	ou.Attribute("ou", []string{"people"})
	if err := c.Add(ou); err != nil && !ldap.IsErrorWithCode(err, ldap.LDAPResultEntryAlreadyExists) {
		return err
	}
	for _, a := range Accounts() {
		add := ldap.NewAddRequest(s.userDN(a.User), nil)
		add.Attribute("objectClass", []string{"inetOrgPerson"})
		add.Attribute("uid", []string{a.User})
		add.Attribute("cn", []string{a.User})
		add.Attribute("sn", []string{suffix(a.User)})
		add.Attribute("userPassword", []string{a.Password})
		err := c.Add(add)
		if ldap.IsErrorWithCode(err, ldap.LDAPResultEntryAlreadyExists) {
			mod := ldap.NewModifyRequest(s.userDN(a.User), nil)
			mod.Replace("userPassword", []string{a.Password})
			err = c.Modify(mod)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) declared(user string) bool {
	for _, a := range Accounts() {
		if a.User == user {
			return true
		}
	}
	return false
}

// exists checks as admin whether the user's entry is present; a fresh
// OpenLDAP keeps nothing across restarts.
func (s *Service) exists(c *ldap.Conn, user string) (bool, error) {
	if err := c.Bind(s.adminDN(), s.cfg.AdminPassword); err != nil {
		return false, err
	}
	res, err := c.Search(ldap.NewSearchRequest(s.peopleDN(), ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 1, 0, false,
		"(uid="+ldap.EscapeFilter(user)+")", []string{"uid"}, nil))
	if ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(res.Entries) > 0, nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /login", s.login)
	return mux
}

// login binds, searches and modifies the user's LDAP entry, then runs the
// RADIUS access and accounting exchanges for the same credentials. A wrong
// password is answered 401 with both rejections in the body.
func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req LoginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "username and password required")
		return
	}
	res := LoginResult{Username: req.Username}
	session := fmt.Sprintf("%s%d", Markers.One("SESSION"), s.sessions.Add(1))

	rejected, err := s.ldapLogin(req, &res)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "ldap: "+err.Error())
		return
	}

	if r.URL.Query().Get("slow") == "1" {
		select {
		case <-time.After(slowDelay):
		case <-ctx.Done():
			return
		}
	}

	access, err := s.access(ctx, req, session)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "radius: "+err.Error())
		return
	}
	res.Radius.Access = access.String()
	switch access {
	case radius.CodeAccessAccept:
		if err := s.account(ctx, req.Username, session); err != nil {
			httpx.Error(w, http.StatusBadGateway, "radius accounting: "+err.Error())
			return
		}
		res.Radius.Accounting = "Start and Stop acknowledged for " + session
	case radius.CodeAccessReject:
		rejected = true
	default:
		httpx.Error(w, http.StatusBadGateway, "radius: unexpected "+access.String())
		return
	}
	status := http.StatusOK
	if rejected {
		status = http.StatusUnauthorized
	}
	httpx.JSON(w, status, res)
}

func (s *Service) ldapLogin(req LoginRequest, res *LoginResult) (rejected bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.ldap()
	if err != nil {
		return false, err
	}
	defer func() {
		if ldap.IsErrorWithCode(err, ldap.ErrorNetwork) {
			s.drop()
		}
	}()
	dn := s.userDN(req.Username)
	err = c.Bind(dn, req.Password)
	if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) && s.declared(req.Username) {
		// The server lost its data (it keeps none across restarts); seed
		// again rather than reject a user the flow declares.
		present, lookupErr := s.exists(c, req.Username)
		if lookupErr != nil {
			return false, lookupErr
		}
		if !present {
			if err = s.seed(c); err != nil {
				return false, err
			}
			err = c.Bind(dn, req.Password)
		}
	}
	if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
		res.LDAP.Bind = "invalidCredentials (49) for " + dn
		return true, nil
	}
	if err != nil {
		return false, err
	}
	res.LDAP.Bind = "bound as " + dn

	found, err := c.Search(ldap.NewSearchRequest(s.peopleDN(), ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		"(uid="+ldap.EscapeFilter(req.Username)+")", []string{"cn", "description"}, nil))
	if err != nil {
		return false, err
	}
	res.LDAP.Search = fmt.Sprintf("%d entry under %s", len(found.Entries), s.peopleDN())

	mod := ldap.NewModifyRequest(dn, nil)
	description := Markers.One("DESCRIPTION") + " " + time.Now().UTC().Format(time.RFC3339)
	mod.Replace("description", []string{description})
	if err = c.Modify(mod); err != nil {
		return false, err
	}
	res.LDAP.Modify = "description replaced with " + description
	return false, nil
}

// access is a PAP Access-Request; the server answers Accept or Reject.
func (s *Service) access(ctx context.Context, req LoginRequest, session string) (radius.Code, error) {
	p := radius.New(radius.CodeAccessRequest, []byte(s.cfg.RadiusSecret))
	_ = rfc2865.UserName_SetString(p, req.Username)
	_ = rfc2865.UserPassword_SetString(p, req.Password)
	_ = rfc2865.NASIdentifier_SetString(p, Markers.One("NAS"))
	_ = rfc2865.ServiceType_Set(p, rfc2865.ServiceType_Value_LoginUser)
	_ = rfc2866.AcctSessionID_SetString(p, session)
	if err := signRequest(p); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, radiusTimeout)
	defer cancel()
	resp, err := s.radius.Exchange(ctx, p, s.cfg.RadiusAuthAddr)
	if err != nil {
		return 0, err
	}
	return resp.Code, nil
}

// signRequest adds the Message-Authenticator of RFC 2869 section 5.14: an
// HMAC-MD5 over the request with the attribute zeroed, keyed by the shared
// secret. The client library sends it only when the caller sets it.
func signRequest(p *radius.Packet) error {
	if err := rfc2869.MessageAuthenticator_Set(p, make([]byte, md5.Size)); err != nil {
		return err
	}
	wire, err := p.MarshalBinary()
	if err != nil {
		return err
	}
	mac := hmac.New(md5.New, p.Secret)
	mac.Write(wire)
	return rfc2869.MessageAuthenticator_Set(p, mac.Sum(nil))
}

// account sends the Start and Stop records of one session.
func (s *Service) account(ctx context.Context, user, session string) error {
	start := time.Now()
	for _, status := range []rfc2866.AcctStatusType{rfc2866.AcctStatusType_Value_Start, rfc2866.AcctStatusType_Value_Stop} {
		p := radius.New(radius.CodeAccountingRequest, []byte(s.cfg.RadiusSecret))
		_ = rfc2866.AcctStatusType_Set(p, status)
		_ = rfc2866.AcctSessionID_SetString(p, session)
		_ = rfc2865.UserName_SetString(p, user)
		_ = rfc2865.NASIdentifier_SetString(p, Markers.One("NAS"))
		if status == rfc2866.AcctStatusType_Value_Stop {
			_ = rfc2866.AcctSessionTime_Set(p, rfc2866.AcctSessionTime(time.Since(start).Seconds()))
			_ = rfc2866.AcctTerminateCause_Set(p, rfc2866.AcctTerminateCause_Value_UserRequest)
		}
		ctx, cancel := context.WithTimeout(ctx, radiusTimeout)
		resp, err := s.radius.Exchange(ctx, p, s.cfg.RadiusAcctAddr)
		cancel()
		if err != nil {
			return err
		}
		if resp.Code != radius.CodeAccountingResponse {
			return fmt.Errorf("%s: unexpected %s", status, resp.Code)
		}
	}
	return nil
}
