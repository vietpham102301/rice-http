package compare

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

type sample struct {
	rps      float64
	p50, p99 float64
}

// Summarize reads end-to-end result lines, one per round:
//
//	<framework> <scenario> <round> <rps> <p50µs> <p99µs>
//
// and writes one Markdown table per scenario that has samples, in Scenarios()
// order, with one row per framework in Targets() order: the median req/s, its
// min–max across rounds, and the median p50 and p99 in microseconds.
func Summarize(r io.Reader, w io.Writer) error {
	samples := map[string]map[string][]sample{} // scenario → framework → rounds
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		if len(f) != 6 {
			return fmt.Errorf("line %d: want 6 fields, got %d: %q", line, len(f), sc.Text())
		}
		var nums [4]float64
		if _, err := strconv.Atoi(f[2]); err != nil {
			return fmt.Errorf("line %d: round %q: %w", line, f[2], err)
		}
		for i, s := range f[3:] {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return fmt.Errorf("line %d: field %d %q: %w", line, i+4, s, err)
			}
			nums[i] = v
		}
		fw, scen := f[0], f[1]
		if samples[scen] == nil {
			samples[scen] = map[string][]sample{}
		}
		samples[scen][fw] = append(samples[scen][fw], sample{rps: nums[0], p50: nums[1], p99: nums[2]})
	}
	if err := sc.Err(); err != nil {
		return err
	}

	for _, s := range Scenarios() {
		byFw := samples[s.Name]
		if len(byFw) == 0 {
			continue
		}
		fmt.Fprintf(w, "## %s\n\n", s.Name)
		fmt.Fprintln(w, "| framework | req/s (median) | req/s (min–max) | p50 µs (median) | p99 µs (median) |")
		fmt.Fprintln(w, "| --- | ---: | --- | ---: | ---: |")
		for _, t := range Targets() {
			rounds := byFw[t.Name]
			if len(rounds) == 0 {
				continue
			}
			rps := pick(rounds, func(x sample) float64 { return x.rps })
			fmt.Fprintf(w, "| %s | %.0f | %.0f–%.0f | %.0f | %.0f |\n", t.Name,
				median(rps), rps[0], rps[len(rps)-1],
				median(pick(rounds, func(x sample) float64 { return x.p50 })),
				median(pick(rounds, func(x sample) float64 { return x.p99 })))
		}
		fmt.Fprintln(w)
	}
	return nil
}

// pick returns field of every sample, sorted ascending.
func pick(xs []sample, field func(sample) float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = field(x)
	}
	sort.Float64s(out)
	return out
}

// median of a sorted, non-empty slice.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
