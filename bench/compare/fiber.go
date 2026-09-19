package compare

import (
	"net"
	"strconv"

	"github.com/gofiber/fiber/v3"
)

// Fiber is Fiber v3 with its default configuration.
func Fiber() Target { return Target{Name: "fiber", Build: buildFiber} }

func buildFiber(app App) Server {
	f := fiber.New()
	switch app {
	case Small:
		f.Get("/hello", func(c fiber.Ctx) error { return c.SendString("hello") })
		f.Get("/users/:id", func(c fiber.Ctx) error { return c.SendString(c.Params("id")) })
		// Fiber runs a route's handlers in order: the five no-ops, then the handler.
		f.Get("/mw", fiberNoop, fiberNoop, fiberNoop, fiberNoop, fiberNoop,
			func(c fiber.Ctx) error { return c.SendString("ok") })
		f.Get("/json", func(c fiber.Ctx) error { return c.JSON(jsonPayload) })
		f.Post("/echo-len", func(c fiber.Ctx) error {
			return c.SendString(strconv.Itoa(len(c.Body())))
		})
	case GitHub:
		for _, rt := range githubAPI {
			f.Add([]string{rt.Method}, rt.Path, func(c fiber.Ctx) error { return c.SendString("ok") })
		}
	}
	return Server{
		Fasthttp: f.Handler(),
		Serve: func(ln net.Listener) error {
			return f.Listener(ln, fiber.ListenConfig{DisableStartupMessage: true})
		},
	}
}

func fiberNoop(c fiber.Ctx) error { return c.Next() }
