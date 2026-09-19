package compare

import (
	"encoding/json"
	"strconv"

	rice "github.com/vietpham102301/rice-http"
)

// Rice is rice, used the way its documentation shows.
func Rice() Target { return Target{Name: "rice", Build: buildRice} }

func buildRice(app App) Server {
	r := rice.New()
	switch app {
	case Small:
		r.GET("/hello", func(c *rice.Ctx) error { return c.String(200, "hello") })
		r.GET("/users/:id", func(c *rice.Ctx) error { return c.Bytes(200, c.Param("id")) })
		r.GET("/mw", func(c *rice.Ctx) error { return c.String(200, "ok") },
			riceNoop, riceNoop, riceNoop, riceNoop, riceNoop)
		// rice has no JSON helper (not added in M8): the handler encodes with the
		// standard library, as a rice user would today.
		r.GET("/json", func(c *rice.Ctx) error {
			b, err := json.Marshal(jsonPayload)
			if err != nil {
				return err
			}
			c.SetContentType("application/json")
			return c.Bytes(200, b)
		})
		// rice has no body accessor: RequestCtx is the documented escape hatch.
		r.POST("/echo-len", func(c *rice.Ctx) error {
			return c.String(200, strconv.Itoa(len(c.RequestCtx().PostBody())))
		})
	case GitHub:
		for _, rt := range githubAPI {
			r.Handle(rt.Method, rt.Path, func(c *rice.Ctx) error { return c.String(200, "ok") })
		}
	}
	return Server{Fasthttp: r.FasthttpHandler(), Serve: r.Serve}
}

func riceNoop(next rice.Handler) rice.Handler {
	return func(c *rice.Ctx) error { return next(c) }
}
