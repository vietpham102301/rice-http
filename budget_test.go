package rice

import "testing"

// budget asserts that fn allocates no more than want objects per call.
//
// The response buffers are warmed before measuring, because a live server is
// warm. Measuring a cold buffer would measure one-time setup, not steady state.
//
// Under -race, sync.Pool drops one Put in four, so a pooled dispatch really does
// allocate a fraction of a Ctx per call there. AllocsPerRun divides integer
// counts and reports that fraction as 0, while a genuine per-request allocation
// still reads as at least 1. The zero budgets are meaningful in both modes; see
// newCtx for why that holds only while newCtx allocates three objects or fewer.
//
// This helper is not build-tagged: TestAllocBudgetMethodIndex in method_test.go
// uses it too, and methodIndex's allocation behaviour has nothing to do with
// ricedebug. alloc_test.go, whose budgets are about the pooled Ctx, is excluded
// from the ricedebug build; this file, which only holds the shared helper, is not.
func budget(t *testing.T, name string, want float64, fn func()) {
	t.Helper()
	fn() // warm
	if got := testing.AllocsPerRun(1000, fn); got > want {
		t.Errorf("%s allocated %.1f objects per call, budget is %.0f", name, got, want)
	}
}
