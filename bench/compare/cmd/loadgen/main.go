// Command loadgen drives one server with one scenario and prints one result
// line: framework, scenario, round, requests per second, p50 and p99 in
// microseconds. It exits non-zero if the server is not ready or any response
// differs from the scenario's status.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/vietpham102301/rice-http/bench/compare"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "server address")
	scenario := flag.String("scenario", "static", "scenario name")
	framework := flag.String("framework", "", "framework label for the result line")
	round := flag.Int("round", 1, "round number for the result line")
	conns := flag.Int("conns", 64, "keep-alive connections")
	warmup := flag.Duration("warmup", 5*time.Second, "warm-up, not measured")
	duration := flag.Duration("duration", 10*time.Second, "measured duration")
	list := flag.Bool("list", false, "print scenario names and exit")
	flag.Parse()

	if *list {
		for _, s := range compare.Scenarios() {
			fmt.Println(s.Name)
		}
		return
	}
	if *framework == "" {
		fmt.Fprintln(os.Stderr, "loadgen: -framework is required")
		os.Exit(2)
	}
	if *duration <= 0 {
		fmt.Fprintf(os.Stderr, "loadgen: -duration must be positive, got %v\n", *duration)
		os.Exit(2)
	}
	if *warmup < 0 {
		fmt.Fprintf(os.Stderr, "loadgen: -warmup must not be negative, got %v\n", *warmup)
		os.Exit(2)
	}
	s, ok := compare.ScenarioByName(*scenario)
	if !ok {
		fmt.Fprintf(os.Stderr, "loadgen: unknown scenario %q\n", *scenario)
		os.Exit(2)
	}
	if err := compare.WaitReady(*addr, 10*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "loadgen: %v\n", err)
		os.Exit(1)
	}
	res, err := compare.Load(compare.LoadConfig{Addr: *addr, Scenario: s, Conns: *conns, Warmup: *warmup, Duration: *duration})
	if err != nil {
		fmt.Fprintf(os.Stderr, "loadgen: %s %s: %v\n", *framework, *scenario, err)
		os.Exit(1)
	}
	fmt.Printf("%s %s %d %.0f %d %d\n", *framework, s.Name, *round, res.RPS, res.P50.Microseconds(), res.P99.Microseconds())
}
