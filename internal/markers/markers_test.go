package markers

import (
	"strings"
	"testing"
)

func TestParseRejectsMalformedLines(t *testing.T) {
	cases := map[string]string{
		"two columns":     "keep\tKSKEEP-X-A-1",
		"unknown kind":    "secret\tKSKEEP-X-A-1\twhat",
		"duplicate":       "keep\tKSKEEP-X-A-1\twhat\nkeep\tKSKEEP-X-A-1\tagain",
		"empty what":      "keep\tKSKEEP-X-A-1\t",
		"no entries":      "# only a comment\n",
		"space separated": "keep KSKEEP-X-A-1 what",
		"unknown expect":  "keep\tKSKEEP-X-A-1\twhat\tpayload",
		"five columns":    "keep\tKSKEEP-X-A-1\twhat\twire\textra",
	}
	for name, text := range cases {
		if _, err := Parse("x", text); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestExpectDefaultsToStream(t *testing.T) {
	s := MustParse("x", "keep\tKSKEEP-X-A-1\tseen\nkeep\tKSKEEP-X-B-1\tsent only\twire\nkeep\tKSKEEP-X-C-1\texplicit\tstream")
	want := []Expect{Stream, Wire, Stream}
	for i, m := range s.Markers {
		if m.Expect != want[i] {
			t.Errorf("%s: expect %q, want %q", m.Value, m.Expect, want[i])
		}
	}
}

func TestFieldSelection(t *testing.T) {
	s := MustParse("catalog", strings.Join([]string{
		"# comment",
		"keep\tKSKEEP-CATALOG-SKU-WOOL\tsku",
		"",
		"keep\tKSKEEP-CATALOG-SKU-SILK\tsku",
		"vanish\tKSKEEP-CATALOG-TOKEN-1\ttoken",
	}, "\n"))
	if got := s.Field("SKU"); len(got) != 2 || got[0] != "KSKEEP-CATALOG-SKU-WOOL" {
		t.Fatalf("Field(SKU) = %v", got)
	}
	if got := s.One("TOKEN"); got != "KSKEEP-CATALOG-TOKEN-1" {
		t.Fatalf("One(TOKEN) = %q", got)
	}
	if s.Markers[2].Kind != Vanish {
		t.Fatalf("kind = %q", s.Markers[2].Kind)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("One(SKU) with two values must panic")
		}
	}()
	s.One("SKU")
}
