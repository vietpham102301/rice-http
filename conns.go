package rice

import (
	"net"

	"github.com/valyala/fasthttp"
)

// connState is installed as fasthttp.Server.ConnState. It keeps the set of open
// connections that Shutdown force-closes when its deadline passes: fasthttp's
// ShutdownWithContext resets its stop flag when it times out, so a busy
// keep-alive connection would otherwise go on serving after Shutdown returned.
// See ADR-0009.
//
// fasthttp calls it on every request with StateActive and StateIdle. Those
// return before touching the lock, so the per-request cost is the call itself.
func (a *App) connState(c net.Conn, s fasthttp.ConnState) {
	switch s {
	case fasthttp.StateNew:
		a.connMu.Lock()
		if a.forceClosed {
			a.connMu.Unlock()
			_ = c.Close()
			return
		}
		if a.conns == nil {
			a.conns = make(map[net.Conn]struct{})
		}
		a.conns[c] = struct{}{}
		a.connMu.Unlock()
	case fasthttp.StateClosed, fasthttp.StateHijacked:
		a.connMu.Lock()
		delete(a.conns, c)
		a.connMu.Unlock()
	}
}

// closeConns closes every tracked connection and makes connState close any
// connection reported after it. Each serving goroutine's next read or write
// then fails, and fasthttp reports StateClosed, which untracks it.
//
// The closes happen outside connMu: a tls.Conn's Close can block for seconds
// sending close_notify, and connState needs the lock for every connection that
// opens or closes meanwhile. Setting forceClosed under the lock first is what
// keeps that safe: a connection reported after it is closed on arrival, so
// nothing escapes the copy.
func (a *App) closeConns() {
	a.connMu.Lock()
	a.forceClosed = true
	conns := make([]net.Conn, 0, len(a.conns))
	for c := range a.conns {
		conns = append(conns, c)
	}
	a.connMu.Unlock()

	for _, c := range conns {
		_ = c.Close()
	}
}
