package compare

import (
	"io"
	"net"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"
)

// Echo is Echo v5 with no middleware added.
func Echo() Target { return Target{Name: "echo", Build: buildEcho} }

func buildEcho(app App) Server {
	e := echo.New()
	switch app {
	case Small:
		e.GET("/hello", func(c *echo.Context) error { return c.String(200, "hello") })
		e.GET("/users/:id", func(c *echo.Context) error { return c.String(200, c.Param("id")) })
		e.GET("/mw", func(c *echo.Context) error { return c.String(200, "ok") },
			echoNoop, echoNoop, echoNoop, echoNoop, echoNoop)
		e.GET("/json", func(c *echo.Context) error { return c.JSON(200, jsonPayload) })
		e.POST("/echo-len", func(c *echo.Context) error {
			b, err := io.ReadAll(c.Request().Body)
			if err != nil {
				return err
			}
			return c.String(200, strconv.Itoa(len(b)))
		})
	case GitHub:
		for _, rt := range githubAPI {
			e.Add(rt.Method, rt.Path, func(c *echo.Context) error { return c.String(200, "ok") })
		}
	}
	return Server{
		HTTP:  e,
		Serve: func(ln net.Listener) error { return (&http.Server{Handler: e}).Serve(ln) },
	}
}

func echoNoop(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c *echo.Context) error { return next(c) }
}
