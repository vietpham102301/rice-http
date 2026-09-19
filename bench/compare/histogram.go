package compare

import (
	"math"
	"math/bits"
	"time"
)

// Histogram counts durations in log-linear buckets: below 16ns one bucket per
// nanosecond, above that 16 buckets per power of two, so a recorded value is
// reported within 1/16 (6.25%) below its true value. Record never allocates,
// so the load generator's measuring path adds no garbage to the machine it is
// measuring.
type Histogram struct {
	counts [1024]uint64
	n      uint64
}

func bucketOf(ns uint64) int {
	if ns < 16 {
		return int(ns)
	}
	e := bits.Len64(ns) - 1 // e >= 4
	sub := (ns >> (e - 4)) & 15
	return (e-3)*16 + int(sub)
}

// lowerBound is the smallest value in bucket i.
func lowerBound(i int) uint64 {
	if i < 16 {
		return uint64(i)
	}
	e := i/16 + 3
	sub := uint64(i % 16)
	return (16 + sub) << (e - 4)
}

// Record adds one duration. Negative durations count as zero.
func (h *Histogram) Record(d time.Duration) {
	if d < 0 {
		d = 0
	}
	h.counts[bucketOf(uint64(d))]++
	h.n++
}

// Merge adds every count of o to h.
func (h *Histogram) Merge(o *Histogram) {
	for i, c := range o.counts {
		h.counts[i] += c
	}
	h.n += o.n
}

// Count is the number of recorded durations.
func (h *Histogram) Count() uint64 { return h.n }

// Quantile returns the lower bound of the bucket holding the q-th quantile, or
// zero for an empty histogram.
func (h *Histogram) Quantile(q float64) time.Duration {
	if h.n == 0 {
		return 0
	}
	target := uint64(math.Ceil(q * float64(h.n)))
	if target == 0 {
		target = 1
	}
	var seen uint64
	for i, c := range h.counts {
		seen += c
		if seen >= target {
			return time.Duration(lowerBound(i))
		}
	}
	return time.Duration(lowerBound(len(h.counts) - 1))
}
