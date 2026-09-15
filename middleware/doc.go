// Package middleware holds rice's optional, opt-in middleware.
//
// Nothing here is imported by package rice. Importing rice must not drag in
// anything a user did not ask for, and the import graph is the honest signal of
// what costs what — which is why this is a separate package rather than a
// subdirectory of helpers or a set of methods on App.
//
// The dependency runs one way: middleware imports rice. Nothing under
// internal/ may import rice; middleware/ is not under internal/, so it may.
package middleware
