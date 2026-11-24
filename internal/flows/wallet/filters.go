package wallet

import "github.com/kubeshark/demo-apps/internal/flowrun"

func (f *Flow) Filters() []flowrun.Filter {
	return []flowrun.Filter{
		{Variant: flowrun.OK, Title: "Every hop of the flow", KFL: `"{namespace}" in namespaces && (dst.service.name == "{release}-wallet" || (redis && src.pod.name.startsWith("{release}-wallet")))`},
		{Variant: flowrun.OK, Title: "Wallet requests", KFL: `http && dst.service.name == "{release}-wallet"`},
		{Variant: flowrun.OK, Title: "Redis AUTH as the wallet user", KFL: `redis && src.pod.name.startsWith("{release}-wallet")`},
		{Variant: flowrun.OK, Title: "Redacted request headers", KFL: `http && dst.service.name == "{release}-wallet" && request.headers.exists(k, request.headers[k] == "[REDACTED]")`, Redaction: true},
		{Variant: flowrun.OK, Title: "Redacted cookies", KFL: `http && dst.service.name == "{release}-wallet" && (request.cookies.exists(k, request.cookies[k] == "[REDACTED]") || response.cookies.exists(k, response.cookies[k] == "[REDACTED]"))`, Redaction: true},
		{Variant: flowrun.OK, Title: "Redacted query token", KFL: `http && dst.service.name == "{release}-wallet" && query_string["token"] == "[REDACTED]"`, Redaction: true},
		{Variant: flowrun.Slow, Title: "Statement uploads", KFL: `http && dst.service.name == "{release}-wallet" && path == "/statements"`},
		{Variant: flowrun.Slow, Title: "The upload past the 2Mi scan budget", KFL: `http && dst.service.name == "{release}-wallet" && path == "/statements" && request_body_size > 2097152`},
		{Variant: flowrun.Error, Title: "Revoked API key answered 401", KFL: `http && dst.service.name == "{release}-wallet" && path == "/profile" && status_code == 401`},
	}
}
