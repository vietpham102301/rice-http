package middleware

import (
	"context"
	"hash/maphash"
	"math"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// testStore returns a MemoryStore whose clock reads *at past its creation.
func testStore(at *time.Duration) *MemoryStore {
	s := NewMemoryStore()
	s.now = func() time.Time { return s.base.Add(*at) }
	return s
}

// take calls Take and fails the test on an error.
func take(t *testing.T, s *MemoryStore, key string, l Limit) time.Duration {
	t.Helper()
	wait, err := s.Take(context.Background(), key, l)
	if err != nil {
		t.Fatalf("Take(%q): %v", key, err)
	}
	return wait
}

// admitted takes n times at the current instant and returns how many were
// allowed.
func admitted(t *testing.T, s *MemoryStore, key string, l Limit, n int) int {
	t.Helper()
	ok := 0
	for range n {
		if take(t, s, key, l) == 0 {
			ok++
		}
	}
	return ok
}

// tenPerSecond is an emission interval of 100ms and a burst of 3.
var tenPerSecond = Limit{Rate: 10, Per: time.Second, Burst: 3}

func TestMemoryStoreAdmitsABurstThenOneEveryInterval(t *testing.T) {
	var at time.Duration
	s := testStore(&at)

	if got := admitted(t, s, "k", tenPerSecond, 3); got != 3 {
		t.Fatalf("a fresh key admitted %d of 3, want the whole burst", got)
	}
	if wait := take(t, s, "k", tenPerSecond); wait != 100*time.Millisecond {
		t.Fatalf("the first request past the burst waits %v, want 100ms", wait)
	}
	at = 100 * time.Millisecond
	if wait := take(t, s, "k", tenPerSecond); wait != 0 {
		t.Fatalf("after waiting the interval, wait %v, want 0", wait)
	}
	if wait := take(t, s, "k", tenPerSecond); wait != 100*time.Millisecond {
		t.Fatalf("the next one waits %v, want 100ms", wait)
	}
	at += 300 * time.Millisecond // Burst * interval of idleness
	if got := admitted(t, s, "k", tenPerSecond, 4); got != 3 {
		t.Fatalf("after idling, admitted %d of 4, want the burst of 3 back", got)
	}
}

func TestMemoryStoreBurst(t *testing.T) {
	var at time.Duration
	s := testStore(&at)

	five := Limit{Rate: 5, Per: time.Second}
	if got := admitted(t, s, "zero", five, 10); got != 5 {
		t.Errorf("Burst 0 admitted %d, want Rate (5)", got)
	}
	if wait := take(t, s, "zero", five); wait != 200*time.Millisecond {
		t.Errorf("Burst 0: wait %v, want 200ms", wait)
	}
	one := Limit{Rate: 5, Per: time.Second, Burst: 1}
	if got := admitted(t, s, "one", one, 10); got != 1 {
		t.Errorf("Burst 1 admitted %d, want 1", got)
	}
	if wait := take(t, s, "one", one); wait != 200*time.Millisecond {
		t.Errorf("Burst 1: wait %v, want 200ms", wait)
	}
}

func TestMemoryStoreDoesNotChargeADeniedRequest(t *testing.T) {
	var at time.Duration
	s := testStore(&at)

	admitted(t, s, "k", tenPerSecond, 3)
	for range 50 {
		take(t, s, "k", tenPerSecond)
	}
	if wait := take(t, s, "k", tenPerSecond); wait != 100*time.Millisecond {
		t.Errorf("after hammering while limited, wait %v, want still 100ms", wait)
	}
}

func TestMemoryStoreKeysAndLimitsAreIndependent(t *testing.T) {
	var at time.Duration
	s := testStore(&at)

	admitted(t, s, "a", tenPerSecond, 3)
	if got := admitted(t, s, "b", tenPerSecond, 3); got != 3 {
		t.Errorf("key b admitted %d of 3 after key a used its burst", got)
	}
	slow := Limit{Rate: 1, Per: time.Hour, Burst: 2}
	if got := admitted(t, s, "c", slow, 3); got != 2 {
		t.Errorf("key c under its own Limit admitted %d, want 2", got)
	}
}

func TestMemoryStoreClockEarlierThanTheStoredTime(t *testing.T) {
	at := 500 * time.Millisecond
	s := testStore(&at)

	admitted(t, s, "k", tenPerSecond, 3)
	at = 400 * time.Millisecond // a request that read the clock before another
	if wait := take(t, s, "k", tenPerSecond); wait != 200*time.Millisecond {
		t.Errorf("wait %v, want 200ms: the burst is spent until 600ms", wait)
	}
}

func TestMemoryStoreRejectsALimitThatCannotWork(t *testing.T) {
	cases := map[string]Limit{
		"zero Rate":         {Per: time.Second},
		"zero Per":          {Rate: 1},
		"negative Burst":    {Rate: 1, Per: time.Second, Burst: -1},
		"sub-ns interval":   {Rate: 2, Per: 1},
		"overflowing burst": {Rate: 1, Per: time.Hour, Burst: math.MaxInt},
	}
	for name, l := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewMemoryStore().Take(context.Background(), "k", l)
			if err == nil || !strings.HasPrefix(err.Error(), "rice: middleware.MemoryStore:") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// sameShard returns n distinct keys that land in one shard.
func sameShard(s *MemoryStore, n int) []string {
	var keys []string
	want := maphash.String(s.seed, "k0") % memoryShards
	for i := 0; len(keys) < n; i++ {
		k := "k" + strconv.Itoa(i)
		if maphash.String(s.seed, k)%memoryShards == want {
			keys = append(keys, k)
		}
	}
	return keys
}

func TestMemoryStoreSweepsExpiredKeys(t *testing.T) {
	var at time.Duration
	s := testStore(&at)
	s.sweepEvery = time.Second

	keys := sameShard(s, 3)
	expiring, lasting, trigger := keys[0], keys[1], keys[2]
	take(t, s, expiring, tenPerSecond)                  // tat 100ms
	take(t, s, lasting, Limit{Rate: 1, Per: time.Hour}) // tat 1h
	sh := &s.shards[maphash.String(s.seed, expiring)%memoryShards]

	at = 999 * time.Millisecond
	take(t, s, trigger, tenPerSecond)
	if _, ok := sh.tat[expiring]; !ok {
		t.Fatal("swept before the sweep interval")
	}
	at = time.Second
	take(t, s, trigger, tenPerSecond)
	if _, ok := sh.tat[expiring]; ok {
		t.Error("an expired key survived the sweep")
	}
	if _, ok := sh.tat[lasting]; !ok {
		t.Error("the sweep deleted a key that has not expired")
	}
}

func TestMemoryStoreConcurrentTakesAdmitExactlyTheBurst(t *testing.T) {
	var at time.Duration
	s := testStore(&at)
	l := Limit{Rate: 10, Per: time.Second, Burst: 8}

	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for range 64 {
		wg.Go(func() {
			if wait, _ := s.Take(context.Background(), "k", l); wait == 0 {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if ok != 8 {
		t.Errorf("64 concurrent takes admitted %d, want exactly the burst (8)", ok)
	}
}
