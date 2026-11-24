package catalog

import "github.com/kubeshark/demo-apps/internal/flowrun"

func (f *Flow) Filters() []flowrun.Filter {
	return []flowrun.Filter{
		{Variant: flowrun.OK, Title: "Every hop of the flow", KFL: `"{namespace}" in namespaces && (dst.service.name == "{release}-catalog" || dst.service.name == "{release}-redis" || dst.service.name == "{release}-mysql")`},
		{Variant: flowrun.OK, Title: "Gateway to catalog", KFL: `http && dst.service.name == "{release}-catalog"`},
		{Variant: flowrun.OK, Title: "Cache reads and writes", KFL: `redis && src.pod.name.startsWith("{release}-catalog")`},
		{Variant: flowrun.OK, Title: "Database queries", KFL: `mysql && dst.service.name == "{release}-mysql"`},
		{Variant: flowrun.Slow, Title: "The 2s query", KFL: `mysql && dst.service.name == "{release}-mysql" && mysql_query.contains("SLEEP")`},
		{Variant: flowrun.Slow, Title: "Slow catalog responses", KFL: `http && dst.service.name == "{release}-catalog" && elapsed_time > 1500000`},
		{Variant: flowrun.Error, Title: "Missing table", KFL: `mysql && dst.service.name == "{release}-mysql" && mysql_error_code == 1146`},
		{Variant: flowrun.Error, Title: "Catalog answering 500", KFL: `http && dst.service.name == "{release}-catalog" && status_code == 500`},
	}
}
