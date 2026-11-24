package netprobe

import "github.com/kubeshark/demo-apps/internal/flowrun"

func (f *Flow) Filters() []flowrun.Filter {
	return []flowrun.Filter{
		{Variant: flowrun.OK, Title: "Gateway to netprobe", KFL: `http && dst.service.name == "{release}-netprobe"`},
		{Variant: flowrun.OK, Title: "DNS lookups", KFL: `dns && "{namespace}" in namespaces`},
		{Variant: flowrun.OK, Title: "ICMP echoes", KFL: `icmp && "{namespace}" in namespaces`},
		{Variant: flowrun.Slow, Title: "ICMP echoes", KFL: `icmp && "{namespace}" in namespaces`},
		{Variant: flowrun.Error, Title: "The NXDOMAIN lookup", KFL: `dns && "{namespace}" in namespaces && dns_questions.exists(q, q.matches("(?i)missing-ghost"))`},
		{Variant: flowrun.Error, Title: "Netprobe answering 502", KFL: `http && dst.service.name == "{release}-netprobe" && status_code == 502`},
	}
}
