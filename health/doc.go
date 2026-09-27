// Package health serves liveness and readiness probes for a rice App.
//
//	app := rice.New(rice.WithDrainDelay(5 * time.Second))
//	app.GET("/livez", health.Live())
//	app.GET("/readyz", health.Ready(app))
//
// Live answers 200 while the process can answer at all. Ready answers 200
// while the App is serving and not shutting down, and 503 from the moment
// Shutdown begins; with rice.WithDrainDelay the App goes on serving for the
// delay, so the load balancer sees the 503, stops routing here, and nothing
// is refused. Ready takes optional Checks for dependencies; read its doc
// before adding one.
//
// # Where probes sit
//
// Probes are ordinary routes, so middleware installed with app.Use runs on
// them. Install authentication on groups rather than with app.Use, or the
// kubelet's probe is answered 401; a Logger installed with app.Use logs every
// probe.
//
// To keep probes away from the application's middleware altogether, serve
// them from a second App on a port of their own. Ready reads the main App's
// state from there:
//
//	probes := rice.New()
//	probes.GET("/livez", health.Live())
//	probes.GET("/readyz", health.Ready(app))
//	go probes.Run(":8081")
//
// The probe App is not drained: shut it down after the main App.
//
// A Kubernetes pod using both:
//
//	livenessProbe:  {httpGet: {path: /livez, port: 8080}}
//	readinessProbe: {httpGet: {path: /readyz, port: 8080}, periodSeconds: 2}
//	terminationGracePeriodSeconds: 30 # > drain delay + RunContext's grace + OnShutdown hooks
//
// See docs/adr/0023-shutdown-drains-before-it-closes.md.
package health
