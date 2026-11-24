package wallet

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// Kubeshark streams a request body inline only up to this many bytes; a
// marker past it is invisible to any stream checker.
const inlinePreview = 2048

func TestPaymentBodyStaysInlineAndEscapesOneKey(t *testing.T) {
	b := PaymentBody()
	if len(b) >= inlinePreview {
		t.Fatalf("payment body is %d bytes, its markers would fall past the %d-byte inline preview", len(b), inlinePreview)
	}
	if !bytes.Contains(b, []byte(`"\u0074oken"`)) {
		t.Fatalf("payment body lacks the escaped token key: %s", b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("payment body is not json: %v", err)
	}
	if m["token"] != Markers.One("ESCAPEDKEY") {
		t.Fatalf("escaped key decodes to %v", m["token"])
	}
}

func TestAssertionCarriesSubjectInClaims(t *testing.T) {
	parts := strings.Split(Assertion(), ".")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "eyJ") || !strings.HasPrefix(parts[1], "eyJ") {
		t.Fatalf("not a JWT: %v", parts)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct{ Sub string }
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Sub != Markers.One("JWTSUB") {
		t.Fatalf("claims %s: %v", raw, err)
	}
}

func TestStatementsStraddleTheDefaultScanBudget(t *testing.T) {
	const budget = 2 << 20
	for _, c := range []struct {
		suffix string
		size   int
		over   bool
	}{{"UNDER", StatementUnder, false}, {"OVER", StatementOver, true}} {
		b := Statement(c.suffix, c.size)
		if len(b) != c.size {
			t.Errorf("%s: %d bytes, want %d", c.suffix, len(b), c.size)
		}
		if (len(b) > budget) != c.over {
			t.Errorf("%s: %d bytes against a %d budget", c.suffix, len(b), budget)
		}
		if !json.Valid(b) {
			t.Errorf("%s: not json", c.suffix)
		}
		if !bytes.Contains(b[:inlinePreview], []byte(pick("STATEMENT", c.suffix))) {
			t.Errorf("%s: head marker past the inline preview", c.suffix)
		}
	}
}
