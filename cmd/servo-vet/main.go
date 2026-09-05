// Command servo-vet is the singlechecker binary wrapping the servo
// analyzer. The analyzer itself lives in github.com/okian/servo/v3/servovet
// so it can also be imported — by golangci-lint's module plugin system, by
// a multichecker, or by a test — which a var in package main cannot be.
package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/okian/servo/v3/servovet"
)

func main() {
	if msg, reject := inheritedTagsRefusal(os.Args[1:]); reject {
		fmt.Fprint(os.Stderr, msg)
		os.Exit(2)
	}
	singlechecker.Main(servovet.Analyzer)
}

// inheritedTagsRefusal turns a silent lie into an error.
//
// go/analysis registers a -tags flag on every singlechecker binary and
// documents it as "no effect (deprecated)": checker.Run builds its own
// packages.Config with no BuildFlags, so `servo-vet -tags=prod ./...`
// exits 0 having analysed only the default configuration. Anyone who typed
// it believes prod was covered. There is no hook to make it work — the
// config is internal to x/tools — so the honest move is to refuse and name
// the invocation that does.
//
// Scanned from os.Args rather than registered as a flag: the flag already
// exists on flag.CommandLine by the time Main runs, and registering a
// second one panics. It returns the message rather than printing and
// exiting so the decision can be tested without a subprocess.
func inheritedTagsRefusal(args []string) (string, bool) {
	for i, arg := range args {
		name, value, hasValue := strings.Cut(arg, "=")
		if name != "-tags" && name != "--tags" {
			continue
		}
		if !hasValue && i+1 < len(args) {
			value = args[i+1]
		}
		if value == "" {
			continue
		}
		return fmt.Sprintf(`servo-vet: -tags does not work here — it is go/analysis's own no-op flag, so this run would silently analyse only the default configuration.

To check a tagged configuration, drive servo-vet through the go command, which does understand build flags:

	go vet -tags=%s -vettool=$(which servo-vet) ./...
`, value), true
	}
	return "", false
}
