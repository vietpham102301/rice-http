package rice

import (
	"github.com/valyala/fasthttp"

	"github.com/vietpham102301/rice-http/internal/router"
)

// newCtx builds a Ctx for the pool.
//
// It reads maxParams when it runs rather than when the pool was created, so a
// Ctx built after more routes were registered is sized for them. A Ctx built
// before that is not, and grows on its first oversized capture — see the M6
// design doc, D3.
//
// It must allocate no more than three objects. Under -race, sync.Pool drops one
// Put in four, so every dispatch budget of zero in alloc_test.go absorbs a
// quarter of this function's cost; at four objects that reaches a whole
// allocation and every zero budget fails under make test.
func (a *App) newCtx() *Ctx {
	return &Ctx{
		params: router.MakeParams(a.maxParams),
		store:  make([]entry, 0, storeCapacity),
	}
}

// acquire takes a Ctx from the pool and binds it to fctx.
func (a *App) acquire(fctx *fasthttp.RequestCtx) *Ctx {
	c := a.pool.Get().(*Ctx)
	c.reset(a, fctx)
	return c
}

// release unbinds c and hands it back.
//
// It drops every reference c holds, so a pooled Ctx never keeps a finished
// request's memory reachable. In the debug build it then poisons c and keeps it
// out of the pool, so every later use panics; in the release build the poison is
// a no-op and c returns to the pool. poolReuse is a constant, so the branch not
// taken is compiled out.
func (a *App) release(c *Ctx) {
	c.reset(nil, nil)
	c.poison.mark()
	if poolReuse {
		a.pool.Put(c)
	}
}
