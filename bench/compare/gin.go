package compare

import (
	"net"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// Gin is Gin in release mode with no middleware: gin.New, not gin.Default,
// which would add Logger and Recovery that the others do not run.
func Gin() Target { return Target{Name: "gin", Build: buildGin} }

func buildGin(app App) Server {
	gin.SetMode(gin.ReleaseMode)
	g := gin.New()
	switch app {
	case Small:
		g.GET("/hello", func(c *gin.Context) { c.String(200, "hello") })
		// c.String with no format arguments writes the string as is.
		g.GET("/users/:id", func(c *gin.Context) { c.String(200, c.Param("id")) })
		g.GET("/mw", ginNoop, ginNoop, ginNoop, ginNoop, ginNoop,
			func(c *gin.Context) { c.String(200, "ok") })
		g.GET("/json", func(c *gin.Context) { c.JSON(200, jsonPayload) })
		g.POST("/echo-len", func(c *gin.Context) {
			b, err := c.GetRawData()
			if err != nil {
				c.AbortWithStatus(500)
				return
			}
			c.String(200, strconv.Itoa(len(b)))
		})
	case GitHub:
		for _, rt := range githubAPI {
			g.Handle(rt.Method, rt.Path, func(c *gin.Context) { c.String(200, "ok") })
		}
	}
	return Server{
		HTTP:  g,
		Serve: func(ln net.Listener) error { return (&http.Server{Handler: g}).Serve(ln) },
	}
}

func ginNoop(c *gin.Context) { c.Next() }
