// Package bench holds rice's benchmark suite.
//
// # Why benchmarks live outside package rice
//
// They import rice the way a user does, through its exported API only. A
// benchmark with access to unexported internals measures a program no user can
// write.
//
// # Two levels of measurement, and why
//
// Handler-level benchmarks call the fasthttp request handler directly on a
// reused *fasthttp.RequestCtx. They exclude connection handling, parsing and
// socket I/O, which makes them the right place to assert allocation budgets:
// the number that comes out is attributable to framework code and nothing else.
//
// End-to-end benchmarks that drive a real client measure the client as much as
// the server, so their allocation counts are not a contract. Correctness over a
// real socket is proven in server_test.go instead.
//
// BenchmarkFasthttpBaseline is the zero point: fasthttp with no framework at
// all. Every rice benchmark is read as a delta from it.
package bench
