package rice

// Handler is the single unit of work in rice.
//
// It receives a borrowed *Ctx and returns an error or nil. Returning a non-nil
// error is the only way to signal failure: there is no abort flag and no
// sentinel status the framework inspects. If you see "return err" in a handler,
// the request is over.
//
// See docs/adr/0002-handler-returns-error.md.
type Handler func(c *Ctx) error
