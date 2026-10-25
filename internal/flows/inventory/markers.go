// Package inventory is the http -> grpc -> mongodb flow: the gateway reserves
// stock through the inventory service's gRPC API, which keeps stock levels and
// reservations in mongodb.
package inventory

import (
	_ "embed"
	"strings"

	"github.com/kubeshark/demo-apps/internal/markers"
)

const Name = "inventory"

//go:embed markers.tsv
var markersTSV string

var Markers = markers.MustParse(Name, markersTSV)

const UserMetadata = "x-demo-user"

type Item struct {
	SKU       string
	Name      string
	Warehouse string
	Available int64
}

// Items pairs SKU and NAME markers by their trailing segment.
func Items() []Item {
	names := map[string]string{}
	for _, n := range Markers.Field("NAME") {
		names[suffix(n)] = n
	}
	wh := Markers.One("WAREHOUSE")
	var out []Item
	for i, sku := range Markers.Field("SKU") {
		name, ok := names[suffix(sku)]
		if !ok {
			panic("inventory markers: SKU without matching NAME: " + sku)
		}
		out = append(out, Item{SKU: sku, Name: name, Warehouse: wh, Available: int64(100 + 50*i)})
	}
	return out
}

func suffix(v string) string {
	return v[strings.LastIndex(v, "-")+1:]
}
