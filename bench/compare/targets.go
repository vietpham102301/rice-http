package compare

import (
	"net"
	"net/http"

	"github.com/valyala/fasthttp"
)

// Server is one app of one framework, reachable in-process through exactly one
// of HTTP and Fasthttp, or over a listener through Serve.
type Server struct {
	HTTP     http.Handler
	Fasthttp fasthttp.RequestHandler
	// Serve serves on ln until the process exits.
	Serve func(ln net.Listener) error
}

// Target is one framework under comparison.
type Target struct {
	Name  string
	Build func(App) Server
}

// Targets returns every framework, in the order results are reported.
func Targets() []Target {
	return []Target{Rice()}
}

// TargetByName returns the framework with the given name.
func TargetByName(name string) (Target, bool) {
	for _, t := range Targets() {
		if t.Name == name {
			return t, true
		}
	}
	return Target{}, false
}
