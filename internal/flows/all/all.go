// Package all links every flow into the gateway. A flow registers itself in
// its init function; importing it here is what makes it available.
package all

import (
	_ "github.com/kubeshark/demo-apps/internal/flows/catalog"
	_ "github.com/kubeshark/demo-apps/internal/flows/graphql"
	_ "github.com/kubeshark/demo-apps/internal/flows/identity"
	_ "github.com/kubeshark/demo-apps/internal/flows/inventory"
	_ "github.com/kubeshark/demo-apps/internal/flows/ledger"
	_ "github.com/kubeshark/demo-apps/internal/flows/netprobe"
	_ "github.com/kubeshark/demo-apps/internal/flows/orders"
	_ "github.com/kubeshark/demo-apps/internal/flows/telco"
	_ "github.com/kubeshark/demo-apps/internal/flows/websocket"
)
