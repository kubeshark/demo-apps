// Package ledger is the http -> mssql flow: the gateway posts a double-entry
// pair to the ledger service and reads an account balance back; the ledger
// keeps its entries in Microsoft SQL Server.
package ledger

import (
	_ "embed"

	"github.com/kubeshark/demo-apps/internal/markers"
)

const Name = "ledger"

//go:embed markers.tsv
var markersTSV string

var Markers = markers.MustParse(Name, markersTSV)

const UserHeader = "X-Demo-User"
