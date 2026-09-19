// Package compare measures rice against Gin, Echo and Fiber on identical
// scenarios, at the handler level and end to end.
//
// It is a module of its own so that rice's go.mod keeps fasthttp as its only
// dependency: Gin, Echo and Fiber are required here and nowhere else. See
// docs/superpowers/specs/2026-09-19-m8-benchmark-suite-design.md.
package compare

import "bytes"

// App names which of a framework's two apps serves a scenario. The GitHub API
// table declares /users/:user, which conflicts with the small app's /users/:id
// in routers that reject two parameter names at one position, so the table is
// served by an app of its own. It also keeps the other scenarios from being
// measured inside a 203-route tree.
type App int

const (
	// Small serves every scenario except githubapi.
	Small App = iota
	// GitHub serves the 203-route GitHub API table and nothing else.
	GitHub
)

// Scenario is one request every framework must answer the same way.
type Scenario struct {
	Name   string
	App    App
	Method string
	Path   string
	Body   []byte
	Status int
	// Want is the expected response body. It is ignored when StatusOnly is set.
	Want string
	// StatusOnly marks a scenario whose body is each framework's own: the
	// default 404 differs by design between frameworks.
	StatusOnly bool
}

// JSONPayload is what the json scenario encodes.
type JSONPayload struct {
	ID   int      `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

var jsonPayload = JSONPayload{ID: 42, Name: "rice", Tags: []string{"a", "b"}}

// body64k is the body64k scenario's request body.
var body64k = bytes.Repeat([]byte("r"), 64<<10)

// githubTarget is the route the githubapi scenario requests: three parameters
// deep in a table of 203.
const githubTarget = "/repos/julienschmidt/httprouter/pulls/42/comments"

// Scenarios returns every scenario, in the order results are reported.
func Scenarios() []Scenario {
	return []Scenario{
		{Name: "static", App: Small, Method: "GET", Path: "/hello", Status: 200, Want: "hello"},
		{Name: "param", App: Small, Method: "GET", Path: "/users/42", Status: 200, Want: "42"},
		{Name: "middleware5", App: Small, Method: "GET", Path: "/mw", Status: 200, Want: "ok"},
		{Name: "notfound", App: Small, Method: "GET", Path: "/nope", Status: 404, StatusOnly: true},
		{Name: "githubapi", App: GitHub, Method: "GET", Path: githubTarget, Status: 200, Want: "ok"},
		{Name: "json", App: Small, Method: "GET", Path: "/json", Status: 200, Want: `{"id":42,"name":"rice","tags":["a","b"]}`},
		{Name: "body64k", App: Small, Method: "POST", Path: "/echo-len", Body: body64k, Status: 200, Want: "65536"},
	}
}

// ScenarioByName returns the scenario with the given name.
func ScenarioByName(name string) (Scenario, bool) {
	for _, s := range Scenarios() {
		if s.Name == name {
			return s, true
		}
	}
	return Scenario{}, false
}
