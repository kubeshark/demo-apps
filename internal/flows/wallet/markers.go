// Package wallet is the http -> redis flow that carries the traffic
// sensitive-data redaction acts on: a user signs in, reads a profile and pays
// by card, and every credential, token and card number travels in a carrier
// the redaction rules name (headers, cookies, query, json and form fields,
// a JWT, card numbers, a redis AUTH). The same values sit next to look-alikes
// that must survive, so a capture shows what redaction removes and what it
// leaves alone.
package wallet

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/kubeshark/demo-apps/internal/markers"
)

const Name = "wallet"

//go:embed markers.tsv
var markersTSV string

var Markers = markers.MustParse(Name, markersTSV)

const (
	UserHeader    = "X-Demo-User"
	SessionCookie = "wallet_session"
	ThemeCookie   = "theme"
	// RedisUser is the ACL user the service authenticates as on every
	// sign-in, with the REDISAUTH marker as its password.
	RedisUser = "wallet"
	// StatementUnder and StatementOver straddle the default 2Mi redaction
	// scan budget.
	StatementUnder = 1 << 20
	StatementOver  = 3 << 20
)

type SignIn struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Session struct {
	User         string `json:"user"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

type Profile struct {
	User        string `json:"user"`
	DisplayName string `json:"display_name"`
	SSN         string `json:"ssn"`
	Pinned      string `json:"pinned"`
	Tokenizer   string `json:"tokenizer"`
}

type Payment struct {
	RequestID        string  `json:"request_id"`
	Description      string  `json:"description"`
	AmountCents      int     `json:"amount_cents"`
	CardNumber       string  `json:"card_number"`
	CVV              string  `json:"cvv"`
	PIN              string  `json:"pin"`
	PaymentRef       string  `json:"payment_ref"`
	PaymentRefSpaced string  `json:"payment_ref_spaced"`
	Reference        string  `json:"reference"`
	Note             string  `json:"note"`
	APIKey           string  `json:"apiKey"`
	XAPIKey          string  `json:"x-api-key"`
	Passwd           string  `json:"passwd"`
	Secret           string  `json:"secret"`
	ClientSecret     string  `json:"client_secret"`
	PrivateKey       string  `json:"private_key"`
	Billing          Billing `json:"billing"`
}

type Billing struct {
	Credentials struct {
		Password string `json:"password"`
	} `json:"credentials"`
}

type Receipt struct {
	Status    string `json:"status"`
	Receipt   string `json:"receipt"`
	RequestID string `json:"request_id"`
	CardLast4 string `json:"card_last4"`
}

// PaymentBody is the card payment as sent: every field from the markers plus
// one key, token, spelled with a unicode escape that json.Marshal would never
// produce, so the redactor has to decode keys before matching them.
func PaymentBody() []byte {
	p := Payment{
		RequestID:        Markers.One("REQUESTID"),
		Description:      Markers.One("DESCRIPTION"),
		AmountCents:      450,
		CardNumber:       Markers.One("CARDNUMBER"),
		CVV:              Markers.One("CVV"),
		PIN:              Markers.One("PIN"),
		PaymentRef:       Markers.One("PAN"),
		PaymentRefSpaced: Markers.One("PANSPACED"),
		Reference:        Markers.One("NOTPAN"),
		Note:             "eyJhbGciOiJub25lIn0." + Markers.One("NOTJWT") + ".c2ln",
		APIKey:           Markers.One("APIKEYCAMEL"),
		XAPIKey:          Markers.One("XAPIKEY"),
		Passwd:           Markers.One("PASSWD"),
		Secret:           Markers.One("SECRET"),
		ClientSecret:     Markers.One("CLIENTSECRET"),
		PrivateKey:       Markers.One("PRIVATEKEY"),
	}
	p.Billing.Credentials.Password = Markers.One("NESTED")
	b, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	escaped := `,"\u0074oken":"` + Markers.One("ESCAPEDKEY") + `"}`
	return append(b[:len(b)-1], escaped...)
}

// Assertion is a signed JWT whose subject is the JWTSUB marker. The signing
// key is fixed: nothing verifies it, the token only has to be well formed.
func Assertion() string {
	enc := base64.RawURLEncoding
	head := enc.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims := enc.EncodeToString([]byte(`{"sub":"` + Markers.One("JWTSUB") + `","iat":1760000000}`))
	mac := hmac.New(sha256.New, []byte("demo-apps-wallet"))
	mac.Write([]byte(head + "." + claims))
	return head + "." + claims + "." + enc.EncodeToString(mac.Sum(nil))
}

// Statement is a json upload of about size bytes: the STATEMENT marker at its
// head, inside the inline body preview, and the STATEMENTPASSWORD marker with
// the same suffix at its end.
func Statement(suffix string, size int) []byte {
	head := `{"description":"` + pick("STATEMENT", suffix) + `","lines":"`
	tail := `","password":"` + pick("STATEMENTPASSWORD", suffix) + `"}`
	var b bytes.Buffer
	b.Grow(size)
	b.WriteString(head)
	b.Write(bytes.Repeat([]byte{'x'}, max(0, size-len(head)-len(tail))))
	b.WriteString(tail)
	return b.Bytes()
}

func pick(field, suffix string) string {
	for _, v := range Markers.Field(field) {
		if strings.HasSuffix(v, "-"+suffix) {
			return v
		}
	}
	panic("wallet markers: no " + field + " ending in " + suffix)
}
