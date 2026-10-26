package load

import (
	"fmt"
	"os/exec"
	"runtime/debug"
	"strings"

	"golang.org/x/tools/go/packages"
)

// toolsModule is the module whose gcimporter reads export data — the half
// of the coupling servo ships, and so the half its messages name.
const toolsModule = "golang.org/x/tools"

// exportDataError is a standard-library package whose export data the
// golang.org/x/tools linked into this binary could not decode.
//
// Left as x/tools reports it, that reads like a bug in the user's module:
// "internal error in importing "context" (… export data version 5 is
// greater than maximum supported version 4); please report an issue",
// exiting 1 like any other failure. It is neither. Every go/packages tool
// that reads export data is coupled to the toolchain's format, and a Go
// release that moves the format breaks the reader until x/tools catches up
// — so the message says which two versions disagree, and what moves
// either of them.
type exportDataError struct {
	pkg string // the package whose export data failed to decode
	err error  // the reader's own error
	// tools is the golang.org/x/tools version servo is built with, and
	// goVersion the release the go command reports — either "" when it
	// could not be found out.
	tools, goVersion string
	// fromSource is why the fallback, type-checking the standard library
	// from source, failed as well; nil while it has not been tried.
	fromSource error
}

func (e *exportDataError) Error() string {
	msg := fmt.Sprintf("servo: %s cannot read the export data %s wrote for %s (%v)", e.reader(), e.writer(), e.pkg, e.err)
	if e.fromSource != nil {
		msg += fmt.Sprintf(", and type-checking the standard library from source instead failed too: %v", e.fromSource)
	}
	return msg + " — " + e.remedy()
}

// note is the one line Load leaves for stderr when the fallback worked:
// the run succeeded, but every later one stays slower until a remedy is
// applied, so saying nothing would leave it that way.
func (e *exportDataError) note() string {
	return fmt.Sprintf("servo: note: %s cannot read the export data %s writes, so the standard library was type-checked from source instead, which is slower — %s",
		e.reader(), e.writer(), e.remedy())
}

func (e *exportDataError) reader() string {
	if e.tools == "" {
		return "the " + toolsModule + " this servo is built with"
	}
	return toolsModule + " " + e.tools
}

func (e *exportDataError) writer() string {
	if e.goVersion == "" {
		return "this Go release"
	}
	return e.goVersion
}

// remedy names both ways out. `go tool servo` is built against the
// consumer's own build list, so a newer x/tools there is a newer reader
// linked into servo, with no servo release involved; an installed binary
// carries the x/tools its release pinned, and only a newer servo moves it.
func (e *exportDataError) remedy() string {
	return "with `go tool servo`, run `go get " + toolsModule + "@latest` in your module; with an installed servo, install a newer one"
}

// toolsVersion is the golang.org/x/tools this binary links. Under `go tool
// servo` that is the consumer's build list's choice, not servo's go.mod's,
// which is exactly why it is read from the binary rather than assumed.
func toolsVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, m := range info.Deps {
		if m.Path != toolsModule {
			continue
		}
		if m.Replace != nil {
			// A directory replacement has no version to report.
			return m.Replace.Version
		}
		return m.Version
	}
	return ""
}

// goVersion is the release the go command reports from dir. It is asked
// rather than taken from runtime.Version: servo may have been built by
// another release, and a module's toolchain line can switch the go command
// to a third — the export data that failed is that one's.
func goVersion(dir string) string {
	cmd := exec.Command("go", "env", "GOVERSION")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// stdlibError is the first type or parse error in a standard-library
// package, which only a load that type-checks the standard library from
// source can produce, and only when this binary's go/types is older than
// the library it is reading. Errors of other kinds stay where they are: an
// import of a standard-library path that does not exist is the module's
// mistake, and NonInjectorErrors reports it as one.
func stdlibError(all []*packages.Package) error {
	for _, p := range all {
		if p.Module != nil {
			continue
		}
		for _, e := range p.Errors {
			if e.Kind == packages.TypeError || e.Kind == packages.ParseError {
				return e
			}
		}
	}
	return nil
}
