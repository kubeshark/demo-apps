package websocket

import "github.com/kubeshark/demo-apps/internal/flowrun"

func (f *Flow) Filters() []flowrun.Filter {
	return []flowrun.Filter{
		{Variant: flowrun.OK, Title: "The upgrade request", KFL: `http && dst.service.name == "{release}-live" && status_code == 101`},
		{Variant: flowrun.OK, Title: "Every frame of the session", KFL: `ws && dst.service.name == "{release}-live"`},
		{Variant: flowrun.Slow, Title: "Frames of the delayed session", KFL: `ws && dst.service.name == "{release}-live"`},
		{Variant: flowrun.Error, Title: "Close frames; the error path closes with 1008", KFL: `ws && dst.service.name == "{release}-live" && ws_opcode == "Close"`},
	}
}
