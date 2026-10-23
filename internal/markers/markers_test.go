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
	}
	for name, text := range cases {
		if _, err := Parse("x", text); err == nil {
			t.Errorf("%s: expected error", name)
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
