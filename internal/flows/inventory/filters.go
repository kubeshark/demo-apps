package inventory

import "github.com/kubeshark/demo-apps/internal/flowrun"

func (f *Flow) Filters() []flowrun.Filter {
	return []flowrun.Filter{
		{Variant: flowrun.OK, Title: "Every hop of the flow", KFL: `"{namespace}" in namespaces && (dst.service.name == "{release}-inventory" || dst.service.name == "{release}-mongodb")`},
		{Variant: flowrun.OK, Title: "gRPC calls to inventory", KFL: `grpc && dst.service.name == "{release}-inventory"`},
		{Variant: flowrun.OK, Title: "Reservations", KFL: `grpc && dst.service.name == "{release}-inventory" && grpc_method == "Reserve"`},
		{Variant: flowrun.OK, Title: "MongoDB commands", KFL: `mongodb && dst.service.name == "{release}-mongodb"`},
		{Variant: flowrun.Slow, Title: "The delayed GetStock", KFL: `grpc && grpc_method == "GetStock" && elapsed_time > 1500000`},
		{Variant: flowrun.Error, Title: "Reserve refused with FailedPrecondition", KFL: `grpc && dst.service.name == "{release}-inventory" && grpc_status == 9`},
	}
}
