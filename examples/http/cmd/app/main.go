package main

import "github.com/okian/servo/v3/servo"

// servo.Main builds the App with New, runs it until SIGINT, SIGTERM or
// SIGHUP, shuts it down under a deadline, and exits non-zero when anything
// along the way failed.
func main() { servo.Main(New) }
