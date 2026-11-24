package graphql

import "github.com/kubeshark/demo-apps/internal/flowrun"

func (f *Flow) Filters() []flowrun.Filter {
	return []flowrun.Filter{
		{Variant: flowrun.OK, Title: "Every hop of the flow", KFL: `"{namespace}" in namespaces && (dst.service.name == "{release}-graphql" || (postgresql && src.pod.name.startsWith("{release}-graphql")))`},
		{Variant: flowrun.OK, Title: "GraphQL operations", KFL: `gql && dst.service.name == "{release}-graphql"`},
		{Variant: flowrun.OK, Title: "Resolver queries in PostgreSQL", KFL: `postgresql && src.pod.name.startsWith("{release}-graphql")`},
		{Variant: flowrun.Slow, Title: "The 2s resolver query", KFL: `postgresql && src.pod.name.startsWith("{release}-graphql") && elapsed_time > 1500000`},
		{Variant: flowrun.Error, Title: "GraphQL errors answered 400", KFL: `gql && dst.service.name == "{release}-graphql" && status_code == 400`},
	}
}
