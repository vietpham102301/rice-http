package rice

import "testing"

// budget asserts that fn allocates no more than want objects per call.
//
// The response buffers are warmed before measuring, because a live server is
// warm. Measuring a cold buffer would measure one-time setup, not steady state.
//
// Under -race, sync.Pool drops one Put in four, so a pooled dispatch really does
// allocate a fraction of a Ctx per call there: measured on this package, 0.75 per
// call for a route with a parameter and 0.50 for a static one, since newCtx
// allocates three objects in the first case and two in the second. AllocsPerRun
// divides integer counts and reports either as 0, while a genuine per-request
// allocation adds a full 1 and still fails. That much is solid in both modes.
//
// What the fraction does not do is police newCtx. A fourth object there takes the
// parameterised figure to about 1.0, which is the boundary, so those budgets go
// red only on some runs, and the static budgets keep passing outright.
// TestNewCtxStaysWithinThreeAllocations is what actually holds the ceiling.
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
