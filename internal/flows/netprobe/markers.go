// Package netprobe is the http -> dns, icmp, https flow: the gateway asks the
// netprobe service to probe the network, and the netprobe resolves names in
// the demo zone held by a dedicated coredns, pings its neighbours and fetches
// external https urls.
package netprobe

import (
	_ "embed"

	"github.com/kubeshark/demo-apps/internal/markers"
)

const Name = "netprobe"

//go:embed markers.tsv
var markersTSV string

var Markers = markers.MustParse(Name, markersTSV)

const ProbeHeader = "X-Demo-Probe"
