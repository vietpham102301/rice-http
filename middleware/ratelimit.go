package middleware

import (
	"context"
	"math"
	"time"
)

// Limit allows Rate requests every Per, with bursts of up to Burst requests:
// Limit{Rate: 100, Per: time.Minute, Burst: 20} admits 20 at once, then one
// every 600ms. Burst 0 means Rate.
type Limit struct {
	Rate  int
	Per   time.Duration
	Burst int
}

// params returns the emission interval and the burst in nanoseconds and
// requests, or a message saying why the Limit cannot work.
func (l Limit) params() (interval, burst int64, msg string) {
	switch {
	case l.Rate <= 0:
		return 0, 0, "Rate must be positive"
	case l.Per <= 0:
		return 0, 0, "Per must be positive"
	case l.Burst < 0:
		return 0, 0, "Burst may not be negative"
	}
	interval = int64(l.Per) / int64(l.Rate)
	if interval == 0 {
		return 0, 0, "Per / Rate is below one nanosecond"
	}
	burst = int64(l.Burst)
	if burst == 0 {
		burst = int64(l.Rate)
	}
	if burst > math.MaxInt64/interval {
		return 0, 0, "Burst * Per / Rate overflows a time.Duration"
	}
	return interval, burst, ""
}

// RateLimitStore keeps the state RateLimit counts with. Take charges one
// request to key under GCRA with limit l and reports how long the caller must
// wait before it would be allowed; 0 means allowed now, and a request that is
// not allowed is not charged. The check and the charge are one atomic step:
// a store shared by several instances runs them as one operation on the
// shared state (a Lua script in Redis, say), never as a read and a later write.
//
// Keys are the caller's namespace: two RateLimit middleware sharing a store
// must not produce the same key for different purposes.
type RateLimitStore interface {
	Take(ctx context.Context, key string, l Limit) (wait time.Duration, err error)
}
