// Package telco is the http -> diameter + sctp flow: the gateway asks the
// telco service to charge a subscriber, and the telco service runs a
// Device-Watchdog and a Credit-Control exchange with the telco server over
// Diameter on TCP and on SCTP, then echoes a payload over plain SCTP.
package telco

import (
	_ "embed"

	"github.com/fiorix/go-diameter/v4/diam"
	"github.com/fiorix/go-diameter/v4/diam/datatype"

	"github.com/kubeshark/demo-apps/internal/markers"
)

const Name = "telco"

//go:embed markers.tsv
var markersTSV string

var Markers = markers.MustParse(Name, markersTSV)

// Diameter identities of the two ends. The server accepts only peers from
// its own realm.
const (
	Realm      = "demo.local"
	ClientHost = "telco." + Realm
	ServerHost = "telco-server." + Realm

	// CreditControlApp is the Diameter Credit-Control application id.
	CreditControlApp = 4
)

// add appends a vendor-less AVP to m.
func add(m *diam.Message, code uint32, flags uint8, data datatype.Type) {
	m.AddAVP(diam.NewAVP(code, flags, 0, data))
}
