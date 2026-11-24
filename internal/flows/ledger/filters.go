package ledger

import "github.com/kubeshark/demo-apps/internal/flowrun"

func (f *Flow) Filters() []flowrun.Filter {
	return []flowrun.Filter{
		{Variant: flowrun.OK, Title: "Gateway to ledger", KFL: `http && dst.service.name == "{release}-ledger"`},
		{Variant: flowrun.OK, Title: "MSSQL traffic, no TDS dissector yet", KFL: `"{namespace}" in namespaces && dst.service.name == "{release}-mssql"`},
		{Variant: flowrun.Slow, Title: "Slow ledger responses", KFL: `http && dst.service.name == "{release}-ledger" && elapsed_time > 1500000`},
		{Variant: flowrun.Error, Title: "Ledger answering 500", KFL: `http && dst.service.name == "{release}-ledger" && status_code == 500`},
	}
}
