package wallet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gomodule/redigo/redis"

	"github.com/kubeshark/demo-apps/internal/httpx"
)

const (
	sessionTTL = 300
	maxJSON    = 1 << 16
	maxUpload  = 8 << 20
)

type ServiceConfig struct {
	RedisAddr     string
	RedisPassword string
}

// Service answers the wallet requests. Every sign-in opens a fresh redis
// connection as the wallet ACL user, so the AUTH exchange recurs on every run
// instead of once per pooled connection.
type Service struct {
	cfg ServiceConfig
}

func NewService(ctx context.Context, cfg ServiceConfig) (*Service, error) {
	s := &Service{cfg: cfg}
	return s, httpx.Retry(ctx, "redis", 3*time.Second, func() error { return s.provision(ctx) })
}

// provision creates the wallet ACL user as the default user. The password is
// given as its SHA-256, so the ACL SETUSER command, which redaction does not
// cover, never carries the REDISAUTH marker in clear.
func (s *Service) provision(ctx context.Context) error {
	c, err := redis.DialContext(ctx, "tcp", s.cfg.RedisAddr, redis.DialPassword(s.cfg.RedisPassword),
		redis.DialConnectTimeout(5*time.Second), redis.DialReadTimeout(5*time.Second))
	if err != nil {
		return err
	}
	defer c.Close()
	sum := sha256.Sum256([]byte(Markers.One("REDISAUTH")))
	if _, err := c.Do("ACL", "SETUSER", RedisUser, "reset", "on", "#"+hex.EncodeToString(sum[:]), "~wallet:*", "+set", "+ping"); err != nil {
		return fmt.Errorf("acl setuser: %w", err)
	}
	return nil
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /session", s.signIn)
	mux.HandleFunc("GET /profile", s.profile)
	mux.HandleFunc("POST /payments", s.pay)
	mux.HandleFunc("POST /payments/form", s.payForm)
	mux.HandleFunc("POST /statements", s.statement)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Trace-Id", Markers.One("TRACE"))
		mux.ServeHTTP(w, r)
	})
}

func (s *Service) signIn(w http.ResponseWriter, r *http.Request) {
	var req SignIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSON)).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "malformed sign-in")
		return
	}
	if req.Username != Markers.One("USER") || req.Password != Markers.One("PASSWORD") {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := s.storeSession(r.Context(), req.Username); err != nil {
		httpx.Error(w, http.StatusBadGateway, "session store: "+err.Error())
		return
	}
	w.Header().Add("Set-Cookie", fmt.Sprintf("%s=%s; Path=/%s; HttpOnly; SameSite=Strict",
		SessionCookie, Markers.One("SETCOOKIE"), Markers.One("COOKIEPATH")))
	w.Header().Set("X-Auth-Token", Markers.One("RESPAUTHTOKEN"))
	httpx.JSON(w, http.StatusOK, Session{
		User:         req.Username,
		AccessToken:  Markers.One("ACCESSTOKEN"),
		RefreshToken: Markers.One("REFRESHTOKEN"),
		TokenType:    "Bearer",
		ExpiresIn:    sessionTTL,
	})
}

// storeSession records when the user signed in. The stored value is a
// timestamp, not a secret: redis values other than AUTH arguments are not
// redacted.
func (s *Service) storeSession(ctx context.Context, user string) error {
	c, err := redis.DialContext(ctx, "tcp", s.cfg.RedisAddr,
		redis.DialUsername(RedisUser), redis.DialPassword(Markers.One("REDISAUTH")),
		redis.DialConnectTimeout(5*time.Second), redis.DialReadTimeout(5*time.Second))
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Do("SET", "wallet:session:"+user, time.Now().Unix(), "EX", sessionTTL)
	return err
}

func (s *Service) profile(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != Markers.One("APIKEY") {
		w.Header().Set("WWW-Authenticate", `ApiKey error="revoked"`)
		httpx.Error(w, http.StatusUnauthorized, "api key revoked")
		return
	}
	if c, err := r.Cookie(SessionCookie); err != nil || c.Value != Markers.One("SESSION") {
		httpx.Error(w, http.StatusUnauthorized, "no session")
		return
	}
	httpx.JSON(w, http.StatusOK, Profile{
		User:        r.Header.Get(UserHeader),
		DisplayName: Markers.One("NAME"),
		SSN:         Markers.One("SSN"),
		Pinned:      Markers.One("PINNED"),
		Tokenizer:   Markers.One("TOKENIZER"),
	})
}

func (s *Service) pay(w http.ResponseWriter, r *http.Request) {
	var p Payment
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSON)).Decode(&p); err != nil {
		httpx.Error(w, http.StatusBadRequest, "malformed payment")
		return
	}
	httpx.JSON(w, http.StatusOK, Receipt{
		Status:    "approved",
		Receipt:   Markers.One("RECEIPT"),
		RequestID: p.RequestID,
		CardLast4: last4(p.PaymentRef),
	})
}

func (s *Service) payForm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSON)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("card_number") == "" {
		httpx.Error(w, http.StatusBadRequest, "malformed form payment")
		return
	}
	httpx.JSON(w, http.StatusOK, Receipt{Status: "approved"})
}

func (s *Service) statement(w http.ResponseWriter, r *http.Request) {
	n, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, maxUpload))
	if err != nil {
		httpx.Error(w, http.StatusRequestEntityTooLarge, "statement too large")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]int64{"accepted_bytes": n})
}

func last4(v string) string {
	if len(v) < 4 {
		return ""
	}
	return v[len(v)-4:]
}
