package orders

import "github.com/kubeshark/demo-apps/internal/flowrun"

func (f *Flow) Filters() []flowrun.Filter {
	return []flowrun.Filter{
		{Variant: flowrun.OK, Title: "Every hop of the flow", KFL: `"{namespace}" in namespaces && (dst.service.name == "{release}-orders" || dst.service.name == "{release}-postgresql" || dst.service.name == "{release}-kafka" || dst.service.name == "{release}-rabbitmq")`},
		{Variant: flowrun.OK, Title: "Gateway to orders", KFL: `http && dst.service.name == "{release}-orders"`},
		{Variant: flowrun.OK, Title: "Order rows in PostgreSQL", KFL: `postgresql && src.pod.name.startsWith("{release}-orders")`},
		{Variant: flowrun.OK, Title: "Kafka produce and fetch", KFL: `kafka && dst.service.name == "{release}-kafka"`},
		{Variant: flowrun.OK, Title: "RabbitMQ relay", KFL: `amqp && dst.service.name == "{release}-rabbitmq"`},
		{Variant: flowrun.Slow, Title: "The 2s query", KFL: `postgresql && src.pod.name.startsWith("{release}-orders") && postgresql_query.contains("pg_sleep")`},
		{Variant: flowrun.Error, Title: "CHECK constraint violation", KFL: `postgresql && src.pod.name.startsWith("{release}-orders") && postgresql_error_code == "23514"`},
		{Variant: flowrun.Error, Title: "Orders answering 422", KFL: `http && dst.service.name == "{release}-orders" && status_code == 422`},
	}
}
