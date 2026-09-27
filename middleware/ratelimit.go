package middleware

import (
	"context"
	"math"
	"strconv"
	"time"

	rice "github.com/vietpham102301/rice-http"
)

// errTooManyRequests is the one error every rate-limited request returns.
var errTooManyRequests = &rice.HTTPError{Code: 429, Message: "Too Many Requests"}

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

// RateLimitConfig is what RateLimit needs. Limit is required.
//
// KeyFunc says whom a request counts against; nil means the client's address
// (see RateLimit). It may return any string, "" included, which is a key like
// any other. An error it returns goes to the error funnel. A limit per user
// reads the identity an auth middleware stored:
//
//	KeyFunc: func(c *rice.Ctx) (string, error) {
//		u, _ := userKey.Get(c)
//		return u.ID, nil
//	}
//
// Store holds the counts; nil means a new MemoryStore for this middleware
// alone. Two middleware sharing one Store must keep their keys apart, with a
// prefix, say.
type RateLimitConfig struct {
	Limit   Limit
	KeyFunc func(c *rice.Ctx) (string, error)
	Store   RateLimitStore
}

// RateLimit admits cfg.Limit's rate of requests per key and answers the rest
// with 429 Too Many Requests and a Retry-After of the whole seconds to wait,
// rounded up. It counts with GCRA: a key may send Burst requests at once, then
// one every Per/Rate, and a request that is refused does not count. An
// allowed request gets no header.
//
// The default key is c.ClientIP(): a whole IPv4 address, and an IPv6 address
// to its /64, since one IPv6 client is routinely given a whole /64. Install
// RealIP before it behind a proxy, or every request counts against the proxy.
//
// An error from KeyFunc or the Store goes to the error funnel, which answers
// 500: a store that is down is not a client over its limit. To serve rather
// than refuse while a shared store is down, wrap it:
//
//	type failOpen struct{ middleware.RateLimitStore }
//
//	func (s failOpen) Take(ctx context.Context, key string, l middleware.Limit) (time.Duration, error) {
//		wait, err := s.RateLimitStore.Take(ctx, key, l)
//		if err != nil {
//			log.Printf("rate limit store: %v", err)
//			return 0, nil
//		}
//		return wait, nil
//	}
//
// Install it after RealIP and inside CORS, Logger and otelrice, so a 429
// carries the CORS headers and is logged and traced. By address, before
// BasicAuth or KeyAuth, it throttles credential guessing; by user, after them
// with a KeyFunc, it is a quota per identity. With app.Use it also counts
// route misses (ADR-0012). See ADR-0022.
//
// A Limit with a Rate or Per that is not positive, a negative Burst, a
// Per/Rate below one nanosecond or a Burst*Per/Rate beyond a time.Duration
// panics.
func RateLimit(cfg RateLimitConfig) rice.Middleware {
	if _, _, msg := cfg.Limit.params(); msg != "" {
		panic("rice: middleware.RateLimit: " + msg)
	}
	keyFunc, store, limit := cfg.KeyFunc, cfg.Store, cfg.Limit
	if keyFunc == nil {
		keyFunc = clientKey
	}
	if store == nil {
		store = NewMemoryStore()
	}

	return func(next rice.Handler) rice.Handler {
		return func(c *rice.Ctx) error {
			key, err := keyFunc(c)
			if err != nil {
				return err
			}
			wait, err := store.Take(c.Context(), key, limit)
			if err != nil {
				return err
			}
			if wait > 0 {
				c.SetHeader("Retry-After", strconv.FormatInt(retryAfter(wait), 10))
				return errTooManyRequests
			}
			return next(c)
		}
	}
}

// retryAfter is wait in whole seconds, rounded up so that a client that waits
// as told is admitted, and at least 1.
func retryAfter(wait time.Duration) int64 {
	return max(int64((wait+time.Second-1)/time.Second), 1)
}

// clientKey is the default key: the client's IPv4 address, or its IPv6
// address's /64, as the 16 bytes of the IPv6 form. IPv4 and IPv4-mapped IPv6
// addresses are one key.
func clientKey(c *rice.Ctx) (string, error) {
	ip := c.ClientIP()
	var k [16]byte
	if v4 := ip.To4(); v4 != nil {
		k[10], k[11] = 0xff, 0xff
		copy(k[12:], v4)
	} else if len(ip) == 16 {
		copy(k[:8], ip[:8])
	}
	return string(k[:]), nil
}
