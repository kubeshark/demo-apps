// Package orders is the http -> postgresql -> kafka -> amqp flow: the gateway
// places an order with the orders service, which stores it in postgresql and
// publishes an event to kafka; the notifier consumes that event and relays it
// through rabbitmq.
package orders

import (
	_ "embed"

	"github.com/kubeshark/demo-apps/internal/markers"
)

const Name = "orders"

//go:embed markers.tsv
var markersTSV string

var Markers = markers.MustParse(Name, markersTSV)

const (
	Topic      = "orders"
	Exchange   = "notifications"
	Queue      = "notifications"
	RoutingKey = "order.created"
)

// Order is the request body of POST /orders and the row stored in postgresql.
type Order struct {
	ID       string `json:"id"`
	Customer string `json:"customer"`
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
	Note     string `json:"note"`
}

// Event is the kafka record and the amqp message body for one order.
type Event struct {
	Type  string `json:"type"`
	Order Order  `json:"order"`
}

// Notification is what the notifier recorded for one delivered event.
type Notification struct {
	OrderID  string `json:"order_id"`
	Customer string `json:"customer"`
	SKU      string `json:"sku"`
	Note     string `json:"note"`
}
