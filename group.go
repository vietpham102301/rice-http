package rice

// Group is a route prefix plus middleware. M4 Task 3 gives it its constructor and
// its registration methods; it is declared here because the build phase needs its
// shape to walk a route's group chain.
type Group struct {
	app    *App
	parent *Group
	prefix string
	mws    []Middleware
}
