//go:build race

package middleware_test

// raceDetector reports whether this test binary was built with -race. Alloc
// budgets that touch a sync.Pool read it to relax their bound accordingly —
// see TestAllocBudgetLogger.
const raceDetector = true
