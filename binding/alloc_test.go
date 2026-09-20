package binding_test

import (
	"testing"

	rice "github.com/vietpham102301/rice-http"
	"github.com/vietpham102301/rice-http/binding"
)

// TestAllocBudgetJSONBinding pins binding.JSON's cost exactly, the way
// TestAllocBudgetJSON pins c.JSON's in package rice: this package buys
// ergonomics with allocations and says how many. Pinned rather than bounded, so
// a cheaper encoder fails here and the documented figure is corrected instead
// of going stale.
//
// The figure is for the createUser fixture. A larger or deeper type costs
// whatever encoding/json charges for its shape.
//
// The measurement runs inside the handler because a *rice.Ctx is valid only
// there. AllocsPerRun's own bookkeeping is not attributed to the closure.
func TestAllocBudgetJSONBinding(t *testing.T) {
	const want float64 = 9

	var got float64
	app := rice.New()
	app.POST("/users", func(c *rice.Ctx) error {
		_, _ = binding.JSON[createUser](c) // warm
		got = testing.AllocsPerRun(1000, func() {
			_, _ = binding.JSON[createUser](c)
		})
		return nil
	})

	dispatch(t, app, `{"email":"a@b.c","age":30}`)

	if got != want {
		t.Errorf("binding.JSON allocated %.1f objects per call, want exactly %.0f", got, want)
	}
}
