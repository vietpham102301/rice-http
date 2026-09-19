// Command server serves one framework's app for one scenario over TCP until it
// is killed. Every framework is served by its own server: rice's Serve, Gin's
// and Echo's net/http.Server, Fiber's Listener.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/vietpham102301/rice-http/bench/compare"
)

func main() {
	framework := flag.String("framework", "rice", "rice, gin, echo or fiber")
	scenario := flag.String("scenario", "static", "scenario whose app to serve")
	addr := flag.String("addr", "127.0.0.1:18080", "listen address")
	flag.Parse()

	t, ok := compare.TargetByName(*framework)
	if !ok {
		fmt.Fprintf(os.Stderr, "server: unknown framework %q\n", *framework)
		os.Exit(2)
	}
	s, ok := compare.ScenarioByName(*scenario)
	if !ok {
		fmt.Fprintf(os.Stderr, "server: unknown scenario %q\n", *scenario)
		os.Exit(2)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
	if err := t.Build(s.App).Serve(ln); err != nil {
		fmt.Fprintf(os.Stderr, "server: %v\n", err)
		os.Exit(1)
	}
}
