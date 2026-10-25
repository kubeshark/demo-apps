// Package identity is the http -> ldap + radius flow: the gateway logs a user
// in through the identity service, which binds, searches and modifies that
// user's entry in OpenLDAP and then authenticates and accounts the same
// session against FreeRADIUS.
package identity

import (
	_ "embed"
	"strings"

	"github.com/kubeshark/demo-apps/internal/markers"
)

const Name = "identity"

//go:embed markers.tsv
var markersTSV string

var Markers = markers.MustParse(Name, markersTSV)

type Account struct {
	User     string
	Password string
}

// Accounts pairs USER and PASSWORD markers by their trailing segment.
func Accounts() []Account {
	passwords := map[string]string{}
	for _, p := range Markers.Field("PASSWORD") {
		passwords[suffix(p)] = p
	}
	var out []Account
	for _, u := range Markers.Field("USER") {
		p, ok := passwords[suffix(u)]
		if !ok {
			panic("identity markers: USER without matching PASSWORD: " + u)
		}
		out = append(out, Account{User: u, Password: p})
	}
	return out
}

func suffix(v string) string {
	return v[strings.LastIndex(v, "-")+1:]
}
