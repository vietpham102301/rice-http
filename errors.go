package rice

import "errors"

// ErrNotFound is passed into the error funnel when no route matches the
// request. The error handler turns it into a 404.
//
// It is a package-level value created once at init, so returning it costs no
// allocation — which matters, because it is returned on the path that mistaken
// and hostile traffic hits hardest.
//
// M5 generalises the funnel to an HTTPError type. errors.Is keeps working
// against this sentinel, so checks written against it today keep working then.
var ErrNotFound = errors.New("rice: not found")
