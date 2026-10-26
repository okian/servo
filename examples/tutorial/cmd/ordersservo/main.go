package main

import "github.com/okian/servo/v3/servo"

// main is the whole binary: the listener, the routes, the middleware chain
// and the admin story all live in the graph or the generated file now, and
// servo.Main does the rest — runs the App until SIGINT, SIGTERM or SIGHUP,
// shuts it down under a deadline, logs any failure at ERROR and exits 1
// for it. The admin listener cmd/orders mounts beside its App could be
// mounted here the same way; the chapter keeps this main minimal so the
// difference stays visible.
func main() { servo.Main(New) }
