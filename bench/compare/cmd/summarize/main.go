// Command summarize turns loadgen result lines on stdin into the Markdown
// tables of bench/results/M8-compare-e2e.txt on stdout.
package main

import (
	"fmt"
	"os"

	"github.com/vietpham102301/rice-http/bench/compare"
)

func main() {
	if err := compare.Summarize(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "summarize: %v\n", err)
		os.Exit(1)
	}
}
