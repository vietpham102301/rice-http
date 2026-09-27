package middleware

import (
	"context"
	"errors"
	"hash/maphash"
	"sync"
	"time"
)

// memoryShards is how many independently locked maps a MemoryStore spreads
// its keys over.
const memoryShards = 64

// MemoryStore is a RateLimitStore in this process's memory: each instance of
// a service counts on its own. Build it with NewMemoryStore; it needs no
// Close.
//
// It starts no goroutine. A key whose theoretical arrival time has passed is
// the same as an absent one, so each shard deletes those keys at most once a
// minute, while serving a Take. Its memory is the keys active in the last
// Burst*Per/Rate, plus up to a minute of expired ones.
type MemoryStore struct {
	seed       maphash.Seed
	base       time.Time
	now        func() time.Time
	sweepEvery time.Duration
	shards     [memoryShards]memoryShard
}

type memoryShard struct {
	mu        sync.Mutex
	tat       map[string]int64 // key → theoretical arrival time, ns since base
	lastSweep int64
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	s := &MemoryStore{seed: maphash.MakeSeed(), base: time.Now(), now: time.Now, sweepEvery: time.Minute}
	for i := range s.shards {
		s.shards[i].tat = make(map[string]int64)
	}
	return s
}

// Take implements RateLimitStore. A Limit that RateLimit would refuse at
// construction returns an error.
func (s *MemoryStore) Take(_ context.Context, key string, l Limit) (time.Duration, error) {
	interval, burst, msg := l.params()
	if msg != "" {
		return 0, errors.New("rice: middleware.MemoryStore: " + msg)
	}
	// Measured on the monotonic clock from the store's creation, so a step of
	// the wall clock neither releases nor locks out every client.
	now := int64(s.now().Sub(s.base))

	sh := &s.shards[maphash.String(s.seed, key)%memoryShards]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if now-sh.lastSweep >= int64(s.sweepEvery) {
		for k, tat := range sh.tat {
			if tat <= now {
				delete(sh.tat, k)
			}
		}
		sh.lastSweep = now
	}

	tat, ok := sh.tat[key]
	if !ok || tat < now {
		tat = now
	}
	next := tat + interval
	if allowAt := next - burst*interval; now < allowAt {
		return time.Duration(allowAt - now), nil
	}
	sh.tat[key] = next
	return 0, nil
}
