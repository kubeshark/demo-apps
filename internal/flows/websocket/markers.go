// Package websocket is the websocket flow: the gateway opens a session with
// the live service over an HTTP/1.1 upgrade, subscribes to a channel and
// receives server pushes until it unsubscribes.
package websocket

import (
	_ "embed"

	"github.com/kubeshark/demo-apps/internal/markers"
)

const Name = "websocket"

//go:embed markers.tsv
var markersTSV string

var Markers = markers.MustParse(Name, markersTSV)

const UserHeader = "X-Demo-User"

// Message is every text frame the client sends: subscribe, ping, unsubscribe.
type Message struct {
	Type    string `json:"type"`
	Channel string `json:"channel,omitempty"`
	DelayMs int    `json:"delayMs,omitempty"`
}

// Event is what the server pushes after a subscribe.
type Event struct {
	Channel string `json:"channel"`
	Seq     int    `json:"seq"`
	Payload string `json:"payload"`
}
