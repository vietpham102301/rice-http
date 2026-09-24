package rice

import (
	"strconv"
	"sync"
	"testing"

	"github.com/valyala/fasthttp"
)

// raceIDKey is package-level, as keys are meant to be, so every concurrent
// request shares one key and only the Ctx separates their values.
var raceIDKey = NewKey[string]("id")

// TestConcurrentRequestsNeverSeeEachOthersState hammers the pool from many
// goroutines. Each request carries a distinct id in its path; a middleware copies
// it into the store; the handler echoes both. A Ctx shared between two in-flight
// requests, or one not fully reset between them, shows up as a response carrying
// someone else's id. make test and make test-debug both run this under -race.
func TestConcurrentRequestsNeverSeeEachOthersState(t *testing.T) {
	app := New()
	app.Use(func(next Handler) Handler {
		return func(c *Ctx) error {
			raceIDKey.Set(c, c.ParamString("id"))
			return next(c)
		}
	})
	app.GET("/users/:id", func(c *Ctx) error {
		stored, _ := raceIDKey.Get(c)
		return c.String(200, string(c.Param("id"))+"|"+stored)
	})
	h := app.FasthttpHandler()

	const workers, perWorker = 16, 500
	var wg sync.WaitGroup
	errs := make(chan string, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			fctx := &fasthttp.RequestCtx{}
			for i := 0; i < perWorker; i++ {
				id := strconv.Itoa(w*perWorker + i)
				fctx.Request.Reset()
				fctx.Response.Reset()
				fctx.Request.Header.SetMethod("GET")
				fctx.Request.SetRequestURI("/users/" + id)

				h(fctx)

				if got, want := string(fctx.Response.Body()), id+"|"+id; got != want {
					errs <- "body = " + got + ", want " + want
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)

	for e := range errs {
		t.Error(e)
	}
}
