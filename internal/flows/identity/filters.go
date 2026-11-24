package identity

import "github.com/kubeshark/demo-apps/internal/flowrun"

func (f *Flow) Filters() []flowrun.Filter {
	return []flowrun.Filter{
		{Variant: flowrun.OK, Title: "Every hop of the flow", KFL: `"{namespace}" in namespaces && (dst.service.name == "{release}-identity" || dst.service.name == "{release}-openldap" || dst.service.name == "{release}-freeradius")`},
		{Variant: flowrun.OK, Title: "Logins", KFL: `http && dst.service.name == "{release}-identity" && path == "/login"`},
		{Variant: flowrun.OK, Title: "LDAP binds", KFL: `ldap && dst.service.name == "{release}-openldap" && ldap_type == "Bind Request"`},
		{Variant: flowrun.OK, Title: "RADIUS access and accounting", KFL: `radius && dst.service.name == "{release}-freeradius"`},
		{Variant: flowrun.Slow, Title: "Logins slowed between LDAP and RADIUS", KFL: `http && dst.service.name == "{release}-identity" && elapsed_time > 1500000`},
		{Variant: flowrun.Error, Title: "Logins refused", KFL: `http && dst.service.name == "{release}-identity" && status_code == 401`},
	}
}
