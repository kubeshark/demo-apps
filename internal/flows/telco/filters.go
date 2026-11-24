package telco

import "github.com/kubeshark/demo-apps/internal/flowrun"

func (f *Flow) Filters() []flowrun.Filter {
	return []flowrun.Filter{
		{Variant: flowrun.OK, Title: "Every hop of the flow", KFL: `"{namespace}" in namespaces && (dst.service.name == "{release}-telco" || dst.service.name == "{release}-telco-server")`},
		{Variant: flowrun.OK, Title: "Gateway to telco", KFL: `http && dst.service.name == "{release}-telco"`},
		{Variant: flowrun.OK, Title: "Diameter exchanges", KFL: `diameter && "{namespace}" in namespaces`},
		{Variant: flowrun.OK, Title: "SCTP associations", KFL: `sctp && "{namespace}" in namespaces`},
		{Variant: flowrun.Slow, Title: "The held answer", KFL: `diameter && "{namespace}" in namespaces && elapsed_time > 1500000`},
		{Variant: flowrun.Error, Title: "Telco answering 502", KFL: `http && dst.service.name == "{release}-telco" && status_code == 502`},
	}
}
