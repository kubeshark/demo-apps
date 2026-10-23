// Package catalog is the http -> redis -> mysql flow: the gateway asks the
// catalog service for a product, the catalog answers from redis when it can
// and from mysql otherwise.
package catalog

import (
	_ "embed"

	"github.com/kubeshark/demo-apps/internal/markers"
)

const Name = "catalog"

//go:embed markers.tsv
var markersTSV string

var Markers = markers.MustParse(Name, markersTSV)

const UserHeader = "X-Demo-User"
