package rice

import "github.com/vietpham102301/rice-http/internal/chain"

// chainCompileForTest exposes the chain compiler to this package's tests, kept
// out of build.go so the symbol never ships in a production binary or shows up
// in godoc for library consumers. It is not used by production code; the build
// phase calls chain.Compile directly.
func chainCompileForTest(h Handler, mws []Middleware) Handler {
	return chain.Compile(h, mws)
}
