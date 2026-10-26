// Command app builds one of two graphs depending on the build tags.
//
//	go build ./...              -> wired against memory.Mem
//	go build -tags=prod ./...   -> wired against postgres.DB
//
// main.go itself is identical in both: New comes from whichever generated
// file the build selects, and nothing here mentions a tag.
package main

import "github.com/okian/servo/v3/servo"

// servo.Main builds the App with New, runs it until SIGINT, SIGTERM or
// SIGHUP, shuts it down under a deadline, and exits non-zero when anything
// along the way failed.
func main() { servo.Main(New) }
